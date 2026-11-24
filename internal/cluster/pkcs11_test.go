package cluster

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/ellanetworks/smsc/internal/encryption/softhsmtest"
)

func TestMain(m *testing.M) {
	code := m.Run()

	softhsmtest.Remove()
	os.Exit(code)
}

func hsmEncryption(t *testing.T, label string) EncryptionConfig {
	t.Helper()

	tok := softhsmtest.Open(t)
	tok.AES(t, label, encryption.KeySize)

	return EncryptionConfig{Key: KeySource{Type: KeyTypePKCS11, PKCS11: &encryption.PKCS11Config{
		Library: tok.Library, TokenLabel: tok.Label, PINFile: tok.PINFile, KeyLabel: label,
		Mechanism: encryption.MechanismAESGCM,
	}}}
}

func openHSM(t *testing.T, enc EncryptionConfig) *env {
	t.Helper()

	e := openWith(t, t.TempDir(), freeAddress(t), func(cfg *Config) { cfg.Encryption = enc })
	t.Cleanup(func() {
		_ = e.node.Close()
		_ = e.db.Close()
	})

	waitLeader(t, e.node)
	waitUnlocked(t, e)

	return e
}

func TestAClusterOnAnHSMKey(t *testing.T) {
	enc := hsmEncryption(t, "cluster")
	leader := openHSM(t, enc)

	s := leader.node.Encryption()
	if !strings.HasPrefix(s.Fingerprint, "cluster/") {
		t.Fatalf("fingerprint = %q", s.Fingerprint)
	}

	if _, err := os.Stat(filepath.Join(leader.dir, defaultKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a node on an HSM key wrote a key file: %v", err)
	}

	joiner := openHSM(t, enc)

	if err := join(t, leader, joiner); err != nil {
		t.Fatal(err)
	}

	if got := waitUnlocked(t, joiner); got.Fingerprint != s.Fingerprint {
		t.Fatalf("the joiner unlocked with %s, want %s", got.Fingerprint, s.Fingerprint)
	}
}

func TestJoinRefusedWithoutTheClustersHSMKey(t *testing.T) {
	leader := openHSM(t, hsmEncryption(t, "cluster"))

	joiners := map[string]EncryptionConfig{
		"another HSM key": hsmEncryption(t, "foreign"),
		"a key file":      {},
	}

	for name, enc := range joiners {
		t.Run(name, func(t *testing.T) {
			joiner := openHSM(t, enc)

			if err := join(t, leader, joiner); !errors.Is(err, ErrInvalidJoin) &&
				(err == nil || !strings.Contains(err.Error(), "key")) {
				t.Fatalf("join = %v", err)
			}

			if _, err := leader.db.Member(timeout(t), joiner.node.ID()); err == nil {
				t.Fatal("the leader admitted the node")
			}
		})
	}
}
