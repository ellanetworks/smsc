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
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	if err := m.unlockLocked(ctx, false); err != nil {
		return EncryptionStatus{}, fmt.Errorf("%w: %w", ErrEncryptionLocked, err)
	}

	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		return EncryptionStatus{}, err
	}

	switch {
	case !st.Initialized():
		return EncryptionStatus{}, ErrNotInitialized
	case len(st.Wraps) != 1:
		return EncryptionStatus{}, db.ErrKeyChange
	}

	ck, err := m.clusterKey(st)
	if err != nil {
		return EncryptionStatus{}, err
	}

	defer ck.Zeroize()

	sealed, next, err := m.keys.Rotate(ck)
	if err != nil {
		return EncryptionStatus{}, err
	}

	next.Zeroize()

	if err := m.n.db.RotateKeys(ctx, st.ActiveTerm, sealed); err != nil {
		return EncryptionStatus{}, err
	}

	if err := m.unlockLocked(ctx, false); err != nil {
		return EncryptionStatus{}, err
	}

	return m.Status(), nil
}

type keyChange struct {
	add    *encryption.RootWrap
	retire *encryption.RootWrap
	st     db.EncryptionKeys
	target string
}

func (m *keyManager) changeKey(ctx context.Context) error {
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
		if err := m.n.db.RemoveRootWrapsBut(ctx, *c.retire); err != nil {
			return fmt.Errorf("retire the previous cluster key: %w", err)
		}

		m.audit(ctx, "retire_cluster_key", "Every member uses the cluster key "+c.target+
			": the previous key is no longer used")
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

	if m.configured && len(st.Wraps) == 1 {
		return nil, nil
	}

	target, err := m.cfg.Key.load()
	if err != nil {
		return nil, nil
	}

	defer target.Zeroize()

	c := &keyChange{st: st, target: target.Fingerprint()}

	if !hasWrap(st, target) {
		if len(st.Wraps) != 1 {
			return nil, nil
		}

		from, err := m.clusterKey(st)
		if err != nil {
			return nil, err
		}

		defer from.Zeroize()

		w, err := encryption.Rewrap(target, st.Wraps, st.Keyring, from)
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

	for _, member := range members {
		key := member.EncryptionKey
		if member.NodeID == m.n.id {
			key = m.unlockedWith()
		}

		if key != c.target {
			return nil, nil
		}
	}

	for _, w := range st.Wraps {
		if w.Fingerprint == target.Fingerprint() && w.Type == target.Type() && w.Mechanism == target.Mechanism() {
			c.retire = &w
		}
	}

	return c, nil
}

type adoption struct {
	backup db.EncryptionKeys
	wrap   encryption.RootWrap
	keys   *encryption.Keys
}

func (m *keyManager) adopt(live, backup db.EncryptionKeys) (*adoption, error) {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	if len(live.Wraps) != 1 {
		return nil, db.ErrKeyChange
	}

	current, err := m.clusterKey(live)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEncryptionLocked, err)
	}

	defer current.Zeroize()

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

		a, err := openBackup(k, current, backup)
		k.Zeroize()

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnrestorableBackup, err)
		}

		return a, nil
	}

	return nil, fmt.Errorf("%w: it is encrypted under another cluster's key %v, and this node has %v: configure that "+
		"key as encryption.previous_key", ErrUnrestorableBackup, fingerprints(backup), tried)
}

func openBackup(k, current encryption.ClusterKey, backup db.EncryptionKeys) (*adoption, error) {
	keyring, err := encryption.Unlock(k, backup.Wraps, backup.Keyring)
	if err != nil {
		return nil, err
	}

	if keyring.ActiveTerm() != backup.ActiveTerm {
		keyring.Zeroize()

		return nil, fmt.Errorf("its keyring's active term %d is not its state's %d", keyring.ActiveTerm(),
			backup.ActiveTerm)
	}

	wrap, err := encryption.Rewrap(current, backup.Wraps, backup.Keyring, k)
	if err != nil {
		keyring.Zeroize()

		return nil, err
	}

	a := &adoption{backup: backup, wrap: wrap, keys: &encryption.Keys{}}
	a.keys.Install(keyring)

	return a, nil
}
