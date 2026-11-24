package cluster

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/hashicorp/raft"
)

const (
	KeyTypeFile   = encryption.TypeFile
	KeyTypePKCS11 = encryption.TypePKCS11

	defaultKeyFile   = "encryption.key"
	unlockMinBackoff = 5 * time.Second
	unlockMaxBackoff = time.Minute
)

var (
	ErrEncryptionLocked = encryption.ErrLocked

	errNoKeyFile = errors.New("no encryption key file")
)

type KeySource struct {
	Type   string
	Path   string
	PKCS11 *encryption.PKCS11Config
}

func (s KeySource) load() (encryption.ClusterKey, error) {
	if s.Type == KeyTypePKCS11 {
		if s.PKCS11 == nil {
			return nil, errors.New("the pkcs11 cluster key has no configuration")
		}

		k, err := encryption.LoadPKCS11Key(*s.PKCS11)
		if err != nil {
			return nil, err
		}

		return k, nil
	}

	k, err := encryption.LoadFileKey(s.Path)
	if err != nil {
		return nil, err
	}

	return k, nil
}

func (s KeySource) String() string {
	if s.Type == KeyTypePKCS11 && s.PKCS11 != nil {
		return "the pkcs11 key " + s.PKCS11.KeyLabel
	}

	return s.Path
}

type EncryptionConfig struct {
	Key      KeySource
	Previous *KeySource
}

type EncryptionStatus struct {
	Initialized bool
	Locked      bool
	Fingerprint string
	ActiveTerm  uint32
	Detail      string
}

type keyManager struct {
	n    *Node
	cfg  EncryptionConfig
	keys encryption.Keys
	wake chan struct{}

	unlockMu   sync.Mutex
	installed  [sha256.Size]byte
	configured bool
	changed    chan struct{}

	mu     sync.Mutex
	status EncryptionStatus
}

func newKeyManager(n *Node, cfg EncryptionConfig) *keyManager {
	return &keyManager{
		n: n, cfg: cfg, wake: make(chan struct{}, 1), changed: make(chan struct{}, 1),
	}
}

func (m *keyManager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *keyManager) onEvents(ev db.Events) {
	if ev.Encryption || ev.Replaced || ev.Members || ev.Migrated > 0 {
		m.poke()
	}
}

func (m *keyManager) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-m.n.done
		cancel()
	}()

	var backoff time.Duration

	for {
		var retry <-chan time.Time

		err := m.unlock(ctx, true)
		if err == nil {
			err = m.changeKey(ctx)
			if err != nil && ctx.Err() == nil {
				m.n.cfg.Logger.Warn("failed to change the cluster key", slog.Any("error", err))
			}
		}

		if err != nil && ctx.Err() == nil {
			backoff = min(max(2*backoff, unlockMinBackoff), unlockMaxBackoff)
			retry = time.After(backoff)
		} else {
			backoff = 0
		}

		select {
		case <-ctx.Done():
			m.keys.Lock()
			return
		case <-m.wake:
		case <-retry:
		}
	}
}

func (m *keyManager) unlock(ctx context.Context, nudge bool) error {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	return m.unlockLocked(ctx, nudge)
}

