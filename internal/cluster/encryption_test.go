package cluster

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/hashicorp/raft"
)

var secretLocation = encryption.Location{Table: "jwt_secret", Column: "secret"}

func encryptionKeyFile(e *env) string {
	return filepath.Join(e.dir, defaultKeyFile)
}

func waitUnlocked(t testing.TB, e *env) EncryptionStatus {
	t.Helper()

	var s EncryptionStatus

	eventually(t, "unlocked encryption keys on "+e.node.ID(), func() error {
		s = e.node.Encryption()
		if !s.Initialized || s.Locked || !e.node.Keys().Unlocked() {
			return errors.New("locked: " + s.Detail)
		}

		return nil
	})

	return s
}

func reopen(t testing.TB, e *env, address string) *env {
	t.Helper()

	_ = e.node.Close()
	_ = e.db.Close()

	if address == "" {
		return open(t, e.dir)
	}

	return openClustered(t, e.dir, address)
}

func TestANewClusterIsEncryptedFromItsFirstCommit(t *testing.T) {
	e := open(t, t.TempDir())
	waitLeader(t, e.node)

	s := waitUnlocked(t, e)
	if !s.Initialized || s.ActiveTerm != 1 || s.Fingerprint == "" {
		t.Fatalf("status = %+v", s)
	}

	select {
	case <-e.node.KeysChanged():
	case <-time.After(time.Second):
		t.Fatal("the unlock was not signalled")
	}

	k, err := encryption.LoadFileKey(encryptionKeyFile(e))
	if err != nil || k.Fingerprint() != s.Fingerprint {
		t.Fatalf("key file = %v, %v; want fingerprint %s", k, err, s.Fingerprint)
	}

	info, err := os.Stat(encryptionKeyFile(e))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, %v", info, err)
	}

	value, err := e.node.Keys().Encrypt([]byte("secret"), secretLocation)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.db.Secrets(context.Background()); err != nil {
		t.Fatalf("the secrets: %v", err)
	}

	if check := e.node.checkEncryption(); !check.OK {
		t.Fatalf("readiness = %+v", check)
	}

	e = reopen(t, e, "")
	waitLeader(t, e.node)

	if again := waitUnlocked(t, e); again.Fingerprint != s.Fingerprint {
		t.Fatalf("after a restart the key is %s, want %s", again.Fingerprint, s.Fingerprint)
	}

	if got, err := e.node.Keys().Decrypt(value, secretLocation); err != nil || string(got) != "secret" {
		t.Fatalf("decrypt after a restart = %q, %v", got, err)
	}
}

