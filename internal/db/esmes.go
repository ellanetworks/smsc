package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// ESME is an account that an External Short Message Entity binds to the SMPP listener with. It acts as its user: the
// messages it submits are the user's, and the user's role decides which binds it may make.
type ESME struct {
	ID       int64
	SystemID string
	UserID   int64
	// PasswordHash is the bcrypt hash of the SMPP password.
	PasswordHash string
	// AllowedNetworks are the networks that it may bind from. None means no address may bind.
	AllowedNetworks []netip.Prefix
	CreatedAt       time.Time
}

const esmeColumns = `id, system_id, user_id, password_hash, created_at`

func scanESME(row rowScanner) (ESME, error) {
	var (
		e       ESME
		created int64
	)

	if err := row.Scan(&e.ID, &e.SystemID, &e.UserID, &e.PasswordHash, &created); err != nil {
		return ESME{}, err
	}

	e.CreatedAt = time.Unix(0, created).UTC()

	return e, nil
}

// CountESMEs returns how many ESMEs exist.
func (d *DB) CountESMEs(ctx context.Context) (_ int, err error) {
	defer d.observe(&err)()

	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM esmes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count ESMEs: %w", err)
	}

	return n, nil
}

// CreateESME stores an ESME and returns its ID. It returns ErrAlreadyExists for a system_id that another ESME has.
func (d *DB) CreateESME(ctx context.Context, e ESME) (_ int64, err error) {
	defer d.observe(&err)()

	var id int64

	err = d.inTx(ctx, "create ESME", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO esmes (system_id, user_id, password_hash, created_at)
			VALUES (?, ?, ?, ?)`, e.SystemID, e.UserID, e.PasswordHash, e.CreatedAt.UTC().UnixNano())
		if err != nil {
			return err
		}

		if id, err = res.LastInsertId(); err != nil {
			return err
		}

		return insertNetworks(ctx, tx, id, e.AllowedNetworks)
	})
	if isUnique(err) {
		return 0, fmt.Errorf("create ESME: %w", ErrAlreadyExists)
	}

	return id, err
}

// UpdateESME replaces the password hash and networks of the ESME of e.ID. It keeps the password hash when
// e.PasswordHash is empty. It returns ErrNotFound for an unknown ID.
func (d *DB) UpdateESME(ctx context.Context, e ESME) (err error) {
	defer d.observe(&err)()

	return d.inTx(ctx, "update ESME", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE esmes SET password_hash = COALESCE(NULLIF(?, ''), password_hash)
			WHERE id = ?`, e.PasswordHash, e.ID)
		if err != nil {
			return err
		}

		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM esme_networks WHERE esme_id = ?`, e.ID); err != nil {
			return err
		}

		return insertNetworks(ctx, tx, e.ID, e.AllowedNetworks)
	})
}

func insertNetworks(ctx context.Context, tx *sql.Tx, id int64, networks []netip.Prefix) error {
	for _, p := range networks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO esme_networks (esme_id, prefix) VALUES (?, ?)`,
			id, p.String()); err != nil {
			return err
		}
	}

	return nil
}

// DeleteESME deletes an ESME, with its routing rows and its notifications. Its messages stay, as its user's. It returns
// ErrNotFound for an unknown ID.
func (d *DB) DeleteESME(ctx context.Context, id int64) (err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM esmes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete ESME: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("delete ESME: %w", err)
	} else if n == 0 {
		return fmt.Errorf("delete ESME: %w", ErrNotFound)
	}

	return nil
}

// GetESMEBySystemID returns the ESME of a system_id, which is compared exactly (SMPP 3.4 section 5.2.1 defines no case
// folding).
func (d *DB) GetESMEBySystemID(ctx context.Context, systemID string) (_ ESME, err error) {
	defer d.observe(&err)()

	e, err := scanESME(d.conn.QueryRowContext(ctx, `SELECT `+esmeColumns+` FROM esmes WHERE system_id = ?`, systemID))
	if errors.Is(err, sql.ErrNoRows) {
		return ESME{}, ErrNotFound
	}

	if err != nil {
		return ESME{}, fmt.Errorf("get ESME: %w", err)
	}

	if err := d.esmeNetworks(ctx, []*ESME{&e}); err != nil {
		return ESME{}, err
	}

	return e, nil
}

// ListESMEs returns every ESME, in the order they were created.
func (d *DB) ListESMEs(ctx context.Context) (_ []ESME, err error) {
	defer d.observe(&err)()

	return d.listESMEs(ctx, ``)
}

// ListESMEsOfUser returns the ESMEs that act as a user.
func (d *DB) ListESMEsOfUser(ctx context.Context, userID int64) (_ []ESME, err error) {
	defer d.observe(&err)()

	return d.listESMEs(ctx, ` WHERE user_id = ?`, userID)
}

func (d *DB) listESMEs(ctx context.Context, where string, args ...any) ([]ESME, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+esmeColumns+` FROM esmes`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list ESMEs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	esmes := []ESME{}

	for rows.Next() {
		e, err := scanESME(rows)
		if err != nil {
			return nil, fmt.Errorf("list ESMEs: %w", err)
		}

		esmes = append(esmes, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ESMEs: %w", err)
	}

	ptrs := make([]*ESME, len(esmes))
	for i := range esmes {
		ptrs[i] = &esmes[i]
	}

	if err := d.esmeNetworks(ctx, ptrs); err != nil {
		return nil, err
	}

	return esmes, nil
}

func (d *DB) esmeNetworks(ctx context.Context, esmes []*ESME) error {
	if len(esmes) == 0 {
		return nil
	}

	index := make(map[int64]*ESME, len(esmes))
	args := make([]any, len(esmes))

	for i, e := range esmes {
		index[e.ID], args[i] = e, e.ID
		e.AllowedNetworks = []netip.Prefix{}
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT esme_id, prefix FROM esme_networks WHERE esme_id IN (?`+
		strings.Repeat(`, ?`, len(esmes)-1)+`) ORDER BY esme_id, rowid`, args...)
	if err != nil {
		return fmt.Errorf("get ESME networks: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id     int64
			prefix string
		)

		if err := rows.Scan(&id, &prefix); err != nil {
			return fmt.Errorf("get ESME networks: %w", err)
		}

		p, err := netip.ParsePrefix(prefix)
		if err != nil {
			return fmt.Errorf("get ESME networks: ESME %d: %w", id, err)
		}

		index[id].AllowedNetworks = append(index[id].AllowedNetworks, p)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("get ESME networks: %w", err)
	}

	return nil
}