func (m *keyManager) unlockLocked(ctx context.Context, nudge bool) error {
	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		m.setDetail(err)
		return err
	}

	if !st.Initialized() {
		if m.keys.Unlocked() {
			m.signal()
		}

		m.keys.Lock()
		m.installed = [sha256.Size]byte{}
		m.set(EncryptionStatus{}, nil)

		return nil
	}

	digest := digestOf(st)
	if m.keys.Unlocked() && digest == m.installed {
		return nil
	}

	if err := m.checkHighWater(st); err != nil {
		m.lock(st, "", err)
		return err
	}

	ck, source, err := m.matchingKey(st)
	if err != nil {
		m.lock(st, "", err)
		return err
	}

	fingerprint := ck.Fingerprint()

	keyring, err := encryption.Unlock(ck, st.Wraps, st.Keyring)
	ck.Zeroize()

	if err != nil {
		m.lock(st, fingerprint, err)
		return err
	}

	if keyring.ActiveTerm() != st.ActiveTerm {
		keyring.Zeroize()

		err := fmt.Errorf("the keyring's active term %d is not the cluster's %d", keyring.ActiveTerm(), st.ActiveTerm)
		m.lock(st, fingerprint, err)

		return err
	}

	if err := m.raiseHighWater(st); err != nil {
		keyring.Zeroize()
		m.lock(st, fingerprint, fmt.Errorf("record the active term: %w", err))

		return err
	}

	wasLocked := !m.keys.Unlocked()

	m.keys.Install(keyring)
	m.installed = digest
	m.configured = source == 0
	m.set(EncryptionStatus{Initialized: true, Fingerprint: fingerprint, ActiveTerm: st.ActiveTerm}, nil)
	m.signal()

	if wasLocked {
		m.n.cfg.Logger.InfoContext(ctx, "encryption keys unlocked", slog.String("key", fingerprint),
			slog.Uint64("term", uint64(st.ActiveTerm)))

		if nudge {
			m.n.reestablish(false)
		}
	}

	return nil
}

func digestOf(st db.EncryptionKeys) [sha256.Size]byte {
	h := sha256.New()
	_ = binary.Write(h, binary.BigEndian, st.ActiveTerm)

	for _, b := range append([][]byte{st.Keyring}, wrapBytes(st)...) {
		_ = binary.Write(h, binary.BigEndian, uint32(len(b)))
		h.Write(b)
	}

	var out [sha256.Size]byte

	copy(out[:], h.Sum(nil))

	return out
}

func wrapBytes(st db.EncryptionKeys) [][]byte {
	out := make([][]byte, 0, 4*len(st.Wraps))
	for _, w := range st.Wraps {
		out = append(out, []byte(w.Fingerprint), []byte(w.Type), []byte(w.Mechanism), w.Wrapped)
	}

	return out
}

func (m *keyManager) signal() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (m *keyManager) lock(st db.EncryptionKeys, fingerprint string, err error) {
	wasLocked := m.Status().Locked

	if m.keys.Unlocked() {
		m.signal()
	}

	m.keys.Lock()
	m.installed = [sha256.Size]byte{}
	m.set(EncryptionStatus{
		Initialized: st.Initialized(), Locked: true, Fingerprint: fingerprint, ActiveTerm: st.ActiveTerm,
		Detail: err.Error(),
	}, err)

	if !wasLocked {
		m.n.reestablish(true)
	}
}

func (m *keyManager) set(s EncryptionStatus, err error) {
	m.mu.Lock()
	previous := m.status
	m.status = s
	m.mu.Unlock()

	m.n.metrics.setEncryption(s)

	if err != nil && (!previous.Locked || previous.Detail != s.Detail) {
		m.n.cfg.Logger.Error("encryption keys locked: this node cannot read or write secrets until it unlocks",
			slog.Any("error", err))
	}
}

func (m *keyManager) setDetail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.status.Detail = err.Error()
}

func (m *keyManager) Status() EncryptionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.status
}

func (m *keyManager) sources() []KeySource {
	sources := []KeySource{m.cfg.Key}
	if m.cfg.Previous != nil {
		sources = append(sources, *m.cfg.Previous)
	}

	return sources
}

func (m *keyManager) clusterKey(st db.EncryptionKeys) (encryption.ClusterKey, error) {
	k, _, err := m.matchingKey(st)
	return k, err
}

func (m *keyManager) matchingKey(st db.EncryptionKeys) (encryption.ClusterKey, int, error) {
	var (
		loaded []string
		errs   []error
	)

	for i, source := range m.sources() {
		k, err := source.load()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", source, err))
			continue
		}

		if hasWrap(st, k) {
			return k, i, nil
		}

		loaded = append(loaded, k.Fingerprint())
		k.Zeroize()
	}

	switch {
	case len(errs) > 0:
		return nil, 0, errors.Join(errs...)
	case len(loaded) == 0:
		return nil, 0, fmt.Errorf("%w at %s", errNoKeyFile, m.cfg.Key)
	}

	return nil, 0, fmt.Errorf("%w: the key %v matches none of the cluster's keys %v", encryption.ErrNoMatchingWrap,
		loaded, fingerprints(st))
}

