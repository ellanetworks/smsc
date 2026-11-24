package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	reencryptBatch     = 200
	reencryptPause     = 50 * time.Millisecond
	tidyInterval       = 10 * time.Minute
	probeInterval      = time.Minute
	probeRetryInterval = 10 * time.Second
)

func (m *keyManager) tidy() {
	select {
	case m.tidying <- struct{}{}:
	default:
	}
}

func (m *keyManager) maintain() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-m.n.done
		cancel()
	}()

	ticker := time.NewTicker(tidyInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.tidying:
		}

		if leading, _ := m.n.Leader(); !leading || !m.keys.Unlocked() {
			continue
		}

		if err := m.tidyKeys(ctx); err != nil && ctx.Err() == nil {
			m.n.cfg.Logger.Warn("failed to move stored values to the active data key", slog.Any("error", err))
		}
	}
}

func (m *keyManager) tidyKeys(ctx context.Context) error {
	for {
		n, undecryptable, err := m.n.db.ReencryptStale(ctx, reencryptBatch)
		if err != nil {
			return err
		}

		if undecryptable > 0 {
			m.n.cfg.Logger.ErrorContext(ctx, "stored values cannot be decrypted, and stay under an older data key",
				slog.Int("values", undecryptable))
		}

		if n == 0 || n == undecryptable {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reencryptPause):
		}
	}

	return m.pruneTerms(ctx)
}

func (m *keyManager) pruneTerms(ctx context.Context) error {
	m.changeMu.Lock()
	defer m.changeMu.Unlock()

	m.unlockMu.Lock()

	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil || len(st.Wraps) != 1 || digestOf(st) != m.installed {
		m.unlockMu.Unlock()
		return err
	}

	unused, err := m.n.db.UnusedTerms(ctx, st)
	if err != nil || len(unused) == 0 {
		m.unlockMu.Unlock()
		return err
	}

	ck, err := m.clusterKey(st)
	if err != nil {
		m.unlockMu.Unlock()
		return err
	}

	sealed, err := m.keys.Prune(ck, unused)
	ck.Zeroize()
	m.unlockMu.Unlock()

	if err != nil {
		return err
	}

	if err := m.n.db.PruneTerms(ctx, st, sealed, unused); err != nil {
		return err
	}

	m.audit(ctx, "remove_data_keys", fmt.Sprintf("Data keys of terms %v removed: no stored value uses them", unused))

	return nil
}

func (m *keyManager) probe() {
	interval := probeInterval
	available := true

	for {
		select {
		case <-m.n.done:
			return
		case <-time.After(interval):
		}

		if !m.Status().Initialized {
			continue
		}

		err := m.probeKey()

		m.n.metrics.setKeyAvailable(err == nil)

		switch {
		case err != nil && available:
			m.n.cfg.Logger.Warn("the encryption key is unavailable: this node keeps its loaded keys",
				slog.Any("error", err))
		case err == nil && !available:
			m.n.cfg.Logger.Info("the encryption key is available again")
			m.poke()
		}

		available = err == nil
		interval = probeInterval

		if !available {
			interval = probeRetryInterval
		}
	}
}

func (m *keyManager) probeKey() error {
	if err := m.rejectedErr(); err != nil {
		return err
	}

	k, err := m.cfg.Key.load()
	if err != nil {
		return err
	}

	fingerprint := k.Fingerprint()
	k.Zeroize()

	if unlocked := m.unlockedWith(); m.isConfigured() && unlocked != "" && unlocked != fingerprint {
		return fmt.Errorf("the key at %s is %s, but this node runs on %s: it locks at its next restart unless "+
			"%s is the previous key", m.cfg.Key, fingerprint, unlocked, unlocked)
	}

	return nil
}
