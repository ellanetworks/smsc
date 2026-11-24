package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/ellanetworks/smsc/internal/encryption"
)

var ErrStaleDataKey = fmt.Errorf("%w: %w", ErrBusy, ErrKeysChanged)

var resealable = []encryption.Location{jwtSecretAt, cookieKeyAt, joinKeyAt, clientSecretAt}

type content struct {
	table, column string
}

var (
	tpduContent   = content{"messages", "tpdu"}
	reportContent = content{"delivery_attempts", "deliver_report"}
)

type ResealedValue struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Old    []byte `json:"old"`
	New    []byte `json:"new"`
}

type KeyChange struct {
	KeyringSum []byte              `json:"keyring_sum"`
	Term       uint32              `json:"term"`
	Retire     bool                `json:"retire"`
	Wrap       encryption.RootWrap `json:"wrap"`
	Keyring    []byte              `json:"keyring"`
	Upgrade    []byte              `json:"upgrade"`
	Secrets    []ResealedValue     `json:"secrets"`
}

func termBytes(term uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, term)
}

func requireSealed(ctx context.Context, q querier, v []byte) error {
	if v == nil {
		return nil
	}

	term, err := encryption.Term(v)
	if err != nil {
		return fmt.Errorf("%w: a value is not encrypted: %w", ErrInvalid, err)
	}

	var live int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM encryption_terms WHERE term = ?`, term).
		Scan(&live); err != nil {
		return err
	}

	if live == 0 {
		return fmt.Errorf("%w: a value is encrypted under the data key term %d, which the cluster no longer uses",
			ErrStaleDataKey, term)
	}

	return nil
}

func (d *DB) ChangeKeys(ctx context.Context, c KeyChange) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdChangeKeys, c)

	return err
}

func applyChangeKeys(a *applyCtx, p KeyChange) (struct{}, error) {
	if err := validWrap(p.Wrap); err != nil {
		return struct{}{}, err
	}

	if len(p.Keyring) == 0 || len(p.Upgrade) == 0 {
		return struct{}{}, fmt.Errorf("%w: a key change without a keyring or upgrade", ErrInvalid)
	}

	st, err := readEncryptionKeys(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	switch {
	case !st.Initialized():
		return struct{}{}, fmt.Errorf("%w: the cluster has no keys", ErrInvalid)
	case !bytes.Equal(KeyringSum(st.Keyring), p.KeyringSum) || st.ActiveTerm+1 != p.Term:
		return struct{}{}, ErrKeysChanged
	case !p.Retire && len(st.Wraps) != 1:
		return struct{}{}, ErrKeyChange
	case !p.Retire && !sameWrapKey(st.Wraps[0], p.Wrap):
		return struct{}{}, fmt.Errorf("%w: the cluster key is %s", ErrKeysChanged, st.Wraps[0].Fingerprint)
	case p.Retire && (len(st.Wraps) < 2 || !slices.ContainsFunc(st.Wraps, func(w encryption.RootWrap) bool {
		return sameWrapKey(w, p.Wrap)
	})):
		return struct{}{}, ErrKeysChanged
	}

	if err := reseal(a, p.Term, p.Secrets); err != nil {
		return struct{}{}, err
	}

	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM encryption_root_wraps`, nil},
		{
			`INSERT INTO encryption_root_wraps (fingerprint, key_type, mechanism, wrapped_root) VALUES (?, ?, ?, ?)`,
			[]any{p.Wrap.Fingerprint, p.Wrap.Type, p.Wrap.Mechanism, p.Wrap.Wrapped},
		},
		{`UPDATE encryption_keyring SET sealed = ?, active_term = ? WHERE id = 1`, []any{p.Keyring, p.Term}},
		{`INSERT INTO encryption_terms (term, upgrade) VALUES (?, ?)`, []any{p.Term, p.Upgrade}},
	} {
		if _, err := a.tx.ExecContext(a.ctx, q.sql, q.args...); err != nil {
			return struct{}{}, err
		}
	}

	a.events.Encryption = true

	return struct{}{}, nil
}