func (m *keyManager) installKeyFile(ctx context.Context, path string, contents []byte, message string) (
	*encryption.FileKey, error,
) {
	k, err := encryption.CreateFileKey(path, contents)
	if err != nil {
		return nil, err
	}

	m.n.cfg.Logger.InfoContext(ctx, message, slog.String("path", path), slog.String("key", k.Fingerprint()))

	return k, nil
}

func (m *keyManager) keyFileFor(ctx context.Context, nodeID string) ([]byte, error) {
	if m.cfg.Key.Type != KeyTypeFile {
		return nil, fmt.Errorf("%w: the cluster's key is a %s key, which is never sent: configure the node with it",
			ErrInvalidJoin, m.cfg.Key.Type)
	}

	cannot := fmt.Errorf("%w: this node cannot send the encryption key file", errRetry)

	if !m.keys.Unlocked() {
		return nil, cannot
	}

	contents, k, err := encryption.ReadFileKey(m.cfg.Key.Path)
	if err != nil {
		return nil, cannot
	}

	fingerprint := k.Fingerprint()
	k.Zeroize()

	if fingerprint != m.Status().Fingerprint {
		clear(contents)
		return nil, cannot
	}

	m.n.cfg.Logger.InfoContext(ctx, "audit event", slog.String("action", "send_encryption_key"),
		slog.String("actor", "node:"+m.n.id), slog.String("details", "Encryption key file sent to node "+nodeID))

	return contents, nil
}

func (m *keyManager) bootstrap(ctx context.Context, via db.Proposer) error {
	if _, err := m.n.db.ClusterID(ctx); !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	r, _ := m.n.current()
	if r == nil {
		return raft.ErrRaftShutdown
	}

	future := r.GetConfiguration()
	if err := future.Error(); err != nil {
		return err
	}

	if servers := future.Configuration().Servers; len(servers) != 1 || string(servers[0].ID) != m.n.id {
		return fmt.Errorf("the cluster has %d servers and no secrets: only the node that formed it creates them",
			len(servers))
	}

	for target := m.n.db.Schema() + 1; target <= db.SchemaVersion(); target++ {
		if err := m.n.db.MigrateVia(ctx, via, target); err != nil {
			return err
		}
	}

	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	ck, err := m.cfg.Key.load()
	if errors.Is(err, fs.ErrNotExist) && m.cfg.Key.Type == KeyTypeFile {
		ck, err = encryption.GenerateFileKey(m.cfg.Key.Path)
	}

	if err != nil {
		return fmt.Errorf("the cluster key: %w", err)
	}

	defer ck.Zeroize()

	sealed, keyring, err := encryption.Initialize(ck)
	if err != nil {
		return err
	}

	m.keys.Install(keyring)

	if _, err := m.n.db.CreateClusterVia(ctx, via, sealed); err != nil {
		m.keys.Lock()
		return err
	}

	m.installed = [sha256.Size]byte{}

	if err := m.unlockLocked(ctx, false); err != nil {
		return err
	}

	m.n.cfg.Logger.InfoContext(ctx, "audit event", slog.String("action", "initialize_encryption"),
		slog.String("actor", "node:"+m.n.id), slog.String("details", "Encryption initialized with key "+
			ck.Fingerprint()))

	return nil
}

func (m *keyManager) leadable(ctx context.Context) error {
	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		return err
	}

	if !st.Initialized() || m.keys.Unlocked() {
		return nil
	}

	if err := m.unlock(ctx, false); err != nil {
		return fmt.Errorf("%w: %w", ErrEncryptionLocked, err)
	}

	return nil
}

func (n *Node) reestablish(established bool) {
	r, _ := n.current()
	if r == nil || r.State() != raft.Leader {
		return
	}

	if leading, _ := n.Leader(); leading != established {
		return
	}

	select {
	case n.notify <- true:
	default:
	}
}

func (n *Node) Keys() *encryption.Keys {
	return &n.keyManager.keys
}

func (n *Node) KeysChanged() <-chan struct{} {
	return n.keyManager.changed
}

func (n *Node) Encryption() EncryptionStatus {
	return n.keyManager.Status()
}

