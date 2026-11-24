package cluster

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
)

func waitTerm(t testing.TB, e *env, term uint32) {
	t.Helper()

	eventually(t, "the active term on "+e.node.ID(), func() error {
		s := e.node.Encryption()
		if s.Locked || s.ActiveTerm != term {
			return errors.New("not yet")
		}

		got, err := e.node.Keys().ActiveTerm()
		if err != nil || got != term {
			return errors.New("not installed yet")
		}

		return nil
	})
}

func tpduOf(t *testing.T, e *env, id int64) []byte {
	t.Helper()

	m, err := e.db.GetMessage(timeout(t), id)
	if err != nil {
		t.Fatal(err)
	}

	return m.TPDU
}

func TestRotationKeepsOlderValuesReadable(t *testing.T) {
	dir := t.TempDir()
	e := open(t, dir)

	waitLeader(t, e.node)
	waitUnlocked(t, e)

	older, err := e.db.CreateMessage(timeout(t), message(1))
	if err != nil {
		t.Fatal(err)
	}

	secrets, err := e.db.Secrets(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	s, err := e.node.RotateKeys(timeout(t))
	if err != nil || s.ActiveTerm != 2 {
		t.Fatalf("rotate = %+v, %v", s, err)
	}

	newer, err := e.db.CreateMessage(timeout(t), message(2))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.node.RotateKeys(timeout(t)); err != nil {
		t.Fatal(err)
	}

	st, err := e.db.EncryptionKeys(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := e.db.RotateKeys(timeout(t), 1, encryption.Sealed{Wrap: st.Wraps[0], Keyring: st.Keyring}); !errors.Is(err,
		db.ErrKeysChanged) {
		t.Fatalf("a rotation from a stale term = %v, want %v", err, db.ErrKeysChanged)
	}

	e = reopen(t, e, "")
	defer e.close(t)

	waitLeader(t, e.node)
	waitTerm(t, e, 3)

	if got := tpduOf(t, e, older); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("message from term 1 = %x", got)
	}

	if got := tpduOf(t, e, newer); !bytes.Equal(got, []byte{2}) {
		t.Fatalf("message from term 2 = %x", got)
	}

	if after, err := e.db.Secrets(timeout(t)); err != nil || !reflect.DeepEqual(after, secrets) {
		t.Fatalf("the secrets after the rotations: %v", err)
	}
}

func TestRotationReachesEveryMember(t *testing.T) {
	c := newCluster(t, 3)
	leader := c.leader()

	for _, e := range c.nodes {
		waitUnlocked(t, e)
	}

	if _, err := leader.node.RotateKeys(timeout(t)); err != nil {
		t.Fatal(err)
	}

	for _, e := range c.nodes {
		waitTerm(t, e, 2)
	}

	eventually(t, "every member unlocked", func() error {
		st, err := leader.node.Status(timeout(t))
		if err != nil {
			return err
		}

		for _, m := range st.Members {
			if m.Locked {
				return errors.New(m.NodeID + " is locked")
			}
		}

		return nil
	})

	for _, e := range c.nodes {
		if e == leader {
			continue
		}

		var le *LeaderError
		if _, err := e.node.RotateKeys(timeout(t)); !errors.As(err, &le) {
			t.Fatalf("rotation on a follower = %v, want a leader error", err)
		}
	}
}

func TestRolledBackKeysLockTheNode(t *testing.T) {
	dir := t.TempDir()
	e := open(t, dir)

	waitLeader(t, e.node)
	waitUnlocked(t, e)

	old, err := e.db.EncryptionKeys(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.node.RotateKeys(timeout(t)); err != nil {
		t.Fatal(err)
	}

	if err := errors.Join(e.node.Close(), e.db.Close()); err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE encryption_state SET active_term = ? WHERE id = 1`, []any{old.ActiveTerm}},
		{`UPDATE encryption_keyring SET sealed = ? WHERE id = 1`, []any{old.Keyring}},
		{`UPDATE encryption_root_wraps SET wrapped_root = ?`, []any{old.Wraps[0].Wrapped}},
	} {
		if _, err := conn.ExecContext(context.Background(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	_ = conn.Close()

	e = open(t, dir)
	defer e.close(t)

	eventually(t, "a locked node", func() error {
		if s := e.node.Encryption(); !s.Locked || !strings.Contains(s.Detail, ErrKeysRolledBack.Error()) {
			return errors.New("not locked: " + s.Detail)
		}

		return nil
	})

	if e.node.Keys().Unlocked() {
		t.Fatal("the node installed rolled back keys")
	}

	st, err := e.node.Status(timeout(t))
	if err != nil || len(st.Members) != 1 || !st.Members[0].Locked {
		t.Fatalf("members of a locked node = %+v, %v", st.Members, err)
	}
}

func TestRestoreAdoptsTheKeysOfAnotherClustersBackup(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	source := open(t, sourceDir)

	defer source.close(t)

	waitLeader(t, source.node)
	waitUnlocked(t, source)

	if _, err := source.node.RotateKeys(timeout(t)); err != nil {
		t.Fatal(err)
	}

	id, err := source.db.CreateMessage(timeout(t), message(7))
	if err != nil {
		t.Fatal(err)
	}

	var backup bytes.Buffer
	if err := source.node.Backup(ctx, &backup); err != nil {
		t.Fatal(err)
	}

	targetDir := t.TempDir()
	previous := &KeySource{Type: KeyTypeFile, Path: filepath.Join(sourceDir, defaultKeyFile)}
	target := openWith(t, targetDir, "", func(cfg *Config) { cfg.Encryption.Previous = previous })

	waitLeader(t, target.node)
	own := waitUnlocked(t, target).Fingerprint

	secrets, err := target.db.Secrets(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := target.node.Restore(ctx, bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}

	waitLeader(t, target.node)
	waitTerm(t, target, 2)

	if got := tpduOf(t, target, id); !bytes.Equal(got, []byte{7}) {
		t.Fatalf("the restored message = %x", got)
	}

	if after, err := target.db.Secrets(timeout(t)); err != nil || !reflect.DeepEqual(after, secrets) {
		t.Fatalf("the cluster's secrets after the restore: %v", err)
	}

	st, err := target.db.EncryptionKeys(timeout(t))
	if err != nil || len(st.Wraps) != 1 || st.Wraps[0].Fingerprint != own {
		t.Fatalf("keys after the restore = %v, %v; want only %s", fingerprints(st), err, own)
	}

	_ = target.node.Close()
	_ = target.db.Close()

	target = open(t, targetDir)
	defer target.close(t)

	waitLeader(t, target.node)
	waitTerm(t, target, 2)

	if got := tpduOf(t, target, id); !bytes.Equal(got, []byte{7}) {
		t.Fatalf("the restored message without the previous key = %x", got)
	}
}

func TestKeyChangeFromAFileToAnHSM(t *testing.T) {
	hsm := hsmEncryption(t, "change")
	nodes := make([]*env, 2)

	t.Cleanup(func() {
		for _, e := range nodes {
			_ = e.node.Close()
			_ = e.db.Close()
		}
	})

	for i := range nodes {
		nodes[i] = openClustered(t, t.TempDir(), freeAddress(t))
		waitLeader(t, nodes[i].node)
		waitUnlocked(t, nodes[i])
	}

	leader, joiner := nodes[0], nodes[1]

	if err := join(t, leader, joiner); err != nil {
		t.Fatal(err)
	}

	waitUnlocked(t, joiner)

	for i, e := range nodes {
		address := e.node.cfg.Address
		dir := e.dir

		_ = e.node.Close()
		_ = e.db.Close()

		nodes[i] = openWith(t, dir, address, func(cfg *Config) {
			cfg.Encryption = EncryptionConfig{Key: hsm.Key, Previous: &KeySource{
				Type: KeyTypeFile, Path: filepath.Join(dir, defaultKeyFile),
			}}
		})
		if i == 0 {
			eventually(t, "a key change in progress", func() error {
				st, err := nodes[0].db.EncryptionKeys(timeout(t))
				if err != nil || len(st.Wraps) < 2 {
					return errors.New("no key change")
				}

				return nil
			})
		}
	}

	var want string

	eventually(t, "the HSM key alone", func() error {
		st, err := nodes[0].db.EncryptionKeys(timeout(t))
		if err != nil {
			return err
		}

		if len(st.Wraps) != 1 || st.Wraps[0].Type != KeyTypePKCS11 {
			return errors.New("still changing")
		}

		want = st.Wraps[0].Fingerprint

		members, err := nodes[0].db.Members(timeout(t))
		if err != nil {
			return err
		}

		for _, m := range members {
			if m.EncryptionKey != want {
				return errors.New("a member is not on the HSM key yet")
			}
		}

		return nil
	})

	for _, e := range nodes {
		if s := waitUnlocked(t, e); s.Fingerprint != want {
			t.Fatalf("%s unlocked with %s, want %s", e.node.ID(), s.Fingerprint, want)
		}
	}

	if _, err := nodes[0].node.RotateKeys(timeout(t)); err != nil {
		var le *LeaderError
		if !errors.As(err, &le) {
			t.Fatal(err)
		}

		if _, err := nodes[1].node.RotateKeys(timeout(t)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKeyChangeOnAStandaloneNode(t *testing.T) {
	dir := t.TempDir()
	e := open(t, dir)

	waitLeader(t, e.node)
	waitUnlocked(t, e)

	id, err := e.db.CreateMessage(timeout(t), message(3))
	if err != nil {
		t.Fatal(err)
	}

	if err := errors.Join(e.node.Close(), e.db.Close()); err != nil {
		t.Fatal(err)
	}

	next := filepath.Join(dir, "encryption-new.key")

	k, err := encryption.GenerateFileKey(next)
	if err != nil {
		t.Fatal(err)
	}

	want := k.Fingerprint()
	k.Zeroize()

	e = openWith(t, dir, "", func(cfg *Config) {
		cfg.Encryption = EncryptionConfig{
			Key:      KeySource{Type: KeyTypeFile, Path: next},
			Previous: &KeySource{Type: KeyTypeFile, Path: filepath.Join(dir, defaultKeyFile)},
		}
	})
	defer e.close(t)

	waitLeader(t, e.node)

	eventually(t, "the previous key retired", func() error {
		st, err := e.db.EncryptionKeys(timeout(t))
		if err != nil {
			return err
		}

		if len(st.Wraps) != 1 || st.Wraps[0].Fingerprint != want {
			return errors.New("still changing")
		}

		return nil
	})

	if got := tpduOf(t, e, id); !bytes.Equal(got, []byte{3}) {
		t.Fatalf("message after the key change = %x", got)
	}
}