func reseal(a *applyCtx, term uint32, values []ResealedValue) error {
	for _, v := range values {
		at := slices.IndexFunc(resealable, func(l encryption.Location) bool {
			return l.Table == v.Table && l.Column == v.Column
		})
		if at < 0 {
			return fmt.Errorf("%w: %s.%s is not a secret", ErrInvalid, v.Table, v.Column)
		}

		if got, err := encryption.Term(v.New); err != nil || got != term {
			return fmt.Errorf("%w: the %s is not encrypted under term %d", ErrInvalid, v.Table, term)
		}

		res, err := a.tx.ExecContext(a.ctx, `UPDATE `+v.Table+` SET `+v.Column+` = ? WHERE id = 1 AND `+v.Column+` = ?`,
			v.New, v.Old)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: the %s changed", ErrKeysChanged, v.Table)
		}

		if v.Table == clientSecretAt.Table {
			a.events.OIDC = true
		}
	}

	return nil
}

func (d *DB) resealSecrets(ctx context.Context, next *encryption.Keyring) ([]ResealedValue, error) {
	var out []ResealedValue

	for _, at := range resealable {
		var value []byte

		err := d.reader().QueryRowContext(ctx, `SELECT `+at.Column+` FROM `+at.Table+` WHERE id = 1`).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) || err == nil && value == nil {
			continue
		}

		if err != nil {
			return nil, err
		}

		plain, err := d.keys.Decrypt(value, at)
		if err != nil {
			return nil, fmt.Errorf("decrypt the %s: %w", at.Table, err)
		}

		sealed, err := next.Encrypt(plain, at)
		clear(plain)

		if err != nil {
			return nil, err
		}

		out = append(out, ResealedValue{Table: at.Table, Column: at.Column, Old: value, New: sealed})
	}

	return out, nil
}

func (d *DB) PrepareKeyChange(ctx context.Context, st EncryptionKeys, retire bool, sealed encryption.Sealed,
	next *encryption.Keyring, upgrade encryption.Upgrade,
) (_ KeyChange, err error) {
	defer d.observe(&err)()

	if !d.unlocked() {
		return KeyChange{}, ErrKeysLocked
	}

	secrets, err := d.resealSecrets(ctx, next)
	if err != nil {
		return KeyChange{}, err
	}

	return KeyChange{
		KeyringSum: KeyringSum(st.Keyring), Term: upgrade.Term, Retire: retire, Wrap: sealed.Wrap,
		Keyring: sealed.Keyring, Upgrade: upgrade.Sealed, Secrets: secrets,
	}, nil
}

type ReencryptedValue struct {
	Table string `json:"table"`
	ID    int64  `json:"id"`
	Old   []byte `json:"old"`
	New   []byte `json:"new"`
}

type reencrypt struct {
	Values []ReencryptedValue `json:"values"`
}

func applyReencrypt(a *applyCtx, p reencrypt) (int, error) {
	updated := 0

	for _, v := range p.Values {
		c := tpduContent
		if v.Table == reportContent.table {
			c = reportContent
		} else if v.Table != tpduContent.table {
			return 0, fmt.Errorf("%w: %s holds no encrypted content", ErrInvalid, v.Table)
		}

		if err := requireSealed(a.ctx, a.tx, v.New); err != nil || v.New == nil {
			return 0, fmt.Errorf("%w: a re-encrypted value is not sealed under a live term: %w", ErrInvalid, err)
		}

		res, err := a.tx.ExecContext(a.ctx, `UPDATE `+c.table+` SET `+c.column+` = ? WHERE id = ? AND `+c.column+` = ?`,
			v.New, v.ID, v.Old)
		if err != nil {
			return 0, err
		}

		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}

		updated += int(n)
	}

	return updated, nil
}

type staleRow struct {
	value ReencryptedValue
	at    encryption.Location
}

