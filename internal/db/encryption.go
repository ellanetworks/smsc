package db

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/ellanetworks/smsc/internal/encryption"
)

const keyringIDLength = 16

var (
	ErrKeysChanged = errors.New("the encryption keys changed meanwhile")
	ErrKeyChange   = errors.New("a cluster key change is in progress")
)

type EncryptionKeys struct {
	ActiveTerm uint32
	KeyringID  []byte
	Wraps      []encryption.RootWrap
	Keyring    []byte
}

func (k EncryptionKeys) Initialized() bool {
	return k.ActiveTerm != 0
}

func BackupEncryptionKeys(ctx context.Context, path string) (_ EncryptionKeys, err error) {
	conn, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return EncryptionKeys{}, err
	}

	defer func() { _ = conn.Close() }()

	return readEncryptionKeys(ctx, conn)
}

func (d *DB) AdoptKeys(ctx context.Context, path string, backup EncryptionKeys, wrap encryption.RootWrap,
	adopted *encryption.Keys,
) (err error) {
	defer d.observe(&err)()

	if !d.unlocked() {
		return ErrKeysLocked
	}

	if err := validWrap(wrap); err != nil {
		return err
	}

	conn, err := sql.Open("sqlite3", "file:"+path+"?_secure_delete=on")
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	for _, k := range secretKeys {
		if err := reseal(ctx, tx, k.at, d.keys, adopted); err != nil {
			return fmt.Errorf("re-encrypt the %s: %w", k.at.Table, err)
		}
	}

	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM encryption_root_wraps`, nil},
		{
			`INSERT INTO encryption_root_wraps (fingerprint, key_type, mechanism, wrapped_root) VALUES (?, ?, ?, ?)`,
			[]any{wrap.Fingerprint, wrap.Type, wrap.Mechanism, wrap.Wrapped},
		},
		{`UPDATE encryption_keyring SET sealed = ? WHERE id = 1`, []any{backup.Keyring}},
		{
			`UPDATE encryption_state SET active_term = ?, keyring_id = ? WHERE id = 1`,
			[]any{backup.ActiveTerm, backup.KeyringID},
		},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return fmt.Errorf("adopt the backup's keys: %w", err)
		}
	}

	return tx.Commit()
}

func reseal(ctx context.Context, tx *sql.Tx, at encryption.Location, from, to *encryption.Keys) error {
	var value []byte

	err := tx.QueryRowContext(ctx, `SELECT `+at.Column+` FROM `+at.Table+` WHERE id = 1`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	plain, err := from.Decrypt(value, at)
	if err != nil {
		return err
	}

	defer clear(plain)

	sealed, err := to.Encrypt(plain, at)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `UPDATE `+at.Table+` SET `+at.Column+` = ? WHERE id = 1`, sealed)

	return err
}

func (d *DB) EncryptionKeys(ctx context.Context) (_ EncryptionKeys, err error) {
	defer d.observe(&err)()

	return readEncryptionKeys(ctx, d.reader())
}

func readEncryptionKeys(ctx context.Context, q querier) (EncryptionKeys, error) {
	var k EncryptionKeys

	err := q.QueryRowContext(ctx, `SELECT active_term, keyring_id FROM encryption_state WHERE id = 1`).
		Scan(&k.ActiveTerm, &k.KeyringID)
	if errors.Is(err, sql.ErrNoRows) {
		return EncryptionKeys{}, nil
	}

	if err != nil {
		return EncryptionKeys{}, fmt.Errorf("read the encryption state: %w", err)
	}

	rows, err := q.QueryContext(ctx, `SELECT fingerprint, key_type, mechanism, wrapped_root FROM encryption_root_wraps
		ORDER BY fingerprint`)
	if err != nil {
		return EncryptionKeys{}, fmt.Errorf("read the root key wraps: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var w encryption.RootWrap
		if err := rows.Scan(&w.Fingerprint, &w.Type, &w.Mechanism, &w.Wrapped); err != nil {
			return EncryptionKeys{}, fmt.Errorf("read the root key wraps: %w", err)
		}

		k.Wraps = append(k.Wraps, w)
	}

	if err := rows.Err(); err != nil {
		return EncryptionKeys{}, fmt.Errorf("read the root key wraps: %w", err)
	}

	if err := q.QueryRowContext(ctx, `SELECT sealed FROM encryption_keyring WHERE id = 1`).Scan(&k.Keyring); err != nil {
		return EncryptionKeys{}, fmt.Errorf("read the keyring: %w", err)
	}

	return k, nil
}

type clusterKeys struct {
	Wrap       encryption.RootWrap
	Keyring    []byte
	ActiveTerm uint32
	KeyringID  []byte
}

func newClusterKeys(sealed encryption.Sealed, activeTerm uint32) clusterKeys {
	k := clusterKeys{
		Wrap: sealed.Wrap, Keyring: sealed.Keyring, ActiveTerm: activeTerm, KeyringID: make([]byte, keyringIDLength),
	}
	_, _ = rand.Read(k.KeyringID)

	return k
}

func (k clusterKeys) validate() error {
	switch {
	case k.ActiveTerm == 0:
		return fmt.Errorf("%w: active term 0", ErrInvalid)
	case k.Wrap.Fingerprint == "" || k.Wrap.Type == "" || k.Wrap.Mechanism == "" || len(k.Wrap.Wrapped) == 0:
		return fmt.Errorf("%w: incomplete root key wrap", ErrInvalid)
	case len(k.Keyring) == 0:
		return fmt.Errorf("%w: empty keyring", ErrInvalid)
	case len(k.KeyringID) != keyringIDLength:
		return fmt.Errorf("%w: a keyring ID of %d bytes", ErrInvalid, len(k.KeyringID))
	}

	return nil
}

func insertClusterKeys(a *applyCtx, k clusterKeys) error {
	ctx, tx := a.ctx, a.tx

	if err := k.validate(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_root_wraps (fingerprint, key_type, mechanism, wrapped_root)
		VALUES (?, ?, ?, ?)`, k.Wrap.Fingerprint, k.Wrap.Type, k.Wrap.Mechanism, k.Wrap.Wrapped); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_keyring (id, sealed) VALUES (1, ?)`, k.Keyring); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_state (id, active_term, keyring_id) VALUES (1, ?, ?)`,
		k.ActiveTerm, k.KeyringID); err != nil {
		return err
	}

	a.events.Encryption = true

	return nil
}

