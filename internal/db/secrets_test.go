package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ellanetworks/smsc/internal/encryption"
)

const testKeyFile = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func testClusterKey(t testing.TB) *encryption.FileKey {
	t.Helper()

	k, err := encryption.ParseFileKey([]byte(testKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	return k
}

func newTestCluster(t testing.TB, d *DB) {
	t.Helper()

	sealed, keyring, err := encryption.Initialize(testClusterKey(t))
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

func testSealer(t testing.TB) *DB {
	t.Helper()

	_, keyring, err := encryption.Initialize(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	keys := &encryption.Keys{}
	keys.Install(keyring)

	return &DB{keys: keys}
}

func unlockFrom(t testing.TB, d *DB) {
	t.Helper()

	st, err := d.EncryptionKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	keyring, err := encryption.Unlock(testClusterKey(t), st.Wraps, st.Keyring)
	if err != nil {
		t.Fatal(err)
	}

	keys := &encryption.Keys{}
	keys.Install(keyring)
	d.SetKeys(keys)
}

func rawColumn(t *testing.T, d *DB, table, column string) []byte {
	t.Helper()

	var v []byte
	if err := d.writer.QueryRowContext(context.Background(), `SELECT `+column+` FROM `+table+` WHERE id = 1`).Scan(&v); err != nil {
		t.Fatal(err)
	}

	return v
}

func TestCreateCluster(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	st, err := d.EncryptionKeys(ctx)
	if err != nil || !st.Initialized() || st.ActiveTerm != 1 || len(st.KeyringID) != keyringIDLength ||
		len(st.Wraps) != 1 {
		t.Fatalf("encryption after creating the cluster = %+v, %v", st, err)
	}

	first, err := d.Secrets(ctx)
	if err != nil || len(first.JWT) != jwtSecretLength || len(first.OIDCCookieKey) != oidcCookieKeyLength ||
		len(first.JoinKey) != joinKeyLength || first.ClusterID == "" || bytes.Equal(first.JWT, first.OIDCCookieKey) {
		t.Fatalf("Secrets = %+v, %v", first, err)
	}

	for _, c := range [][3]string{{"jwt_secret", "secret", ""}, {"oidc_cookie_key", "key", ""}, {"cluster", "join_key", ""}} {
		if v := rawColumn(t, d, c[0], c[1]); requireSealed(v) != nil || bytes.Contains(v, first.JWT) {
			t.Fatalf("%s.%s is not encrypted", c[0], c[1])
		}
	}

	sealed, keyring, err := encryption.Initialize(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	defer keyring.Zeroize()

	if id, err := d.CreateCluster(ctx, sealed); err != nil || id != first.ClusterID {
		t.Fatalf("creating the cluster again = %q, %v; want %q", id, err, first.ClusterID)
	}

	if again, err := d.Secrets(ctx); err != nil || !bytes.Equal(again.JWT, first.JWT) {
		t.Fatalf("the secrets changed when the cluster was created again: %v", err)
	}

	if _, err := openBaselineDB(t).CreateCluster(ctx, sealed); !errors.Is(err, ErrKeysLocked) {
		t.Fatalf("creating a cluster without keys = %v", err)
	}
}

func TestBeforeTheClusterExists(t *testing.T) {
	ctx := context.Background()
	d := openBaselineDB(t)

	if st, err := d.EncryptionKeys(ctx); err != nil || st.Initialized() {
		t.Fatalf("EncryptionKeys = %+v, %v", st, err)
	}

	if _, err := d.Secrets(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Secrets = %v", err)
	}

	if _, err := d.ClusterID(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ClusterID = %v", err)
	}
}

func TestCreateClusterRefusesPlaintext(t *testing.T) {
	ctx := context.Background()
	d := openBaselineDB(t)

	sealed, keyring, err := encryption.Initialize(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	defer keyring.Zeroize()

	p := createCluster{
		secretRow: secretRow{
			JWT: bytes.Repeat([]byte{1}, 32), OIDCCookieKey: bytes.Repeat([]byte{2}, 32), ClusterID: "c",
			JoinKey: bytes.Repeat([]byte{3}, 32),
		},
		Keys: newClusterKeys(sealed, keyring.ActiveTerm()),
	}

	if _, err := submit[string](ctx, d, cmdCreateCluster, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("creating a cluster with plaintext secrets = %v", err)
	}

	if st, err := d.EncryptionKeys(ctx); err != nil || st.Initialized() {
		t.Fatalf("a refused cluster left its keys: %+v, %v", st, err)
	}
}

func TestSecretsSurviveReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "smsc.db")

	var secrets []Secrets

	for i := range 2 {
		d, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}

		if i == 0 {
			newTestCluster(t, d)
		} else {
			unlockFrom(t, d)
		}

		s, err := d.Secrets(ctx)
		if err != nil {
			t.Fatal(err)
		}

		secrets = append(secrets, s)

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if !reflect.DeepEqual(secrets[0], secrets[1]) {
		t.Fatal("the secrets changed when the database was reopened")
	}

	if s, err := openTestDB(t).Secrets(ctx); err != nil || bytes.Equal(s.JWT, secrets[0].JWT) ||
		bytes.Equal(s.OIDCCookieKey, secrets[0].OIDCCookieKey) || s.ClusterID == secrets[0].ClusterID {
		t.Fatalf("two clusters share a secret: %v", err)
	}
}

func TestOIDCClientSecretIsEncrypted(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	o, err := d.GetOIDC(ctx)
	if err != nil || o.ClientSecret != "" {
		t.Fatalf("GetOIDC = %+v, %v", o, err)
	}

	if v := rawColumn(t, d, "oidc", "client_secret"); v != nil {
		t.Fatalf("a new cluster's client secret is %x, want NULL", v)
	}

	o.ClientSecret = "the client secret"

	if err := d.UpdateOIDC(ctx, o); err != nil {
		t.Fatal(err)
	}

	if v := rawColumn(t, d, "oidc", "client_secret"); requireSealed(v) != nil || bytes.Contains(v, []byte(o.ClientSecret)) {
		t.Fatalf("the client secret is stored as %q", v)
	}

	if got, err := d.GetOIDC(ctx); err != nil || got.ClientSecret != o.ClientSecret {
		t.Fatalf("GetOIDC = %q, %v", got.ClientSecret, err)
	}

	o.ClientSecret = ""

	if err := d.UpdateOIDC(ctx, o); err != nil {
		t.Fatal(err)
	}

	if v := rawColumn(t, d, "oidc", "client_secret"); v != nil {
		t.Fatalf("an unset client secret is stored as %x, want NULL", v)
	}

	if _, err := submit[struct{}](ctx, d, cmdUpdateOIDC, updateOIDC{OIDC: OIDC{
		Scopes: []string{"openid"}, SessionLifetime: o.SessionLifetime, ClientSecret: "plaintext",
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plaintext client secret in the log = %v", err)
	}

	o.ClientSecret = "again"
	if err := d.UpdateOIDC(ctx, o); err != nil {
		t.Fatal(err)
	}

	d.keys.Lock()

	if locked, err := d.GetOIDC(ctx); err != nil || !locked.ClientSecretLocked || locked.ClientSecret != "" ||
		locked.ClientID != o.ClientID {
		t.Fatalf("GetOIDC while locked = %+v, %v", locked, err)
	}

	o.ClientSecret = "while locked"
	if err := d.UpdateOIDC(ctx, o); !errors.Is(err, encryption.ErrLocked) {
		t.Fatalf("UpdateOIDC while locked = %v", err)
	}
}

func TestLockedNodeKnowsItsClusterButNotItsSecrets(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	want, err := d.Secrets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	d.keys.Lock()

	if _, err := d.Secrets(ctx); !errors.Is(err, ErrKeysLocked) {
		t.Fatalf("Secrets while locked = %v", err)
	}

	if id, err := d.ClusterID(ctx); err != nil || id != want.ClusterID {
		t.Fatalf("ClusterID while locked = %q, %v", id, err)
	}
}

func TestSecretsThatAreNotTheClustersAreRefused(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	sealed := rawColumn(t, d, "jwt_secret", "secret")

	other := openTestDB(t)
	foreign := rawColumn(t, other, "jwt_secret", "secret")

	cookie := rawColumn(t, d, "oidc_cookie_key", "key")

	for name, v := range map[string][]byte{
		"plaintext":       bytes.Repeat([]byte{0x41}, jwtSecretLength),
		"truncated":       sealed[:len(sealed)-1],
		"another version": append([]byte{sealed[0], 0x02}, sealed[2:]...),
		"another column":  cookie,
		"another cluster": foreign,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := d.writer.ExecContext(ctx, `UPDATE jwt_secret SET secret = ?`, v); err != nil {
				t.Fatal(err)
			}

			if _, err := d.Secrets(ctx); err == nil {
				t.Fatal("the JWT secret was read")
			}
		})
	}
}

func TestOpenRefusesAPreReleaseDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smsc.db")

	ctx := context.Background()

	conn, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	if _, err := Open(ctx, path); !errors.Is(err, ErrPreRelease) {
		t.Fatalf("Open on a pre-release database = %v, want %v", err, ErrPreRelease)
	}
}