func (d *DB) staleRows(ctx context.Context, active uint32, limit int) ([]staleRow, error) {
	var out []staleRow

	rows, err := d.reader().QueryContext(ctx, `SELECT id, tpdu, cipher_id, msisdn, originator_ton, originator_npi,
		originator FROM messages WHERE cipher_id IS NOT NULL AND substr(tpdu, 3, 4) != ? ORDER BY id LIMIT ?`,
		termBytes(active), limit)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var (
			r        staleRow
			cipherID []byte
			msisdn   string
			orig     Address
		)

		if err := rows.Scan(&r.value.ID, &r.value.Old, &cipherID, &msisdn, &orig.TypeOfNumber, &orig.NumberingPlan,
			&orig.Digits); err != nil {
			_ = rows.Close()
			return nil, err
		}

		r.value.Table, r.at = tpduContent.table, tpduAt(cipherID, msisdn, orig)
		out = append(out, r)
	}

	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	if len(out) >= limit {
		return out, nil
	}

	rows, err = d.reader().QueryContext(ctx, `SELECT id, message_id, deliver_report, cipher_id FROM delivery_attempts
		WHERE cipher_id IS NOT NULL AND substr(deliver_report, 3, 4) != ? ORDER BY id LIMIT ?`,
		termBytes(active), limit-len(out))
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			r         staleRow
			messageID int64
			cipherID  []byte
		)

		if err := rows.Scan(&r.value.ID, &messageID, &r.value.Old, &cipherID); err != nil {
			return nil, err
		}

		r.value.Table, r.at = reportContent.table, deliverReportAt(cipherID, messageID)
		out = append(out, r)
	}

	return out, rows.Err()
}

func (d *DB) ReencryptStale(ctx context.Context, limit int) (stale, undecryptable int, err error) {
	defer d.observe(&err)()

	if !d.unlocked() {
		return 0, 0, ErrKeysLocked
	}

	active, err := d.keys.ActiveTerm()
	if err != nil {
		return 0, 0, err
	}

	rows, err := d.staleRows(ctx, active, limit)
	if err != nil {
		return 0, 0, err
	}

	var batch reencrypt

	for _, r := range rows {
		plain, err := d.keys.Decrypt(r.value.Old, r.at)
		if err != nil {
			undecryptable++
			continue
		}

		r.value.New, err = d.keys.Encrypt(plain, r.at)
		clear(plain)

		if err != nil {
			return 0, 0, err
		}

		batch.Values = append(batch.Values, r.value)
	}

	if len(batch.Values) > 0 {
		if _, err := submit[int](ctx, d, cmdReencrypt, batch); err != nil {
			return 0, 0, err
		}
	}

	return len(rows), undecryptable, nil
}

func termUsage(ctx context.Context, q querier, term uint32) (int, error) {
	var total int

	for _, query := range []string{
		`SELECT count(*) FROM messages WHERE cipher_id IS NOT NULL AND substr(tpdu, 3, 4) = ?`,
		`SELECT count(*) FROM delivery_attempts WHERE cipher_id IS NOT NULL AND substr(deliver_report, 3, 4) = ?`,
	} {
		var n int
		if err := q.QueryRowContext(ctx, query, termBytes(term)).Scan(&n); err != nil {
			return 0, err
		}

		total += n
	}

	for _, at := range resealable {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+at.Table+` WHERE `+at.Column+` IS NOT NULL AND substr(`+
			at.Column+`, 3, 4) = ?`, termBytes(term)).Scan(&n); err != nil {
			return 0, err
		}

		total += n
	}

	return total, nil
}

func (d *DB) UnusedTerms(ctx context.Context, st EncryptionKeys) (_ []uint32, err error) {
	defer d.observe(&err)()

	var unused []uint32

	for _, term := range st.Terms {
		if term == st.ActiveTerm {
			continue
		}

		n, err := termUsage(ctx, d.reader(), term)
		if err != nil {
			return nil, err
		}

		if n == 0 {
			unused = append(unused, term)
		}
	}

	return unused, nil
}

type pruneTerms struct {
	KeyringSum []byte              `json:"keyring_sum"`
	Wrap       encryption.RootWrap `json:"wrap"`
	Keyring    []byte              `json:"keyring"`
	Remove     []uint32            `json:"remove"`
}

func (d *DB) PruneTerms(ctx context.Context, st EncryptionKeys, sealed encryption.Sealed, remove []uint32) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdPruneTerms, pruneTerms{
		KeyringSum: KeyringSum(st.Keyring), Wrap: sealed.Wrap, Keyring: sealed.Keyring, Remove: remove,
	})

	return err
}

