package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/internal/tracing"
)

func dueMODeliveries(ctx context.Context, q querier, esmeID int64, now time.Time, limit int) ([]Notification, error) {
	at := now.UTC().UnixNano()

	rows, err := q.QueryContext(ctx, `SELECT d.id, d.esme_id, d.attempts, `+prefixed("m.", messageColumns)+`
		FROM esme_deliveries d
		JOIN messages m ON m.id = d.message_id
		WHERE d.esme_id = ? AND d.result IS NULL AND d.next_attempt_at <= ? AND d.expires_at > ?
		ORDER BY d.id LIMIT ?`, esmeID, at, at, limit)
	if err != nil {
		return nil, fmt.Errorf("due MO deliveries: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Notification

	for rows.Next() {
		n, err := scanMODelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("due MO deliveries: %w", err)
		}

		out = append(out, n)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due MO deliveries: %w", err)
	}

	return out, nil
}

func scanMODelivery(rows *sql.Rows) (Notification, error) {
	var (
		n                        Notification
		reportFor, submittedBy   sql.Null[int64]
		reportStatus             sql.Null[uint8]
		traceparent              sql.Null[string]
		submitted, ts, exp, next int64
		updated                  int64
		m                        = &n.Message
	)

	err := rows.Scan(&n.ID, &n.ESMEID, &n.Attempts,
		&m.ID, &m.Kind, &reportFor, &reportStatus, &m.Originator.Digits, &m.Originator.TypeOfNumber,
		&m.Originator.NumberingPlan, &m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan,
		&m.MSISDN, &m.Origin, &submittedBy, &m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status,
		&m.SingleShot, &submitted, &ts, &exp, &next, &m.Retries, &updated, &traceparent)
	if err != nil {
		return Notification{}, err
	}

	n.Type = NotificationMO

	m.ReportFor, m.ReportStatus, m.SubmittedBy, m.TraceParent = reportFor.V, reportStatus.V, submittedBy.V, traceparent.V
	m.SubmittedAt = time.Unix(0, submitted).UTC()
	m.TimeStamp = time.Unix(0, ts).UTC()
	m.ExpiresAt = time.Unix(0, exp).UTC()
	m.NextAttemptAt = time.Unix(0, next).UTC()
	m.UpdatedAt = time.Unix(0, updated).UTC()

	return n, nil
}

type messageClosed struct {
	Kind   MessageKind   `json:"kind"`
	Status MessageStatus `json:"status"`
}

type closeMODelivery struct {
	ID          int64              `json:"id"`
	Result      NotificationResult `json:"result"`
	At          time.Time          `json:"at"`
	TraceParent string             `json:"trace_parent"`
}

func (d *DB) CloseMODelivery(ctx context.Context, id int64, result NotificationResult, at time.Time) (err error) {
	defer d.observe(&err)()

	closed, err := submit[messageClosed](ctx, d, cmdCloseMODelivery, closeMODelivery{
		ID: id, Result: result, At: at, TraceParent: tracing.TraceParent(ctx),
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}

	if err != nil {
		return fmt.Errorf("close MO delivery: %w", err)
	}

	if closed.Status != "" {
		d.metrics.count([]completion{{closed.Kind, closed.Status}})
	}

	return nil
}

func applyCloseMODelivery(a *applyCtx, p closeMODelivery) (messageClosed, error) {
	var (
		status   MessageStatus
		tpStatus tpdu.Status
		outcome  string
	)

	switch p.Result {
	case NotificationAcknowledged:
		status, tpStatus, outcome = StatusDelivered, tpdu.StatusReceived, "esme_acknowledged"
	case NotificationRejected:
		status, tpStatus, outcome = StatusFailed, tpdu.StatusConnectionRejected, "esme_rejected"
	case NotificationNoUserData:
		status, tpStatus, outcome = StatusFailed, tpdu.StatusNoInterworking, "esme_undecodable"
	default:
		return messageClosed{}, fmt.Errorf("%w: %q is not a close result of an MO delivery", ErrInvalid, p.Result)
	}

	res, err := a.tx.ExecContext(a.ctx, `UPDATE esme_deliveries SET result = ?, claimed_by = NULL
		WHERE id = ? AND result IS NULL`, p.Result, p.ID)
	if err != nil {
		return messageClosed{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return messageClosed{}, err
	} else if n == 0 {
		return messageClosed{}, ErrNotFound
	}

	var (
		messageID int64
		esmeID    int64
	)

	if err := a.tx.QueryRowContext(a.ctx, `SELECT message_id, esme_id FROM esme_deliveries WHERE id = ?`,
		p.ID).Scan(&messageID, &esmeID); err != nil {
		return messageClosed{}, err
	}

	return completeDelegated(a, messageID, esmeID, status, tpStatus, outcome, p.At, p.TraceParent)
}

func completeDelegated(a *applyCtx, messageID, esmeID int64, status MessageStatus, tpStatus tpdu.Status, outcome string,
	at time.Time, traceparent string,
) (messageClosed, error) {
	ctx, tx := a.ctx, a.tx

	var systemID string

	err := tx.QueryRowContext(ctx, `SELECT system_id FROM esmes WHERE id = ?`, esmeID).Scan(&systemID)
	if errors.Is(err, sql.ErrNoRows) {
		return messageClosed{}, ErrNotFound
	}

	if err != nil {
		return messageClosed{}, err
	}

	res, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		status, at.UTC().UnixNano(), messageID, StatusDelegated)
	if err != nil {
		return messageClosed{}, err
	}

	if n, err := res.RowsAffected(); err != nil {
		return messageClosed{}, err
	} else if n == 0 {
		return messageClosed{}, nil
	}

	if err := insertAttempts(a, []DeliveryAttempt{{
		MessageID: messageID, StartedAt: at, CompletedAt: at,
		Step: StepDelivery, Node: systemID, Outcome: outcome,
	}}); err != nil {
		return messageClosed{}, err
	}

	m, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, messageID))
	if err != nil {
		return messageClosed{}, err
	}

	serviceCentre, validity, err := reportSettings(ctx, tx)
	if err != nil {
		return messageClosed{}, err
	}

	if report, err := StatusReportFor(m, tpStatus, at, serviceCentre, validity); err == nil && report != nil {
		if err := insertStatusReport(ctx, tx, messageID, *report, traceparent); err != nil {
			return messageClosed{}, err
		}
	}

	queued, err := queueNotifications(ctx, tx, messageID, status, uint8(tpStatus), at.UTC().UnixNano())
	if err != nil {
		return messageClosed{}, err
	}

	a.events.Messages = true
	a.events.Notifications = a.events.Notifications || queued

	return messageClosed{Kind: KindMessage, Status: status}, nil
}

