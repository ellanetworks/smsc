package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MessageStatus string

const (
	StatusPending   MessageStatus = "pending"
	StatusDelivered MessageStatus = "delivered"
	StatusFailed    MessageStatus = "failed"
	StatusExpired   MessageStatus = "expired"
)

var ErrDuplicate = errors.New("duplicate message")

type Address struct {
	Digits        string
	TypeOfNumber  uint8
	NumberingPlan uint8
}

type Message struct {
	ID                 int64
	Originator         Address
	Recipient          Address
	MSISDN             string
	MessageReference   uint8
	ProtocolIdentifier uint8
	TPDU               []byte
	Status             MessageStatus
	SingleShot         bool
	SubmittedAt        time.Time
	ExpiresAt          time.Time
	NextAttemptAt      time.Time
	Retries            int
	UpdatedAt          time.Time
}

type NewMessage struct {
	Originator         Address
	Recipient          Address
	MSISDN             string
	MessageReference   uint8
	ProtocolIdentifier uint8
	RejectDuplicates   bool
	Replace            bool
	TPDU               []byte
	SingleShot         bool
	SubmittedAt        time.Time
	ExpiresAt          time.Time
}

func (d *DB) CreateMessage(ctx context.Context, m NewMessage) (int64, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if m.RejectDuplicates {
		duplicate, err := isDuplicate(ctx, tx, m)
		if err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}

		if duplicate {
			return 0, ErrDuplicate
		}
	}

	var id int64

	if m.Replace {
		id, err = replacePending(ctx, tx, m)
		if err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}
	}

	if id == 0 {
		id, err = insertMessage(ctx, tx, m)
		if err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}

	return id, nil
}

func heldUntil(ctx context.Context, tx *sql.Tx, msisdn string, at int64) (int64, error) {
	var held sql.Null[int64]

	err := tx.QueryRowContext(ctx, `SELECT held_until FROM recipients WHERE msisdn = ?`, msisdn).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return at, nil
	}

	if err != nil {
		return 0, err
	}

	if held.Valid && held.V > at {
		return held.V, nil
	}

	return at, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	at := m.SubmittedAt.UTC().UnixNano()

	next, err := heldUntil(ctx, tx, m.MSISDN, at)
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi,
		msisdn, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, expires_at,
		next_attempt_at, retries, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN,
		m.MessageReference, m.ProtocolIdentifier, m.TPDU, StatusPending, m.SingleShot, at, m.ExpiresAt.UTC().UnixNano(), next, at)
	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func replacePending(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	var id int64

	err := tx.QueryRowContext(ctx,
		`SELECT id FROM messages WHERE originator = ? AND originator_ton = ? AND originator_npi = ?
		AND protocol_identifier = ? AND status = ? ORDER BY id DESC LIMIT 1`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, m.ProtocolIdentifier, StatusPending).
		Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	at := m.SubmittedAt.UTC().UnixNano()

	next, err := heldUntil(ctx, tx, m.MSISDN, at)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE messages SET recipient = ?, recipient_ton = ?, recipient_npi = ?, msisdn = ?, message_reference = ?,
		tpdu = ?, single_shot = ?, submitted_at = ?, expires_at = ?, next_attempt_at = ?, retries = 0, updated_at = ?
		WHERE id = ?`,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN, m.MessageReference, m.TPDU,
		m.SingleShot, at, m.ExpiresAt.UTC().UnixNano(), next, at, id)
	if err != nil {
		return 0, err
	}

	return id, nil
}

func isDuplicate(ctx context.Context, tx *sql.Tx, m NewMessage) (bool, error) {
	var held int

	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE originator = ? AND originator_ton = ? AND originator_npi = ?
		AND message_reference = ? AND recipient = ? AND recipient_ton = ? AND recipient_npi = ? AND status = ?`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, m.MessageReference,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, StatusPending).
		Scan(&held)
	if err != nil {
		return false, err
	}

	if held > 0 {
		return true, nil
	}

	var previous uint8

	err = tx.QueryRowContext(ctx,
		`SELECT message_reference FROM messages WHERE originator = ? AND originator_ton = ? AND originator_npi = ?
		ORDER BY id DESC LIMIT 1`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan).
		Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return previous == m.MessageReference, nil
}

