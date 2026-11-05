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

type ESME struct {
	ID              int64
	SystemID        string
	UserID          int64
	PasswordHash    string
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

func (d *DB) CountESMEs(ctx context.Context) (_ int, err error) {
	defer d.observe(&err)()

	var n int
	if err := d.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM esmes`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count ESMEs: %w", err)
	}

	return n, nil
}

func (d *DB) CreateESME(ctx context.Context, e ESME) (_ int64, err error) {
	defer d.observe(&err)()

	id, err := submit[int64](ctx, d, cmdCreateESME, e)
	if err != nil {
		return 0, fmt.Errorf("create ESME: %w", err)
	}

	return id, nil
}

func applyCreateESME(a *applyCtx, e ESME) (int64, error) {
	res, err := a.tx.ExecContext(a.ctx, `INSERT INTO esmes (system_id, user_id, password_hash, created_at)
		VALUES (?, ?, ?, ?)`, e.SystemID, e.UserID, e.PasswordHash, e.CreatedAt.UTC().UnixNano())
	if isUnique(err) {
		return 0, ErrAlreadyExists
	}

	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := insertNetworks(a.ctx, a.tx, id, e.AllowedNetworks); err != nil {
		if isUnique(err) {
			return 0, ErrAlreadyExists
		}

		return 0, err
	}

	return id, nil
}

func (d *DB) UpdateESME(ctx context.Context, e ESME) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdUpdateESME, e); err != nil {
		return fmt.Errorf("update ESME: %w", err)
	}

	return nil
}

func applyUpdateESME(a *applyCtx, e ESME) (struct{}, error) {
	ctx, tx := a.ctx, a.tx

	res, err := tx.ExecContext(ctx, `UPDATE esmes SET password_hash = COALESCE(NULLIF(?, ''), password_hash)
		WHERE id = ?`, e.PasswordHash, e.ID)
	if err != nil {
		return struct{}{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return struct{}{}, err
	} else if n == 0 {
		return struct{}{}, ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM esme_networks WHERE esme_id = ?`, e.ID); err != nil {
		return struct{}{}, err
	}

	if err := insertNetworks(ctx, tx, e.ID, e.AllowedNetworks); err != nil {
		if isUnique(err) {
			return struct{}{}, ErrAlreadyExists
		}

		return struct{}{}, err
	}

	a.events.ClosedESMEs = append(a.events.ClosedESMEs, e.ID)

	return struct{}{}, nil
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

func (d *DB) DeleteESME(ctx context.Context, id int64) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdDeleteESME, id); err != nil {
		return fmt.Errorf("delete ESME: %w", err)
	}

	return nil
}

func applyDeleteESME(a *applyCtx, id int64) (struct{}, error) {
	res, err := a.tx.ExecContext(a.ctx, `DELETE FROM esmes WHERE id = ?`, id)
	if err != nil {
		return struct{}{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return struct{}{}, err
	} else if n == 0 {
		return struct{}{}, ErrNotFound
	}

	a.events.ClosedESMEs = append(a.events.ClosedESMEs, id)

	return struct{}{}, nil
}

func esmesOfUser(ctx context.Context, q querier, userID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM esmes WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	var ids []int64

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	return ids, rows.Err()
}

func (d *DB) GetESMEBySystemID(ctx context.Context, systemID string) (_ ESME, err error) {
	defer d.observe(&err)()

	e, err := scanESME(d.reader().QueryRowContext(ctx, `SELECT `+esmeColumns+` FROM esmes WHERE system_id = ?`, systemID))
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

func (d *DB) ListESMEs(ctx context.Context) (_ []ESME, err error) {
	defer d.observe(&err)()

	return d.listESMEs(ctx, ``)
}

func (d *DB) ListESMEsOfUser(ctx context.Context, userID int64) (_ []ESME, err error) {
	defer d.observe(&err)()

	return d.listESMEs(ctx, ` WHERE user_id = ?`, userID)
}

func (d *DB) listESMEs(ctx context.Context, where string, args ...any) ([]ESME, error) {
	rows, err := d.reader().QueryContext(ctx, `SELECT `+esmeColumns+` FROM esmes`+where+` ORDER BY id`, args...)
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

	rows, err := d.reader().QueryContext(ctx, `SELECT esme_id, prefix FROM esme_networks WHERE esme_id IN (?`+
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
