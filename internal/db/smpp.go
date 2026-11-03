package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type NotificationType string

const (
	NotificationReceipt   NotificationType = "receipt"
	NotificationSIMAnswer NotificationType = "sim_answer"
)

type NotificationResult string

const (
	NotificationAcknowledged NotificationResult = "acknowledged"
	NotificationRejected     NotificationResult = "rejected"
	NotificationExpired      NotificationResult = "expired"
	NotificationNoUserData   NotificationResult = "no_user_data"
)

// SMPP 3.4 section 5.2.28 table 5-6
const (
	messageStateDelivered     = 2
	messageStateExpired       = 3
	messageStateDeleted       = 4
	messageStateUndeliverable = 5
)

// SMPP 3.4 section 5.2.28
func messageState(status MessageStatus) uint8 {
	switch status {
	case StatusDelivered:
		return messageStateDelivered
	case StatusExpired:
		return messageStateExpired
	case StatusReplaced:
		return messageStateDeleted
	default:
		return messageStateUndeliverable
	}
}

func queueNotifications(ctx context.Context, tx *sql.Tx, messageID int64, status MessageStatus, tpStatus uint8,
	at int64,
) (bool, error) {
	var (
		receipt   uint8
		simAnswer bool
		queued    bool
	)

	err := tx.QueryRowContext(ctx, `SELECT receipt, sim_answer FROM smpp_messages WHERE message_id = ?`,
		messageID).Scan(&receipt, &simAnswer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	var validity int64
	if err := tx.QueryRowContext(ctx, `SELECT default_validity FROM delivery WHERE id = 1`).Scan(&validity); err != nil {
		return false, err
	}

	expiresAt := at + validity

	if simAnswer && status != StatusReplaced {
		var (
			attemptID int64
			report    []byte
		)

		err := tx.QueryRowContext(ctx, `SELECT id, deliver_report FROM delivery_attempts WHERE message_id = ?
			ORDER BY id DESC LIMIT 1`, messageID).Scan(&attemptID, &report)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}

		if err == nil && len(report) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO smpp_notifications (message_id, type, attempt_id, expires_at,
				next_attempt_at, attempts) VALUES (?, ?, ?, ?, ?, 0)`,
				messageID, NotificationSIMAnswer, attemptID, expiresAt, at); err != nil {
				return false, err
			}

			queued = true
		}
	}

	if receipt == ReceiptFinal || receipt == ReceiptFailure && status != StatusDelivered {
		if _, err := tx.ExecContext(ctx, `INSERT INTO smpp_notifications (message_id, type, tp_status, expires_at,
			next_attempt_at, attempts) VALUES (?, ?, ?, ?, ?, 0)`,
			messageID, NotificationReceipt, tpStatus, expiresAt, at); err != nil {
			return false, err
		}

		queued = true
	}

	return queued, nil
}

func (d *DB) OnNotificationsQueued(f func()) {
	d.queued = f
}

func (d *DB) notificationsQueued(queued bool) {
	if queued && d.queued != nil {
		d.queued()
	}
}

type Notification struct {
	ID                   int64
	ESMEID               int64
	Type                 NotificationType
	Attempts             int
	MessageState         uint8
	TPStatus             uint8
	Message              Message
	SourceNPI            uint8
	DataCoding           uint8
	UserMessageReference *uint16
	DeliverReport        []byte
	Ack                  bool
}

const notificationColumns = `n.id, s.esme_id, n.type, n.attempts, n.tp_status, s.source_npi,
	s.data_coding, s.user_message_reference, a.deliver_report, a.outcome`

func (d *DB) DueNotifications(ctx context.Context, esmeID int64, now time.Time, exclude []int64, limit int,
) (_ []Notification, err error) {
	defer d.observe(&err)()

	at := now.UTC().UnixNano()
	args := []any{esmeID, at, at}

	notIn := ""
	if len(exclude) > 0 {
		notIn = ` AND n.id NOT IN (?` + strings.Repeat(`, ?`, len(exclude)-1) + `)`

		for _, id := range exclude {
			args = append(args, id)
		}
	}

	args = append(args, limit)

	rows, err := d.conn.QueryContext(ctx, `SELECT `+notificationColumns+`, `+prefixed("m.", messageColumns)+`
		FROM smpp_notifications n
		CROSS JOIN smpp_messages s ON s.message_id = n.message_id
		JOIN messages m ON m.id = n.message_id
		LEFT JOIN delivery_attempts a ON a.id = n.attempt_id
		WHERE s.esme_id = ? AND n.result IS NULL AND n.next_attempt_at <= ? AND n.expires_at > ?
		AND NOT EXISTS (SELECT 1 FROM smpp_notifications o WHERE o.message_id = n.message_id AND o.id < n.id
			AND o.result IS NULL)`+notIn+`
		ORDER BY n.id LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("due notifications: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Notification

	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("due notifications: %w", err)
		}

		out = append(out, n)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("due notifications: %w", err)
	}

	return out, nil
}

func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}

	return strings.Join(parts, ", ")
}

