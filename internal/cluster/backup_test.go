package cluster

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
)

func message(i int) db.NewMessage {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	return db.NewMessage{
		Originator: db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:  db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:     "15551230002", Origin: db.OriginMobile, MessageReference: uint8(i), TPDU: []byte{byte(i)},
		SubmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	e := open(t, t.TempDir())

	defer e.close(t)

	waitLeader(t, e.node)

	kept, err := e.db.CreateMessage(timeout(t), message(1))
	if err != nil {
		t.Fatal(err)
	}

	var backup bytes.Buffer
	if err := e.node.Backup(ctx, &backup); err != nil {
		t.Fatal(err)
	}

	lost, err := e.db.CreateMessage(timeout(t), message(2))
	if err != nil {
		t.Fatal(err)
	}

	var replaced bool

	e.db.OnEvents(func(ev db.Events) { replaced = replaced || ev.Replaced })

	if err := e.node.Restore(ctx, bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}

	if !replaced {
		t.Fatal("no state-replaced event")
	}

	if _, err := e.db.GetMessage(ctx, kept); err != nil {
		t.Fatalf("message from before the backup: %v", err)
	}

	if _, err := e.db.GetMessage(ctx, lost); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("message from after the backup = %v, want it gone", err)
	}

	waitLeader(t, e.node)

	next, err := e.db.CreateMessage(timeout(t), message(3))
	if err != nil {
		t.Fatal(err)
	}

	if next <= lost {
		t.Fatalf("new message ID %d after the restore, want more than %d: IDs went backwards", next, lost)
	}
}

func TestRestoreRejectsATamperedBackup(t *testing.T) {
	ctx := context.Background()
	e := open(t, t.TempDir())

	defer e.close(t)

	waitLeader(t, e.node)

	var backup bytes.Buffer
	if err := e.node.Backup(ctx, &backup); err != nil {
		t.Fatal(err)
	}

	for name, archive := range map[string][]byte{
		"not gzip":  []byte("hello"),
		"truncated": backup.Bytes()[:backup.Len()/2],
	} {
		if err := e.node.Restore(ctx, bytes.NewReader(archive)); !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("%s: err = %v, want an invalid backup", name, err)
		}
	}
}

func TestRestartAfterRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	e := open(t, dir)

	waitLeader(t, e.node)

	kept, err := e.db.CreateMessage(timeout(t), message(1))
	if err != nil {
		t.Fatal(err)
	}

	var backup bytes.Buffer
	if err := e.node.Backup(ctx, &backup); err != nil {
		t.Fatal(err)
	}

	if _, err := e.db.CreateMessage(timeout(t), message(2)); err != nil {
		t.Fatal(err)
	}

	if err := e.node.Restore(ctx, bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}

	waitLeader(t, e.node)

	after, err := e.db.CreateMessage(timeout(t), message(3))
	if err != nil {
		t.Fatal(err)
	}

	e.close(t)

	e = open(t, dir)
	defer e.close(t)

	waitLeader(t, e.node)

	for _, id := range []int64{kept, after} {
		if _, err := e.db.GetMessage(ctx, id); err != nil {
			t.Fatalf("message %d after the restart: %v", id, err)
		}
	}

	if _, total, err := e.db.ListMessages(ctx, db.MessageFilter{}, 1, 10); err != nil || total != 2 {
		t.Fatalf("%d messages after the restart, %v; want 2", total, err)
	}

	if _, err := e.db.CreateMessage(timeout(t), message(4)); err != nil {
		t.Fatal(err)
	}
}

func backedUpDatabase(t *testing.T, backup []byte) []byte {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(backup))
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}

		if h.Name == backupDatabase {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}

			return b
		}
	}
}

func TestBackupLeavesOutTheSecrets(t *testing.T) {
	ctx := context.Background()
	e := open(t, t.TempDir())

	defer e.close(t)

	waitLeader(t, e.node)

	secrets, err := e.db.Secrets(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := e.db.CreateJoinToken(timeout(t), "pending-join-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var backup bytes.Buffer
	if err := e.node.Backup(ctx, &backup); err != nil {
		t.Fatal(err)
	}

	raw := backedUpDatabase(t, backup.Bytes())

	for name, secret := range map[string][]byte{
		"JWT secret": secrets.JWT, "OIDC cookie key": secrets.OIDCCookieKey, "join key": secrets.JoinKey,
		"join token": []byte("pending-join-token"),
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

	for _, table := range []string{"jwt_secret", "oidc_cookie_key", "cluster", "cluster_join_tokens"} {
		var rows int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil || rows != 0 {
			t.Errorf("%s holds %d rows, %v; want none", table, rows, err)
		}
	}

	if err := e.node.Restore(ctx, bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}

	after, err := e.db.ReadSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(after, secrets) {
		t.Fatal("the restore changed the cluster's secrets")
	}
}

func TestRestoreIgnoresTheSecretsOfABackup(t *testing.T) {
	ctx := context.Background()
	source := open(t, t.TempDir())

	defer source.close(t)

	waitLeader(t, source.node)

	if _, err := source.db.Secrets(timeout(t)); err != nil {
		t.Fatal(err)
	}

	c, err := source.db.CopyTo(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var backup bytes.Buffer
	if err := source.node.archive(ctx, &backup, c); err != nil {
		t.Fatal(err)
	}

	target := open(t, t.TempDir())

	defer target.close(t)

	waitLeader(t, target.node)

	secrets, err := target.db.Secrets(timeout(t))
	if err != nil {
		t.Fatal(err)
	}

	if err := target.node.Restore(ctx, bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatal(err)
	}

	after, err := target.db.ReadSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(after, secrets) {
		t.Fatal("the restore took the backup's secrets")
	}
}
