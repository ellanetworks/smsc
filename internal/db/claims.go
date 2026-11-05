package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrStaleTerm = errors.New("a newer leader term owns the recipient")

type Claim struct {
	Term  uint64 `json:"term"`
	Index uint64 `json:"index"`
}

type Fence struct {
	Term  uint64 `json:"term"`
	Claim Claim  `json:"claim"`
	Keep  bool   `json:"keep,omitempty"`
}

type claimRecipient struct {
	MSISDN string `json:"msisdn"`
	Term   uint64 `json:"term"`
}

// TS 23.040 clause 6.2
func (d *DB) ClaimRecipient(ctx context.Context, msisdn string, term uint64) (_ Claim, err error) {
	defer d.observe(&err)()

	return submit[Claim](ctx, d, cmdClaimRecipient, claimRecipient{MSISDN: msisdn, Term: term})
}

func applyClaimRecipient(a *applyCtx, p claimRecipient) (Claim, error) {
	if p.Term < a.term {
		return Claim{}, fmt.Errorf("%w: claim of term %d appended in term %d", ErrStaleTerm, p.Term, a.term)
	}

	var held sql.NullInt64

	err := a.tx.QueryRowContext(a.ctx, `SELECT claim_term FROM recipients WHERE msisdn = ?`, p.MSISDN).Scan(&held)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, err
	}

	if held.Valid && uint64(held.Int64) > p.Term {
		return Claim{}, fmt.Errorf("%w: recipient claimed in term %d", ErrStaleTerm, held.Int64)
	}

	if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO recipients (msisdn, claim_term, claim_index, claimed_at, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (msisdn) DO UPDATE SET claim_term = excluded.claim_term,
		claim_index = excluded.claim_index, claimed_at = excluded.claimed_at, updated_at = excluded.updated_at`,
		p.MSISDN, p.Term, a.index, a.leaderTime, a.leaderTime); err != nil {
		return Claim{}, err
	}

	return Claim{Term: p.Term, Index: a.index}, nil
}

type releaseRecipient struct {
	MSISDN string `json:"msisdn"`
	Claim  Claim  `json:"claim"`
}

func (d *DB) ReleaseRecipient(ctx context.Context, msisdn string, c Claim) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdReleaseRecipient, releaseRecipient{MSISDN: msisdn, Claim: c})

	return err
}

func applyReleaseRecipient(a *applyCtx, p releaseRecipient) (struct{}, error) {
	return struct{}{}, release(a, p.MSISDN, p.Claim)
}

func release(a *applyCtx, msisdn string, c Claim) error {
	if c.Term == 0 {
		return nil
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE recipients SET claim_term = NULL, claim_index = NULL, claimed_at = NULL,
		updated_at = ?
		WHERE msisdn = ? AND claim_term = ? AND claim_index = ?`, a.leaderTime, msisdn, c.Term, c.Index)
	if err != nil {
		return err
	}

	if n, _ := res.RowsAffected(); n > 0 && c.Term < a.term {
		a.events.Messages = true
	}

	return nil
}

func fenced(a *applyCtx, msisdn string, f Fence) error {
	if f.Term == 0 {
		return nil
	}

	var held sql.NullInt64

	err := a.tx.QueryRowContext(a.ctx, `SELECT claim_term FROM recipients WHERE msisdn = ?`, msisdn).Scan(&held)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if held.Valid && uint64(held.Int64) > f.Term {
		return fmt.Errorf("%w: outcome of term %d, recipient claimed in term %d", ErrStaleTerm, f.Term, held.Int64)
	}

	return nil
}

func (d *DB) ClaimedBefore(ctx context.Context, term uint64, since time.Time) (_ []string, err error) {
	defer d.observe(&err)()

	rows, err := d.reader().QueryContext(ctx, `SELECT msisdn FROM recipients WHERE claim_term < ? AND claimed_at >= ?
		ORDER BY msisdn`, term, since.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("list claimed recipients: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var msisdn string
		if err := rows.Scan(&msisdn); err != nil {
			return nil, fmt.Errorf("list claimed recipients: %w", err)
		}

		out = append(out, msisdn)
	}

	return out, rows.Err()
}
