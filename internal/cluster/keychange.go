package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
)

const highWaterFile = "encryption.term"

var (
	ErrNotInitialized = errors.New("encryption at rest is not initialized yet")
	ErrKeysRolledBack = errors.New("the encryption keys were rolled back")
)

type highWater struct {
	KeyringID  []byte `json:"keyring_id"`
	ActiveTerm uint32 `json:"active_term"`
}

func (m *keyManager) highWaterPath() string {
	return filepath.Join(m.n.cfg.Dir, highWaterFile)
}

func (m *keyManager) checkHighWater(st db.EncryptionKeys) error {
	raw, err := os.ReadFile(m.highWaterPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	var hw highWater
	if err := json.Unmarshal(raw, &hw); err != nil {
		return fmt.Errorf("read %s: %w", m.highWaterPath(), err)
	}

	if bytes.Equal(hw.KeyringID, st.KeyringID) && st.ActiveTerm < hw.ActiveTerm {
		return fmt.Errorf("%w: the active term is %d, and this node has seen %d", ErrKeysRolledBack, st.ActiveTerm,
			hw.ActiveTerm)
	}

	return nil
}

func (m *keyManager) raiseHighWater(st db.EncryptionKeys) error {
	raw, err := os.ReadFile(m.highWaterPath())
	if err == nil {
		var hw highWater
		if json.Unmarshal(raw, &hw) == nil && bytes.Equal(hw.KeyringID, st.KeyringID) && hw.ActiveTerm >= st.ActiveTerm {
			return nil
		}
	}

	data, err := json.Marshal(highWater{KeyringID: st.KeyringID, ActiveTerm: st.ActiveTerm})
	if err != nil {
		return err
	}

	return writeFile(m.highWaterPath(), data)
}

func (m *keyManager) unlockedWith() string {
	if !m.keys.Unlocked() {
		return ""
	}

	return m.Status().Fingerprint
}

func (m *keyManager) audit(ctx context.Context, action, details string) {
	m.n.cfg.Logger.InfoContext(ctx, "audit event", slog.String("action", action),
		slog.String("actor", "node:"+m.n.id), slog.String("details", details))
}

func (n *Node) RotateKeys(ctx context.Context) (EncryptionStatus, error) {
	if leading, _ := n.Leader(); !leading {
		return EncryptionStatus{}, n.notLeader(ctx)
	}

	return n.keyManager.rotate(ctx)
}

func (m *keyManager) rotate(ctx context.Context) (EncryptionStatus, error) {
	m.changeMu.Lock()
	defer m.changeMu.Unlock()

	change, err := m.prepareRotation(ctx)
	if err != nil {
		return EncryptionStatus{}, err
	}

	if err := m.n.db.ChangeKeys(ctx, change); err != nil {
		return EncryptionStatus{}, err
	}

	_ = m.unlock(ctx, false)

	s := m.Status()
	s.ActiveTerm = change.Term

	return s, nil
}

func (m *keyManager) prepareRotation(ctx context.Context) (db.KeyChange, error) {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	if err := m.unlockLocked(ctx, false); err != nil && !m.keys.Unlocked() {
		return db.KeyChange{}, fmt.Errorf("%w: %w", ErrEncryptionLocked, err)
	}

	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		return db.KeyChange{}, err
	}

	switch {
	case !st.Initialized():
		return db.KeyChange{}, ErrNotInitialized
	case len(st.Wraps) != 1:
		return db.KeyChange{}, db.ErrKeyChange
	case digestOf(st) != m.installed:
		return db.KeyChange{}, db.ErrKeysChanged
	}

	ck, err := m.clusterKey(st)
	if err != nil {
		return db.KeyChange{}, err
	}

	defer ck.Zeroize()

	return m.prepareChange(ctx, st, ck, false)
}

func (m *keyManager) prepareChange(ctx context.Context, st db.EncryptionKeys, ck encryption.ClusterKey,
	retire bool,
) (db.KeyChange, error) {
	sealed, next, upgrade, err := m.keys.Rotate(ck)
	if err != nil {
		return db.KeyChange{}, err
	}

	defer next.Zeroize()

	return m.n.db.PrepareKeyChange(ctx, st, retire, sealed, next, upgrade)
}

