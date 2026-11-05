package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var ErrTooManySessions = errors.New("too many SMPP sessions for the ESME")

type SMPPSession struct {
	NodeID    string    `json:"node_id"`
	SessionID uint64    `json:"session_id"`
	ESMEID    int64     `json:"esme_id"`
	BindType  string    `json:"bind_type"`
	Remote    string    `json:"remote"`
	BoundAt   time.Time `json:"bound_at"`
}

type openSMPPSession struct {
	Session SMPPSession `json:"session"`
	Limit   int         `json:"max"`
}

func (d *DB) OpenSMPPSession(ctx context.Context, s SMPPSession, limit int) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdOpenSMPPSession, openSMPPSession{Session: s, Limit: limit})

	return err
}

func applyOpenSMPPSession(a *applyCtx, p openSMPPSession) (struct{}, error) {
	s := p.Session

	if s.SessionID > math.MaxInt64 || s.NodeID == "" {
		return struct{}{}, fmt.Errorf("%w: SMPP session %s/%d", ErrInvalid, s.NodeID, s.SessionID)
	}

	var count int

	if err := a.tx.QueryRowContext(a.ctx, `SELECT COUNT(*) FROM smpp_sessions WHERE esme_id = ?`, s.ESMEID).
		Scan(&count); err != nil {
		return struct{}{}, err
	}

	if p.Limit > 0 && count >= p.Limit {
		return struct{}{}, fmt.Errorf("%w: %d bound", ErrTooManySessions, count)
	}

	var exists bool

	if err := a.tx.QueryRowContext(a.ctx, `SELECT EXISTS (SELECT 1 FROM esmes WHERE id = ?)`, s.ESMEID).
		Scan(&exists); err != nil {
		return struct{}{}, err
	}

	if !exists {
		return struct{}{}, fmt.Errorf("ESME %d: %w", s.ESMEID, ErrNotFound)
	}

	_, err := a.tx.ExecContext(a.ctx, `INSERT INTO smpp_sessions (node_id, session_id, esme_id, bind_type, remote, bound_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (node_id, session_id) DO UPDATE SET esme_id = excluded.esme_id,
		bind_type = excluded.bind_type, remote = excluded.remote, bound_at = excluded.bound_at`,
		s.NodeID, s.SessionID, s.ESMEID, s.BindType, s.Remote, s.BoundAt.UTC().UnixNano())

	return struct{}{}, err
}

type closeSMPPSession struct {
	NodeID    string `json:"node_id"`
	SessionID uint64 `json:"session_id"`
}

func (d *DB) CloseSMPPSession(ctx context.Context, nodeID string, sessionID uint64) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdCloseSMPPSession, closeSMPPSession{NodeID: nodeID, SessionID: sessionID})

	return err
}

func applyCloseSMPPSession(a *applyCtx, p closeSMPPSession) (struct{}, error) {
	if p.SessionID > math.MaxInt64 {
		return struct{}{}, fmt.Errorf("%w: SMPP session %d", ErrInvalid, p.SessionID)
	}

	_, err := a.tx.ExecContext(a.ctx, `DELETE FROM smpp_sessions WHERE node_id = ? AND session_id = ?`, p.NodeID,
		p.SessionID)

	return struct{}{}, err
}

type syncSMPPSessions struct {
	NodeID string   `json:"node_id"`
	Live   []uint64 `json:"live"`
}

func (d *DB) SyncSMPPSessions(ctx context.Context, nodeID string, live []uint64) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdSyncSMPPSessions, syncSMPPSessions{NodeID: nodeID, Live: live})

	return err
}

func applySyncSMPPSessions(a *applyCtx, p syncSMPPSessions) (struct{}, error) {
	for _, id := range p.Live {
		if id > math.MaxInt64 {
			return struct{}{}, fmt.Errorf("%w: SMPP session %d", ErrInvalid, id)
		}
	}

	return struct{}{}, dropSMPPSessions(a, p.NodeID, p.Live)
}

func dropSMPPSessions(a *applyCtx, nodeID string, keep []uint64) error {
	query := `DELETE FROM smpp_sessions WHERE node_id = ?`
	args := []any{nodeID}

	if len(keep) > 0 {
		query += ` AND session_id NOT IN (?` + strings.Repeat(`, ?`, len(keep)-1) + `)`

		for _, id := range keep {
			args = append(args, id)
		}
	}

	_, err := a.tx.ExecContext(a.ctx, query, args...)

	return err
}

