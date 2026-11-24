package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func rotateInMemory(t *testing.T, d *DB) {
	t.Helper()

	_, next, _, err := d.keys.Rotate(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	d.keys.Install(next)
}

func rotate(t *testing.T, d *DB) {
	t.Helper()

	ctx := context.Background()

	st, err := d.EncryptionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sealed, next, upgrade, err := d.keys.Rotate(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	change, err := d.PrepareKeyChange(ctx, st, false, sealed, next, upgrade)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ChangeKeys(ctx, change); err != nil {
		t.Fatal(err)
	}

	d.keys.Install(next)
}

func TestAWriteUnderAnUnknownDataKeyIsRetried(t *testing.T) {
	d := openTestDB(t)
	rotateInMemory(t, d)

	_, err := d.CreateMessage(context.Background(), testMessage(1, false, []byte{1}, time.Now()))
	if !errors.Is(err, ErrStaleDataKey) || !errors.Is(err, ErrBusy) {
		t.Fatalf("a message under a term the cluster does not use = %v, want a busy %v", err, ErrStaleDataKey)
	}

	if total := count(t, d, "messages"); total != 0 {
		t.Fatalf("%d messages stored", total)
	}
}

func TestReencryptionMovesValuesToTheActiveTerm(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{7}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	st, err := d.EncryptionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sealed, next, upgrade, err := d.keys.Rotate(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	change, err := d.PrepareKeyChange(ctx, st, false, sealed, next, upgrade)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ChangeKeys(ctx, change); err != nil {
		t.Fatal(err)
	}

	d.keys.Install(next)

	if st, err := d.EncryptionKeys(ctx); err != nil || len(st.Upgrades) != 1 {
		t.Fatalf("upgrade entries after a rotation = %d, %v; want 1", len(st.Upgrades), err)
	}

	if err := d.ForgetUpgrades(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if st, err := d.EncryptionKeys(ctx); err != nil || len(st.Upgrades) != 0 {
		t.Fatalf("upgrade entries after the grace period = %d, %v; want none", len(st.Upgrades), err)
	}

	if err := d.ChangeKeys(ctx, change); !errors.Is(err, ErrKeysChanged) {
		t.Fatalf("the same change twice = %v, want %v", err, ErrKeysChanged)
	}

	if _, n, bad, err := d.ReencryptStale(ctx, ReencryptCursor{}, 10); err != nil || n != 1 || bad != 0 {
		t.Fatalf("ReencryptStale = %d, %d, %v", n, bad, err)
	}

	if _, n, _, err := d.ReencryptStale(ctx, ReencryptCursor{}, 10); err != nil || n != 0 {
		t.Fatalf("ReencryptStale after re-encryption = %d, %v; want 0", n, err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil || !bytes.Equal(m.TPDU, []byte{7}) {
		t.Fatalf("the re-encrypted message = %x, %v", m.TPDU, err)
	}

	st, err = d.EncryptionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}

	unused, err := d.UnusedTerms(ctx, st)
	if err != nil || len(unused) != 1 || unused[0] != 1 {
		t.Fatalf("unused terms = %v, %v; want [1]", unused, err)
	}
}

func TestReencryptionKeepsAValueChangedMeanwhile(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{7}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	var stored []byte
	if err := d.reader().QueryRowContext(ctx, `SELECT tpdu FROM messages WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}

	updated, err := submit[int](ctx, d, cmdReencrypt, reencrypt{
		Values: []ReencryptedValue{{Table: "messages", ID: id, Old: []byte("not the stored value"), New: stored}},
	})
	if err != nil || updated != 0 {
		t.Fatalf("re-encrypting a value that changed meanwhile updated %d rows, %v", updated, err)
	}
}

func TestAnUndecryptableMessageDoesNotStallReads(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Now()

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{7}, now))
	if err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite3", "file:"+d.path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, `UPDATE messages SET tpdu = substr(tpdu, 1, length(tpdu) - 1) || X'00'
		WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	listed, _, err := d.ListMessages(ctx, MessageFilter{}, 1, 10)
	if err != nil || len(listed) != 1 || listed[0].TPDU != nil {
		t.Fatalf("ListMessages = %+v, %v", listed, err)
	}

	m, ok, err := d.NextDue(ctx, testMSISDN, now.Add(time.Hour))
	if err != nil || !ok || m.ID != id || m.TPDU != nil {
		t.Fatalf("NextDue = %d %v %x, %v; want the message with its content left out", m.ID, ok, m.TPDU, err)
	}
}

func TestUndecryptableSecretsDoNotBlockRotation(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	o, err := d.GetOIDC(ctx)
	if err != nil {
		t.Fatal(err)
	}

	o.ClientSecret = "the client secret"

	if err := d.UpdateOIDC(ctx, o); err != nil {
		t.Fatal(err)
	}

	before, err := d.Secrets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite3", "file:"+d.path)
	if err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		`UPDATE jwt_secret SET secret = zeroblob(length(secret))`,
		`UPDATE oidc SET client_secret = zeroblob(length(client_secret))`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	_ = conn.Close()

	if got, err := d.GetOIDC(ctx); err != nil || got.ClientSecret != "" || got.ClientSecretLocked {
		t.Fatalf("GetOIDC with an undecryptable client secret = %q locked=%v, %v", got.ClientSecret,
			got.ClientSecretLocked, err)
	}

	st, err := d.EncryptionKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sealed, next, upgrade, err := d.keys.Rotate(testClusterKey(t))
	if err != nil {
		t.Fatal(err)
	}

	change, err := d.PrepareKeyChange(ctx, st, false, sealed, next, upgrade)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ChangeKeys(ctx, change); err != nil {
		t.Fatal(err)
	}

	d.keys.Install(next)

	after, err := d.Secrets(ctx)
	if err != nil || len(after.JWT) != jwtSecretLength || bytes.Equal(after.JWT, before.JWT) ||
		!bytes.Equal(after.OIDCCookieKey, before.OIDCCookieKey) {
		t.Fatalf("secrets after the rotation = %+v, %v; want a new JWT secret only", after, err)
	}

	if v := rawColumn(t, d, "oidc", "client_secret"); v != nil {
		t.Fatalf("the undecryptable client secret is still %x, want NULL", v)
	}
}

func TestReencryptionPagesPastUndecryptableRows(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	var ids []int64

	for i := range 3 {
		id, err := d.CreateMessage(ctx, testMessage(uint8(i), false, []byte{byte(i)}, time.Now()))
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	conn, err := sql.Open("sqlite3", "file:"+d.path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := conn.ExecContext(ctx, `UPDATE messages SET tpdu = substr(tpdu, 1, 6) || zeroblob(length(tpdu) - 6)
		WHERE id IN (?, ?)`, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	rotate(t, d)

	var (
		cursor ReencryptCursor
		bad    int
	)

	for {
		next, n, undecryptable, err := d.ReencryptStale(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}

		if n == 0 {
			break
		}

		bad += undecryptable
		cursor = next
	}

	if bad != 2 || cursor.Message != ids[2] {
		t.Fatalf("paged to message %d past %d undecryptable rows; want %d past 2", cursor.Message, bad, ids[2])
	}
}