func (n *Node) checkEncryption() Check {
	s := n.keyManager.Status()
	if s.Locked {
		return fail(CheckEncryption, "the encryption keys are locked: "+s.Detail)
	}

	return pass(CheckEncryption)
}

func hasWrap(st db.EncryptionKeys, ck encryption.ClusterKey) bool {
	return slices.ContainsFunc(st.Wraps, func(w encryption.RootWrap) bool {
		return w.Fingerprint == ck.Fingerprint() && w.Type == ck.Type() && w.Mechanism == ck.Mechanism()
	})
}

func fingerprints(st db.EncryptionKeys) []string {
	out := make([]string, 0, len(st.Wraps))
	for _, w := range st.Wraps {
		out = append(out, w.Fingerprint)
	}

	return out
}

type joinKey struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Mechanism   string `json:"mechanism"`
	Discardable bool   `json:"discardable,omitempty"`
}

func (k *joinKey) matches(w encryption.RootWrap) bool {
	return k.Fingerprint == w.Fingerprint && k.Type == w.Type && k.Mechanism == w.Mechanism
}

func (m *keyManager) joiningKey(ctx context.Context) (*joinKey, error) {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	return m.classifyKey(ctx)
}

func (m *keyManager) classifyKey(ctx context.Context) (*joinKey, error) {
	k, err := m.cfg.Key.load()
	if errors.Is(err, fs.ErrNotExist) && m.cfg.Key.Type == KeyTypeFile {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	defer k.Zeroize()

	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		return nil, err
	}

	return &joinKey{
		Fingerprint: k.Fingerprint(), Type: k.Type(), Mechanism: k.Mechanism(),
		Discardable: k.Type() == KeyTypeFile && st.Initialized() && hasWrap(st, k),
	}, nil
}

func (m *keyManager) admitKey(ctx context.Context, key *joinKey) (bool, error) {
	st, err := m.n.db.EncryptionKeys(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errRetry, err)
	}

	if !st.Initialized() {
		return false, fmt.Errorf("%w: the cluster is being created", errRetry)
	}

	if key == nil {
		return false, nil
	}

	matches := slices.ContainsFunc(st.Wraps, key.matches)

	if !matches && !key.Discardable {
		return false, fmt.Errorf("%w: the node's encryption key file %s is not the cluster's %v: delete it so that the "+
			"node receives the cluster's key, or install the cluster's key", ErrInvalidJoin, key.Fingerprint, fingerprints(st))
	}

	return matches, nil
}

func (m *keyManager) takeClusterKey(ctx context.Context, offered *joinKey, reply joinReply) error {
	m.unlockMu.Lock()
	defer m.unlockMu.Unlock()

	defer clear(reply.KeyFile)

	key, err := m.classifyKey(ctx)
	if err != nil {
		return err
	}

	clusterKey := ""

	if len(reply.KeyFile) > 0 {
		k, err := encryption.ParseFileKey(reply.KeyFile)
		if err != nil {
			return err
		}

		clusterKey = k.Fingerprint()
		k.Zeroize()
	}

	switch {
	case key == nil:
	case key.Fingerprint == clusterKey, reply.KeyMatches && offered != nil && offered.Fingerprint == key.Fingerprint:
		return nil
	case !key.Discardable:
		return fmt.Errorf("the encryption key file %s is not the cluster's, and it does not belong to this node's "+
			"replaced data: delete it so that the node receives the cluster's key", key.Fingerprint)
	}

	if key != nil {
		m.keys.Lock()

		if err := os.Remove(m.cfg.Key.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove this node's own encryption key file before taking the cluster's: %w", err)
		}

		m.n.cfg.Logger.InfoContext(ctx, "removed this node's own encryption key file: its data is replaced by the "+
			"cluster's", slog.String("path", m.cfg.Key.Path), slog.String("key", key.Fingerprint))
	}

	if len(reply.KeyFile) == 0 {
		return nil
	}

	k, err := m.installKeyFile(ctx, m.cfg.Key.Path, reply.KeyFile, "received the encryption key file with the join")
	if err != nil {
		return err
	}

	k.Zeroize()

	return nil
}