func applyPruneTerms(a *applyCtx, p pruneTerms) (struct{}, error) {
	if err := validWrap(p.Wrap); err != nil {
		return struct{}{}, err
	}

	st, err := readEncryptionKeys(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	switch {
	case !st.Initialized() || !bytes.Equal(KeyringSum(st.Keyring), p.KeyringSum):
		return struct{}{}, ErrKeysChanged
	case len(st.Wraps) != 1:
		return struct{}{}, ErrKeyChange
	case !sameWrapKey(st.Wraps[0], p.Wrap) || len(p.Remove) == 0:
		return struct{}{}, ErrKeysChanged
	}

	for _, term := range p.Remove {
		if term == st.ActiveTerm || !slices.Contains(st.Terms, term) {
			return struct{}{}, fmt.Errorf("%w: the term %d cannot be removed", ErrKeysChanged, term)
		}

		n, err := termUsage(a.ctx, a.tx, term)
		if err != nil {
			return struct{}{}, err
		}

		if n > 0 {
			return struct{}{}, fmt.Errorf("%w: %d values still use the term %d", ErrKeysChanged, n, term)
		}

		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM encryption_terms WHERE term = ?`, term); err != nil {
			return struct{}{}, err
		}
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE encryption_root_wraps SET wrapped_root = ? WHERE fingerprint = ?`,
		p.Wrap.Wrapped, p.Wrap.Fingerprint); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE encryption_keyring SET sealed = ? WHERE id = 1`, p.Keyring); err != nil {
		return struct{}{}, err
	}

	a.events.Encryption = true

	return struct{}{}, nil
}

func ReencryptBackup(ctx context.Context, path string, from *encryption.Keyring, to *encryption.Keys) error {
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

	move := func(value []byte, at encryption.Location) ([]byte, error) {
		plain, err := from.Decrypt(value, at)
		if err != nil {
			return nil, err
		}

		defer clear(plain)

		return to.Encrypt(plain, at)
	}

	if err := reencryptRows(ctx, tx, `SELECT id, tpdu, cipher_id, msisdn, originator_ton, originator_npi, originator
		FROM messages WHERE cipher_id IS NOT NULL`, `UPDATE messages SET tpdu = ? WHERE id = ?`,
		func(rows *sql.Rows) (int64, []byte, encryption.Location, error) {
			var (
				id       int64
				value    []byte
				cipherID []byte
				msisdn   string
				orig     Address
			)

			err := rows.Scan(&id, &value, &cipherID, &msisdn, &orig.TypeOfNumber, &orig.NumberingPlan, &orig.Digits)

			return id, value, tpduAt(cipherID, msisdn, orig), err
		}, move); err != nil {
		return fmt.Errorf("re-encrypt the messages: %w", err)
	}

	if err := reencryptRows(ctx, tx, `SELECT id, deliver_report, cipher_id, message_id FROM delivery_attempts
		WHERE cipher_id IS NOT NULL`, `UPDATE delivery_attempts SET deliver_report = ? WHERE id = ?`,
		func(rows *sql.Rows) (int64, []byte, encryption.Location, error) {
			var (
				id, messageID int64
				value         []byte
				cipherID      []byte
			)

			err := rows.Scan(&id, &value, &cipherID, &messageID)

			return id, value, deliverReportAt(cipherID, messageID), err
		}, move); err != nil {
		return fmt.Errorf("re-encrypt the deliver reports: %w", err)
	}

	if err := reencryptRows(ctx, tx, `SELECT id, client_secret FROM oidc WHERE client_secret IS NOT NULL`,
		`UPDATE oidc SET client_secret = ? WHERE id = ?`,
		func(rows *sql.Rows) (int64, []byte, encryption.Location, error) {
			var (
				id    int64
				value []byte
			)

			err := rows.Scan(&id, &value)

			return id, value, clientSecretAt, err
		}, move); err != nil {
		return fmt.Errorf("re-encrypt the OIDC client secret: %w", err)
	}

	return tx.Commit()
}

func reencryptRows(ctx context.Context, tx *sql.Tx, query, update string,
	scan func(*sql.Rows) (int64, []byte, encryption.Location, error),
	move func([]byte, encryption.Location) ([]byte, error),
) error {
	type moved struct {
		id    int64
		value []byte
	}

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}

	var all []moved

	for rows.Next() {
		id, value, at, err := scan(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}

		sealed, err := move(value, at)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("row %d: %w", id, err)
		}

		all = append(all, moved{id, sealed})
	}

	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}

	for _, m := range all {
		if _, err := tx.ExecContext(ctx, update, m.value, m.id); err != nil {
			return err
		}
	}

	return nil
}