type retryMODelivery struct {
	ID     int64     `json:"id"`
	Next   time.Time `json:"next"`
	NodeID string    `json:"node_id"`
}

func (d *DB) RetryMODelivery(ctx context.Context, id int64, next time.Time, nodeID string) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdRetryMODelivery, retryMODelivery{ID: id, Next: next, NodeID: nodeID})
	if err != nil {
		return fmt.Errorf("retry MO delivery: %w", err)
	}

	return nil
}

func applyRetryMODelivery(a *applyCtx, p retryMODelivery) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE esme_deliveries SET attempts = attempts + 1, next_attempt_at = ?,
		claimed_by = NULL WHERE id = ? AND result IS NULL AND (claimed_by IS NULL OR claimed_by = ?)`,
		p.Next.UTC().UnixNano(), p.ID, p.NodeID)

	return struct{}{}, err
}

type releaseMODelivery struct {
	ID     int64     `json:"id"`
	NodeID string    `json:"node_id"`
	Now    time.Time `json:"now"`
}

func (d *DB) ReleaseMODelivery(ctx context.Context, id int64, nodeID string, now time.Time) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdReleaseMODelivery, releaseMODelivery{ID: id, NodeID: nodeID, Now: now})
	if err != nil {
		return fmt.Errorf("release MO delivery: %w", err)
	}

	return nil
}

func applyReleaseMODelivery(a *applyCtx, p releaseMODelivery) (struct{}, error) {
	_, err := a.tx.ExecContext(a.ctx, `UPDATE esme_deliveries SET next_attempt_at = MIN(?, expires_at),
		claimed_by = NULL WHERE id = ? AND result IS NULL AND claimed_by = ?`, p.Now.UTC().UnixNano(), p.ID, p.NodeID)

	return struct{}{}, err
}

type expireMODeliveries struct {
	Now time.Time `json:"now"`
}

func (d *DB) ExpireMODeliveries(ctx context.Context, now time.Time) (_ int, err error) {
	defer d.observe(&err)()

	n, err := submit[int](ctx, d, cmdExpireMODeliveries, expireMODeliveries{Now: now})
	if err != nil {
		return 0, fmt.Errorf("expire MO deliveries: %w", err)
	}

	d.metrics.count(completions(KindMessage, StatusExpired, n))

	return n, nil
}

func applyExpireMODeliveries(a *applyCtx, p expireMODeliveries) (int, error) {
	rows, err := a.tx.QueryContext(a.ctx, `UPDATE esme_deliveries SET result = ?, claimed_by = NULL
		WHERE result IS NULL AND expires_at <= ? RETURNING id, message_id, esme_id`, NotificationExpired,
		p.Now.UTC().UnixNano())
	if err != nil {
		return 0, err
	}

	defer func() { _ = rows.Close() }()

	closed := 0

	for rows.Next() {
		var (
			id        int64
			messageID int64
			esmeID    int64
		)

		if err := rows.Scan(&id, &messageID, &esmeID); err != nil {
			return closed, err
		}

		done, err := completeDelegated(a, messageID, esmeID, StatusExpired, tpdu.StatusValidityPeriodExpired,
			"esme_expired", p.Now, "")
		if err != nil {
			return closed, err
		}

		if done.Status != "" {
			closed++
		}
	}

	return closed, rows.Err()
}
