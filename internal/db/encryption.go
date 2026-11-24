package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ellanetworks/smsc/internal/encryption"
)

const keyringIDLength = encryption.KeyringIDSize

var (
	ErrKeysChanged = errors.New("the encryption keys changed meanwhile")
	ErrKeyChange   = errors.New("a cluster key change is in progress")
)

type EncryptionKeys struct {
	ActiveTerm uint32
	KeyringID  []byte
	Wraps      []encryption.RootWrap
	Keyring    []byte
	Terms      []uint32
	Upgrades   map[uint32][]byte
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

func (d *DB) EncryptionKeys(ctx context.Context) (_ EncryptionKeys, err error) {
	defer d.observe(&err)()

	return readEncryptionKeys(ctx, d.reader())
}

func readEncryptionKeys(ctx context.Context, q querier) (EncryptionKeys, error) {
	var k EncryptionKeys

	err := q.QueryRowContext(ctx, `SELECT active_term, keyring_id, sealed FROM encryption_keyring WHERE id = 1`).
		Scan(&k.ActiveTerm, &k.KeyringID, &k.Keyring)
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

	terms, err := q.QueryContext(ctx, `SELECT term, upgrade FROM encryption_terms ORDER BY term`)
	if err != nil {
		return EncryptionKeys{}, fmt.Errorf("read the data key terms: %w", err)
	}

	defer func() { _ = terms.Close() }()

	k.Upgrades = map[uint32][]byte{}

	for terms.Next() {
		var (
			term    uint32
			upgrade []byte
		)

		if err := terms.Scan(&term, &upgrade); err != nil {
			return EncryptionKeys{}, fmt.Errorf("read the data key terms: %w", err)
		}

		k.Terms = append(k.Terms, term)

		if upgrade != nil {
			k.Upgrades[term] = upgrade
		}
	}

	return k, terms.Err()
}

type clusterKeys struct {
	Wrap       encryption.RootWrap
	Keyring    []byte
	ActiveTerm uint32
	KeyringID  []byte
}

func newClusterKeys(sealed encryption.Sealed, activeTerm uint32) clusterKeys {
	return clusterKeys{Wrap: sealed.Wrap, Keyring: sealed.Keyring, ActiveTerm: activeTerm, KeyringID: sealed.KeyringID}
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

	if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_keyring (id, keyring_id, active_term, sealed)
		VALUES (1, ?, ?, ?)`, k.KeyringID, k.ActiveTerm, k.Keyring); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_terms (term, upgrade, created_at) VALUES (?, NULL, ?)`,
		k.ActiveTerm, a.leaderTime); err != nil {
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