func (d *DB) SMPPSessions(ctx context.Context, esmeID int64) (_ []SMPPSession, err error) {
	defer d.observe(&err)()

	return d.querySMPPSessions(ctx, `esme_id = ? ORDER BY bound_at, node_id, session_id`, esmeID)
}

func (d *DB) NodeSMPPSessions(ctx context.Context, nodeID string) (_ []uint64, err error) {
	defer d.observe(&err)()

	sessions, err := d.querySMPPSessions(ctx, `node_id = ? ORDER BY session_id`, nodeID)
	if err != nil {
		return nil, err
	}

	ids := make([]uint64, len(sessions))
	for i, s := range sessions {
		ids[i] = s.SessionID
	}

	return ids, nil
}

func (d *DB) SMPPSessionNodes(ctx context.Context) (_ []string, err error) {
	defer d.observe(&err)()

	rows, err := d.reader().QueryContext(ctx, `SELECT DISTINCT node_id FROM smpp_sessions ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("list SMPP session nodes: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list SMPP session nodes: %w", err)
		}

		out = append(out, id)
	}

	return out, rows.Err()
}

func (d *DB) querySMPPSessions(ctx context.Context, where string, args ...any) ([]SMPPSession, error) {
	rows, err := d.reader().QueryContext(ctx, `SELECT node_id, session_id, esme_id, bind_type, remote, bound_at
		FROM smpp_sessions WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list SMPP sessions: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []SMPPSession

	for rows.Next() {
		var (
			s     SMPPSession
			bound int64
		)

		if err := rows.Scan(&s.NodeID, &s.SessionID, &s.ESMEID, &s.BindType, &s.Remote, &bound); err != nil {
			return nil, fmt.Errorf("list SMPP sessions: %w", err)
		}

		s.BoundAt = time.Unix(0, bound).UTC()
		out = append(out, s)
	}

	return out, rows.Err()
}

type claimNotifications struct {
	NodeID string        `json:"node_id"`
	ESMEID int64         `json:"esme_id"`
	Now    time.Time     `json:"now"`
	Lease  time.Duration `json:"lease"`
	Limit  int           `json:"max"`
}

// SMPP 3.4 section 4.6.2
func (d *DB) ClaimNotifications(ctx context.Context, nodeID string, esmeID int64, now time.Time, lease time.Duration,
	limit int,
) (_ []Notification, err error) {
	defer d.observe(&err)()

	return submit[[]Notification](ctx, d, cmdClaimNotifications, claimNotifications{
		NodeID: nodeID, ESMEID: esmeID, Now: now, Lease: lease, Limit: limit,
	})
}

func applyClaimNotifications(a *applyCtx, p claimNotifications) ([]Notification, error) {
	due, err := dueNotifications(a.ctx, a.tx, p.ESMEID, p.Now, p.Limit)
	if err != nil {
		return nil, err
	}

	until := p.Now.Add(p.Lease).UTC().UnixNano()

	for _, n := range due {
		if _, err := a.tx.ExecContext(a.ctx, `UPDATE smpp_notifications SET next_attempt_at = MIN(?, expires_at),
			claimed_by = ? WHERE id = ?`, until, p.NodeID, n.ID); err != nil {
			return nil, err
		}
	}

	return due, nil
}

type releaseNotification struct {
	ID     int64     `json:"id"`
	NodeID string    `json:"node_id"`
	Now    time.Time `json:"now"`
}

func (d *DB) ReleaseNotification(ctx context.Context, id int64, nodeID string, now time.Time) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdReleaseNotification, releaseNotification{ID: id, NodeID: nodeID, Now: now})

	return err
}

func applyReleaseNotification(a *applyCtx, p releaseNotification) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE smpp_notifications SET next_attempt_at = MIN(?, expires_at),
		claimed_by = NULL WHERE id = ? AND result IS NULL AND claimed_by = ?`, p.Now.UTC().UnixNano(), p.ID, p.NodeID)

	return struct{}{}, err
}