func KeyringSum(sealed []byte) []byte {
	sum := sha256.Sum256(sealed)
	return sum[:]
}

func sameWrapKey(a, b encryption.RootWrap) bool {
	return a.Fingerprint == b.Fingerprint && a.Type == b.Type && a.Mechanism == b.Mechanism
}

func validWrap(w encryption.RootWrap) error {
	if w.Fingerprint == "" || w.Type == "" || w.Mechanism == "" || len(w.Wrapped) == 0 {
		return fmt.Errorf("%w: incomplete root key wrap", ErrInvalid)
	}

	return nil
}

type rotateKeys struct {
	FromTerm uint32              `json:"from_term"`
	Wrap     encryption.RootWrap `json:"wrap"`
	Keyring  []byte              `json:"keyring"`
}

func (d *DB) RotateKeys(ctx context.Context, fromTerm uint32, sealed encryption.Sealed) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdRotateKeys, rotateKeys{
		FromTerm: fromTerm, Wrap: sealed.Wrap,
		Keyring: sealed.Keyring,
	})

	return err
}

func applyRotateKeys(a *applyCtx, p rotateKeys) (struct{}, error) {
	if err := validWrap(p.Wrap); err != nil {
		return struct{}{}, err
	}

	if len(p.Keyring) == 0 || p.FromTerm == 0 || p.FromTerm == math.MaxUint32 {
		return struct{}{}, fmt.Errorf("%w: rotation from term %d", ErrInvalid, p.FromTerm)
	}

	st, err := readEncryptionKeys(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	switch {
	case st.ActiveTerm != p.FromTerm:
		return struct{}{}, fmt.Errorf("%w: the active term is %d, not %d", ErrKeysChanged, st.ActiveTerm, p.FromTerm)
	case len(st.Wraps) != 1:
		return struct{}{}, ErrKeyChange
	case !sameWrapKey(st.Wraps[0], p.Wrap):
		return struct{}{}, fmt.Errorf("%w: the cluster key is %s", ErrKeysChanged, st.Wraps[0].Fingerprint)
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE encryption_root_wraps SET wrapped_root = ? WHERE fingerprint = ?`,
		p.Wrap.Wrapped, p.Wrap.Fingerprint); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE encryption_keyring SET sealed = ? WHERE id = 1`, p.Keyring); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE encryption_state SET active_term = ? WHERE id = 1`,
		p.FromTerm+1); err != nil {
		return struct{}{}, err
	}

	a.events.Encryption = true

	return struct{}{}, nil
}

type addRootWrap struct {
	ActiveTerm uint32              `json:"active_term"`
	KeyringSum []byte              `json:"keyring_sum"`
	Wrap       encryption.RootWrap `json:"wrap"`
}

func (d *DB) AddRootWrap(ctx context.Context, st EncryptionKeys, w encryption.RootWrap) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdAddRootWrap, addRootWrap{
		ActiveTerm: st.ActiveTerm,
		KeyringSum: KeyringSum(st.Keyring), Wrap: w,
	})

	return err
}

func applyAddRootWrap(a *applyCtx, p addRootWrap) (struct{}, error) {
	if err := validWrap(p.Wrap); err != nil {
		return struct{}{}, err
	}

	st, err := readEncryptionKeys(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	switch {
	case !st.Initialized():
		return struct{}{}, fmt.Errorf("%w: the cluster has no keys", ErrInvalid)
	case st.ActiveTerm != p.ActiveTerm || !bytes.Equal(KeyringSum(st.Keyring), p.KeyringSum):
		return struct{}{}, ErrKeysChanged
	case len(st.Wraps) == 1 && sameWrapKey(st.Wraps[0], p.Wrap):
		return struct{}{}, nil
	case len(st.Wraps) != 1:
		return struct{}{}, ErrKeyChange
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO encryption_root_wraps (fingerprint, key_type, mechanism,
		wrapped_root) VALUES (?, ?, ?, ?)`, p.Wrap.Fingerprint, p.Wrap.Type, p.Wrap.Mechanism, p.Wrap.Wrapped); err != nil {
		return struct{}{}, err
	}

	a.events.Encryption = true

	return struct{}{}, nil
}

type removeRootWraps struct {
	Keep encryption.RootWrap `json:"keep"`
}

func (d *DB) RemoveRootWrapsBut(ctx context.Context, keep encryption.RootWrap) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdRemoveRootWraps, removeRootWraps{Keep: keep})

	return err
}

func applyRemoveRootWraps(a *applyCtx, p removeRootWraps) (struct{}, error) {
	st, err := readEncryptionKeys(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	found := false

	for _, w := range st.Wraps {
		if sameWrapKey(w, p.Keep) && bytes.Equal(w.Wrapped, p.Keep.Wrapped) {
			found = true
		}
	}

	if !found {
		return struct{}{}, ErrKeysChanged
	}

	if len(st.Wraps) == 1 {
		return struct{}{}, nil
	}

	if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM encryption_root_wraps WHERE fingerprint != ?`,
		p.Keep.Fingerprint); err != nil {
		return struct{}{}, err
	}

	a.events.Encryption = true

	return struct{}{}, nil
}
