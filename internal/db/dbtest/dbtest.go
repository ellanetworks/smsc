package dbtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
)

const keyFile = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func Open(t testing.TB) *db.DB {
	t.Helper()

	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	CreateCluster(t, d)

	return d
}

func CreateCluster(t testing.TB, d *db.DB) {
	t.Helper()

	k, err := encryption.ParseFileKey([]byte(keyFile))
	if err != nil {
		t.Fatal(err)
	}

	sealed, keyring, err := encryption.Initialize(k)
	if err != nil {
		t.Fatal(err)
	}

	keys := &encryption.Keys{}
	keys.Install(keyring)
	d.SetKeys(keys)

	if _, err := d.CreateCluster(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
}

func WriteKeyFile(t testing.TB, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(keyFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func Unlock(t testing.TB, d *db.DB, keyPath string, within time.Duration) {
	t.Helper()

	ctx := context.Background()
	deadline := time.Now().Add(within)

	for {
		st, err := d.EncryptionKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}

		k, kerr := encryption.LoadFileKey(keyPath)
		if st.Initialized() && kerr == nil {
			keyring, err := encryption.Unlock(k, st.Wraps, st.Keyring)
			k.Zeroize()

			if err != nil {
				t.Fatal(err)
			}

			keys := &encryption.Keys{}
			keys.Install(keyring)
			d.SetKeys(keys)

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the cluster's keys are not available: initialized %t, key file: %v", st.Initialized(), kerr)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