type keyChange struct {
	add    *encryption.RootWrap
	retire *db.KeyChange
	st     db.EncryptionKeys
	target string
}

func (m *keyManager) changeKey(ctx context.Context) error {
	m.changeMu.Lock()
	defer m.changeMu.Unlock()

	c, err := m.planKeyChange(ctx)
	if err != nil || c == nil {
		return err
	}

	switch {
	case c.add != nil:
		if err := m.n.db.AddRootWrap(ctx, c.st, *c.add); err != nil {
			return fmt.Errorf("add the cluster key %s: %w", c.target, err)
		}

		m.audit(ctx, "add_cluster_key", "Cluster key "+c.target+" added")
	case c.retire != nil:
		if err := m.n.db.ChangeKeys(ctx, *c.retire); err != nil {
			return fmt.Errorf("retire the previous cluster key: %w", err)
		}

		m.audit(ctx, "retire_cluster_key", "Every member uses the cluster key "+c.target+
			": the previous key is no longer used, and new data keys replace the old ones")
	}

	return nil
}

func (m *keyManager) planKeyChange(ctx context.Context) (*keyChange, error) {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	if !m.keys.Unlocked() {
		return nil, nil
	}

	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil || !st.Initialized() {
		return nil, err
	}

	if m.isConfigured() && len(st.Wraps) == 1 {
		return nil, nil
	}

	target, err := m.cfg.Key.load()
	if err != nil {
		m.n.cfg.Logger.WarnContext(ctx, "cannot load encryption.key to change the cluster key", slog.Any("error", err))
		return nil, nil
	}

	defer target.Zeroize()

	c := &keyChange{st: st, target: target.Fingerprint()}

	if !hasWrap(st, target) {
		if len(st.Wraps) != 1 {
			m.n.cfg.Logger.WarnContext(ctx, "a change to another cluster key is in progress: finish it before changing to "+
				c.target, slog.Any("keys", fingerprints(st)))

			return nil, nil
		}

		from, err := m.clusterKey(st)
		if err != nil {
			return nil, err
		}

		defer from.Zeroize()

		w, err := encryption.Rewrap(target, st.Wraps, st.Keyring, st.KeyringID, from)
		if err != nil {
			return nil, err
		}

		c.add = &w

		return c, nil
	}

	if len(st.Wraps) == 1 {
		return nil, nil
	}

	if leading, _ := m.n.Leader(); !leading {
		return nil, nil
	}

	members, err := m.n.db.Members(ctx)
	if err != nil {
		return nil, err
	}

	var waiting []string

	for _, member := range members {
		key := member.EncryptionKey
		if member.NodeID == m.n.id {
			key = m.unlockedWith()
		}

		if key != c.target {
			waiting = append(waiting, member.NodeID)
		}
	}

	if len(waiting) > 0 {
		m.n.cfg.Logger.InfoContext(ctx, "the cluster key change waits for members to use "+c.target,
			slog.Any("members", waiting))

		return nil, nil
	}

	if digestOf(st) != m.installed {
		return nil, nil
	}

	change, err := m.prepareChange(ctx, st, target, true)
	if err != nil {
		return nil, err
	}

	c.retire = &change

	return c, nil
}

func (m *keyManager) backupKeyring(backup db.EncryptionKeys) (*encryption.Keyring, error) {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	var tried []string

	for _, source := range m.sources() {
		k, err := source.load()
		if err != nil {
			continue
		}

		tried = append(tried, k.Fingerprint())

		if !hasWrap(backup, k) {
			k.Zeroize()
			continue
		}

		keyring, err := encryption.Unlock(k, backup.Wraps, backup.Keyring, backup.KeyringID)
		k.Zeroize()

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnrestorableBackup, err)
		}

		if keyring.ActiveTerm() != backup.ActiveTerm {
			keyring.Zeroize()

			return nil, fmt.Errorf("%w: its keyring's active term %d is not its state's %d", ErrUnrestorableBackup,
				keyring.ActiveTerm(), backup.ActiveTerm)
		}

		return keyring, nil
	}

	return nil, fmt.Errorf("%w: it is encrypted under the cluster key %v, and this node has %v: configure that "+
		"key as encryption.previous_key", ErrUnrestorableBackup, fingerprints(backup), tried)
}
