package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
)

func backupNode(t *testing.T, store *db.DB) *cluster.Node {
	t.Helper()

	n, err := cluster.Open(context.Background(), cluster.Config{Dir: filepath.Join(t.TempDir(), "raft")}, store)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = n.Close() })

	store.SetProposer(n)

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok, _ := n.Leader(); ok {
			return n
		}
	}

	t.Fatal("no leader")

	return nil
}

func TestAPITokenBackupHoldsNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	node := backupNode(t, store)

	secrets, err := store.Secrets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, store)
	cfg.JWTSecret = func() []byte { return secrets.JWT }
	cfg.OIDCCookieKey = func() []byte { return secrets.OIDCCookieKey }
	cfg.Backups = node
	h := api.NewHandler(cfg)

	admin := initialize(t, h)

	rec := serve(h, request(http.MethodPost, "/api/v1/users/me/api-tokens", `{"name": "backup-cron"}`, admin))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create API token = %d %s", rec.Code, rec.Body)
	}

	var created struct {
		Result json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	token := decode[api.CreateAPITokenResponse](t, created.Result).Token

	rec = serve(h, request(http.MethodGet, "/api/v1/backup", "", token))
	if rec.Code != http.StatusOK {
		t.Fatalf("backup = %d %s", rec.Code, rec.Body)
	}

	raw := backedUpDatabase(t, rec.Body)

	for name, secret := range map[string][]byte{
		"JWT secret": secrets.JWT, "OIDC cookie key": secrets.OIDCCookieKey, "join key": secrets.JoinKey,
	} {
		if bytes.Contains(raw, secret) {
			t.Errorf("the backup holds the %s", name)
		}
	}

	path := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	var stolen []byte
	if err := conn.QueryRowContext(ctx, `SELECT secret FROM jwt_secret WHERE id = 1`).Scan(&stolen); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("JWT secret from the backup = %d bytes, %v; want none", len(stolen), err)
	}
}

func backedUpDatabase(t *testing.T, r io.Reader) []byte {
	t.Helper()

	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}

		if h.Name == "smsc.db" {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}

			return b
		}
	}
}