func TestWrongKeyFileLocksTheNode(t *testing.T) {
	e := open(t, t.TempDir())
	waitLeader(t, e.node)

	s := waitUnlocked(t, e)
	path := encryptionKeyFile(e)

	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_ = e.node.Close()
	_ = e.db.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	wrong, err := encryption.GenerateFileKey(path)
	if err != nil {
		t.Fatal(err)
	}

	e = open(t, e.dir)

	eventually(t, "a locked node", func() error {
		if st := e.node.Encryption(); !st.Locked {
			return errors.New("not locked")
		}

		return nil
	})

	if ok, _ := e.node.Leader(); ok {
		t.Fatal("a locked node leads")
	}

	if check := e.node.checkEncryption(); check.OK {
		t.Fatal("a locked node passes the encryption readiness check")
	}

	if _, err := e.node.Keys().Encrypt([]byte("x"), secretLocation); !errors.Is(err, encryption.ErrLocked) {
		t.Fatalf("encrypt while locked: %v", err)
	}

	if k, err := encryption.LoadFileKey(path); err != nil || k.Fingerprint() != wrong.Fingerprint() {
		t.Fatalf("the wrong key file was replaced: %v, %v", k, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	e.node.keyManager.poke()

	if again := waitUnlocked(t, e); again.Fingerprint != s.Fingerprint {
		t.Fatalf("unlocked with %s, want %s", again.Fingerprint, s.Fingerprint)
	}

	waitLeader(t, e.node)
}

func keyManagerAt(t *testing.T, dir string) *keyManager {
	t.Helper()

	n := &Node{cfg: Config{Dir: dir, Logger: slog.New(slog.DiscardHandler)}, metrics: newMetrics(), formation: &formation{}}

	return newKeyManager(n, EncryptionConfig{Key: KeySource{Type: KeyTypeFile, Path: filepath.Join(dir, defaultKeyFile)}})
}

func initialized(t *testing.T, ck encryption.ClusterKey) db.EncryptionKeys {
	t.Helper()

	sealed, keyring, err := encryption.Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	keyring.Zeroize()

	return db.EncryptionKeys{ActiveTerm: 1, Wraps: []encryption.RootWrap{sealed.Wrap}, Keyring: sealed.Keyring}
}

func TestPreviousKeyUnlocks(t *testing.T) {
	dir := t.TempDir()
	m := keyManagerAt(t, dir)

	previous, err := encryption.GenerateFileKey(filepath.Join(dir, "previous.key"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := encryption.GenerateFileKey(m.cfg.Key.Path); err != nil {
		t.Fatal(err)
	}

	m.cfg.Previous = &KeySource{Type: KeyTypeFile, Path: filepath.Join(dir, "previous.key")}

	ck, err := m.clusterKey(initialized(t, previous))
	if err != nil || ck.Fingerprint() != previous.Fingerprint() {
		t.Fatalf("cluster key = %v, %v; want the previous key", ck, err)
	}

	m.cfg.Previous = nil

	if _, err := m.clusterKey(initialized(t, previous)); !errors.Is(err, encryption.ErrNoMatchingWrap) {
		t.Fatalf("cluster key without the previous key: %v", err)
	}
}

func TestMembersReceiveTheKeyFile(t *testing.T) {
	c := newCluster(t, 3)

	want := waitUnlocked(t, c.leader())

	contents, err := os.ReadFile(encryptionKeyFile(c.leader()))
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		if s := waitUnlocked(t, e); s.Fingerprint != want.Fingerprint {
			t.Fatalf("%s unlocked with %s, want %s", e.node.ID(), s.Fingerprint, want.Fingerprint)
		}

		got, err := os.ReadFile(encryptionKeyFile(e))
		if err != nil || !bytes.Equal(got, contents) {
			t.Fatalf("%s key file differs: %v", e.node.ID(), err)
		}
	}
}

func TestLockedFollowerHandsBackTheLeadership(t *testing.T) {
	c := newCluster(t, 3)

	for _, e := range c.nodes {
		waitUnlocked(t, e)
	}

	follower := c.follower()
	i := 0

	for j, e := range c.nodes {
		if e == follower {
			i = j
		}
	}

	_ = follower.node.Close()
	_ = follower.db.Close()

	if err := os.Remove(encryptionKeyFile(follower)); err != nil {
		t.Fatal(err)
	}

	if _, err := encryption.GenerateFileKey(encryptionKeyFile(follower)); err != nil {
		t.Fatal(err)
	}

	follower = openClustered(t, follower.dir, c.addrs[i])
	c.nodes[i] = follower

	eventually(t, "a locked follower", func() error {
		if !follower.node.Encryption().Locked {
			return errors.New("not locked")
		}

		return nil
	})

	eventually(t, "a leadership transfer to the locked node", func() error {
		r, _ := c.leader().node.current()

		return r.LeadershipTransferToServer(raft.ServerID(follower.node.ID()), raft.ServerAddress(c.addrs[i])).Error()
	})

	eventually(t, "the leadership back on an unlocked node", func() error {
		l := c.leader()
		if l == follower {
			return errors.New("the locked node leads")
		}

		if !l.node.Encryption().Locked && l.node.Keys().Unlocked() {
			return nil
		}

		return errors.New("the leader is locked")
	})

	if ok, _ := follower.node.Leader(); ok {
		t.Fatal("the locked node leads")
	}
}

func standalone(t *testing.T, n int, before func(i int, dir string)) []*env {
	t.Helper()

	var nodes []*env

	for i := range n {
		dir := t.TempDir()
		if before != nil {
			before(i, dir)
		}

		e := openClustered(t, dir, freeAddress(t))
		t.Cleanup(func() {
			_ = e.node.Close()
			_ = e.db.Close()
		})

		waitLeader(t, e.node)
		waitUnlocked(t, e)

		nodes = append(nodes, e)
	}

	return nodes
}

func join(t *testing.T, leader, joiner *env) error {
	t.Helper()

	token, err := leader.node.MintToken(timeout(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	return joiner.node.Join(timeout(t), token.Token)
}

func TestJoinReplacesTheNodesOwnKey(t *testing.T) {
	nodes := standalone(t, 2, nil)
	leader, joiner := nodes[0], nodes[1]

	want, own := leader.node.Encryption().Fingerprint, joiner.node.Encryption().Fingerprint
	if want == own {
		t.Fatal("two standalone nodes generated the same key")
	}

	if err := join(t, leader, joiner); err != nil {
		t.Fatal(err)
	}

	if s := waitUnlocked(t, joiner); s.Fingerprint != want {
		t.Fatalf("the joiner unlocked with %s, want the cluster's %s", s.Fingerprint, want)
	}

	a, _ := os.ReadFile(encryptionKeyFile(leader))
	b, _ := os.ReadFile(encryptionKeyFile(joiner))

	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Fatal("the joiner's key file is not the cluster's")
	}
}

func TestJoinKeepsASharedKey(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared.key")
	if _, err := encryption.GenerateFileKey(shared); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}

	nodes := standalone(t, 2, func(_ int, dir string) {
		if err := os.WriteFile(filepath.Join(dir, defaultKeyFile), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	leader, joiner := nodes[0], nodes[1]

	if err := join(t, leader, joiner); err != nil {
		t.Fatal(err)
	}

	if s := waitUnlocked(t, joiner); s.Fingerprint != leader.node.Encryption().Fingerprint {
		t.Fatalf("the joiner unlocked with %s", s.Fingerprint)
	}

	if got, err := os.ReadFile(encryptionKeyFile(joiner)); err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("the shared key file changed: %v", err)
	}
}

func TestJoinRefusedWithAForeignKey(t *testing.T) {
	nodes := standalone(t, 2, nil)
	leader, joiner := nodes[0], nodes[1]

	path := encryptionKeyFile(joiner)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	foreign, err := encryption.GenerateFileKey(path)
	if err != nil {
		t.Fatal(err)
	}

	err = join(t, leader, joiner)
	if !errors.Is(err, ErrInvalidJoin) && (err == nil || !strings.Contains(err.Error(), "encryption key")) {
		t.Fatalf("join with a foreign key = %v", err)
	}

	if k, err := encryption.LoadFileKey(path); err != nil || k.Fingerprint() != foreign.Fingerprint() {
		t.Fatalf("the foreign key file changed: %v, %v", k, err)
	}

	if _, err := leader.db.Member(context.Background(), joiner.node.ID()); err == nil {
		t.Fatal("the leader admitted the node")
	}
}

func TestJoinsWaitForTheCluster(t *testing.T) {
	dir := t.TempDir()

	d, err := db.Open(context.Background(), filepath.Join(dir, "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	m := keyManagerAt(t, dir)
	m.n.db = d

	if _, err := m.admitKey(context.Background(), nil); !errors.Is(err, errRetry) {
		t.Fatalf("a join before the cluster exists = %v, want a retry", err)
	}
}