func scanNotification(rows *sql.Rows) (Notification, error) {
	var (
		n                        Notification
		status                   sql.Null[uint8]
		reference                sql.Null[uint16]
		outcome                  sql.Null[string]
		reportFor                sql.Null[int64]
		reportStatus             sql.Null[uint8]
		submittedBy              sql.Null[int64]
		traceparent              sql.Null[string]
		submitted, ts, exp, next int64
		updated                  int64
		m                        = &n.Message
	)

	err := rows.Scan(&n.ID, &n.ESMEID, &n.Type, &n.Attempts, &status, &n.SourceNPI, &n.DataCoding, &reference,
		&n.DeliverReport, &outcome,
		&m.ID, &m.Kind, &reportFor, &reportStatus, &m.Originator.Digits, &m.Originator.TypeOfNumber,
		&m.Originator.NumberingPlan, &m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan,
		&m.MSISDN, &m.Origin, &submittedBy, &m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status,
		&m.SingleShot, &submitted, &ts, &exp, &next, &m.Retries, &updated, &traceparent)
	if err != nil {
		return Notification{}, err
	}

	n.TPStatus = status.V
	n.Ack = outcome.V == "success"

	if reference.Valid {
		n.UserMessageReference = &reference.V
	}

	m.ReportFor, m.ReportStatus, m.SubmittedBy, m.TraceParent = reportFor.V, reportStatus.V, submittedBy.V, traceparent.V
	m.SubmittedAt = time.Unix(0, submitted).UTC()
	m.TimeStamp = time.Unix(0, ts).UTC()
	m.ExpiresAt = time.Unix(0, exp).UTC()
	m.NextAttemptAt = time.Unix(0, next).UTC()
	m.UpdatedAt = time.Unix(0, updated).UTC()

	if n.Type == NotificationReceipt {
		n.MessageState = messageState(m.Status)
	}

	return n, nil
}

func (d *DB) CloseNotification(ctx context.Context, id int64, result NotificationResult) (err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `UPDATE smpp_notifications SET result = ? WHERE id = ? AND result IS NULL`,
		result, id)
	if err != nil {
		return fmt.Errorf("close notification: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("close notification: %w", err)
	} else if n == 0 {
		return fmt.Errorf("close notification: %w", ErrNotFound)
	}

	return nil
}

func (d *DB) RetryNotification(ctx context.Context, id int64, next time.Time) (err error) {
	defer d.observe(&err)()

	_, err = d.conn.ExecContext(ctx, `UPDATE smpp_notifications SET attempts = attempts + 1, next_attempt_at = ?
		WHERE id = ? AND result IS NULL`, next.UTC().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("retry notification: %w", err)
	}

	return nil
}

func (d *DB) ExpireNotifications(ctx context.Context, now time.Time) (_ map[NotificationType]int, err error) {
	defer d.observe(&err)()

	at := now.UTC().UnixNano()

	rows, err := d.conn.QueryContext(ctx, `UPDATE smpp_notifications SET result = ?
		WHERE result IS NULL AND expires_at <= ? RETURNING type`, NotificationExpired, at)
	if err != nil {
		return nil, fmt.Errorf("expire notifications: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := map[NotificationType]int{}

	for rows.Next() {
		var t NotificationType
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("expire notifications: %w", err)
		}

		out[t]++
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("expire notifications: %w", err)
	}

	return out, nil
}

func (d *DB) CountPendingNotifications(ctx context.Context, now time.Time) (_ map[NotificationType]int, err error) {
	defer d.observe(&err)()

	rows, err := d.conn.QueryContext(ctx, `SELECT type, COUNT(*) FROM smpp_notifications
		WHERE result IS NULL AND expires_at > ? GROUP BY type`, now.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("count pending notifications: %w", err)
	}

	defer func() { _ = rows.Close() }()

	out := map[NotificationType]int{NotificationReceipt: 0, NotificationSIMAnswer: 0}

	for rows.Next() {
		var (
			t NotificationType
			n int
		)

		if err := rows.Scan(&t, &n); err != nil {
			return nil, fmt.Errorf("count pending notifications: %w", err)
		}

		out[t] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count pending notifications: %w", err)
	}

	return out, nil
}

func (d *DB) NextNotificationAt(ctx context.Context, esmeID int64, now time.Time) (_ time.Time, _ bool, err error) {
	defer d.observe(&err)()

	var at sql.Null[int64]

	if err := d.conn.QueryRowContext(ctx, `SELECT MIN(CASE WHEN n.next_attempt_at > ? THEN MIN(n.next_attempt_at,
		n.expires_at) ELSE n.expires_at END) FROM smpp_notifications n CROSS JOIN smpp_messages s ON s.message_id = n.message_id
		WHERE s.esme_id = ? AND n.result IS NULL`,
		now.UTC().UnixNano(), esmeID).Scan(&at); err != nil {
		return time.Time{}, false, fmt.Errorf("next notification: %w", err)
	}

	if !at.Valid {
		return time.Time{}, false, nil
	}

	return time.Unix(0, at.V).UTC(), true, nil
}

func (d *DB) SystemIDsFor(ctx context.Context, messageIDs []int64) (_ map[int64]string, err error) {
	defer d.observe(&err)()

	out := map[int64]string{}
	if len(messageIDs) == 0 {
		return out, nil
	}

	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		args[i] = id
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT s.message_id, e.system_id FROM smpp_messages s
		JOIN esmes e ON e.id = s.esme_id WHERE s.message_id IN (?`+strings.Repeat(`, ?`, len(messageIDs)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get system IDs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id       int64
			systemID string
		)

		if err := rows.Scan(&id, &systemID); err != nil {
			return nil, fmt.Errorf("get system IDs: %w", err)
		}

		out[id] = systemID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get system IDs: %w", err)
	}

	return out, nil
}