const messageColumns = `id, originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi, msisdn,
	message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, expires_at, next_attempt_at,
	retries, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (Message, error) {
	var (
		m                                                Message
		submittedAt, expiresAt, nextAttemptAt, updatedAt int64
	)

	err := row.Scan(&m.ID, &m.Originator.Digits, &m.Originator.TypeOfNumber, &m.Originator.NumberingPlan,
		&m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan, &m.MSISDN,
		&m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status, &m.SingleShot,
		&submittedAt, &expiresAt, &nextAttemptAt, &m.Retries, &updatedAt)
	if err != nil {
		return Message{}, err
	}

	m.SubmittedAt = time.Unix(0, submittedAt).UTC()
	m.ExpiresAt = time.Unix(0, expiresAt).UTC()
	m.NextAttemptAt = time.Unix(0, nextAttemptAt).UTC()
	m.UpdatedAt = time.Unix(0, updatedAt).UTC()

	return m, nil
}

func (d *DB) GetMessage(ctx context.Context, id int64) (Message, error) {
	m, err := scanMessage(d.conn.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}

	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}

	return m, nil
}

func (d *DB) NextDue(ctx context.Context, now time.Time, busy []string) (Message, bool, error) {
	exclude, args := excludeMSISDNs(busy)

	m, err := scanMessage(d.conn.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE status = ? AND next_attempt_at <= ?`+exclude+`
		ORDER BY next_attempt_at, id LIMIT 1`, append([]any{StatusPending, now.UTC().UnixNano()}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}

	if err != nil {
		return Message{}, false, fmt.Errorf("next due message: %w", err)
	}

	return m, true, nil
}

func (d *DB) NextWakeup(ctx context.Context, busy []string) (time.Time, bool, error) {
	var at sql.Null[int64]

	exclude, args := excludeMSISDNs(busy)

	err := d.conn.QueryRowContext(ctx,
		`SELECT MIN(next_attempt_at) FROM messages WHERE status = ?`+exclude,
		append([]any{StatusPending}, args...)...).Scan(&at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next wakeup: %w", err)
	}

	if !at.Valid {
		return time.Time{}, false, nil
	}

	return time.Unix(0, at.V).UTC(), true, nil
}

func excludeMSISDNs(busy []string) (string, []any) {
	if len(busy) == 0 {
		return "", nil
	}

	args := make([]any, len(busy))
	for i, msisdn := range busy {
		args[i] = msisdn
	}

	return ` AND msisdn NOT IN (?` + strings.Repeat(`, ?`, len(busy)-1) + `)`, args
}

func (d *DB) ScheduleRetry(ctx context.Context, id int64, at, now time.Time) error {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE messages SET next_attempt_at = ?, retries = retries + 1, updated_at = ? WHERE id = ? AND status = ?`,
		at.UTC().UnixNano(), now.UTC().UnixNano(), id, StatusPending)
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

func (d *DB) CountPendingFor(ctx context.Context, msisdn string, excludeID int64) (int, error) {
	var n int

	err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE msisdn = ? AND status = ? AND id != ?`,
		msisdn, StatusPending, excludeID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending messages: %w", err)
	}

	return n, nil
}

func (d *DB) HoldRecipient(ctx context.Context, msisdn string, until, now time.Time) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hold recipient: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	at, updated := until.UTC().UnixNano(), now.UTC().UnixNano()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, held_until, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET held_until = MAX(COALESCE(held_until, 0), excluded.held_until),
		updated_at = excluded.updated_at`,
		msisdn, at, updated)
	if err != nil {
		return fmt.Errorf("hold recipient: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE messages SET next_attempt_at = ?, updated_at = ? WHERE msisdn = ? AND status = ? AND next_attempt_at < ?`,
		at, updated, msisdn, StatusPending, at)
	if err != nil {
		return fmt.Errorf("hold recipient: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hold recipient: %w", err)
	}

	return nil
}

func (d *DB) AlertRecipient(ctx context.Context, msisdn string, now time.Time) ([]string, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	recipients := []string{msisdn}

	rows, err := tx.QueryContext(ctx, `SELECT msisdn FROM recipients WHERE alert_msisdn = ? AND msisdn != ?`, msisdn, msisdn)
	if err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("alert recipient: %w", err)
		}

		recipients = append(recipients, r)
	}

	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	at := now.UTC().UnixNano()

	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx,
			`UPDATE recipients SET held_until = NULL, updated_at = ? WHERE msisdn = ?`, at, r); err != nil {
			return nil, fmt.Errorf("alert recipient: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE messages SET next_attempt_at = ?, updated_at = ? WHERE msisdn = ? AND status = ? AND next_attempt_at > ?`,
			at, at, r, StatusPending, at); err != nil {
			return nil, fmt.Errorf("alert recipient: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	return recipients, nil
}

func (d *DB) SetAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string, now time.Time) error {
	alert := sql.Null[string]{V: alertMSISDN, Valid: alertMSISDN != "" && alertMSISDN != msisdn}

	_, err := d.conn.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, alert_msisdn, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET alert_msisdn = excluded.alert_msisdn, updated_at = excluded.updated_at`,
		msisdn, alert, now.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("set alert MSISDN: %w", err)
	}

	return nil
}

func (d *DB) SetMessageStatus(ctx context.Context, id int64, status MessageStatus, at time.Time) error {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`, status, at.UTC().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("set message status: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set message status: %w", err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}
