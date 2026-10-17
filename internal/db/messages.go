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

type MessageOrigin string

const (
	OriginMobile MessageOrigin = "mobile"
	OriginAPI    MessageOrigin = "api"
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
	Origin             MessageOrigin
	MessageReference   uint8
	ProtocolIdentifier uint8
	TPDU               []byte
	Status             MessageStatus
	SingleShot         bool
	SubmittedAt        time.Time
	// TimeStamp is the SC time stamp of the message, unique among the messages to its recipient.
	TimeStamp     time.Time
	ExpiresAt     time.Time
	NextAttemptAt time.Time
	Retries       int
	UpdatedAt     time.Time
}

type NewMessage struct {
	Originator         Address
	Recipient          Address
	MSISDN             string
	Origin             MessageOrigin
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
	ids, err := d.CreateMessages(ctx, []NewMessage{m})
	if err != nil {
		return 0, err
	}

	return ids[0], nil
}

func (d *DB) CreateMessages(ctx context.Context, messages []NewMessage) (_ []int64, err error) {
	defer d.observe(&err)()

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("create message: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	ids := make([]int64, 0, len(messages))

	for _, m := range messages {
		id, err := createMessage(ctx, tx, m)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create message: %w", err)
	}

	return ids, nil
}

func createMessage(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	if m.RejectDuplicates {
		duplicate, err := isDuplicate(ctx, tx, m)
		if err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}

		if duplicate {
			return 0, ErrDuplicate
		}
	}

	var (
		id  int64
		err error
	)

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

	return id, nil
}

// arrive records that a message to the recipient arrived at the given time. It returns when the message is first due,
// which is no earlier than the end of a hold on the recipient, and its SC time stamp. TS 23.040 clause 6.2 requires
// the time stamps of the messages to an MS to differ, changed as little as possible, so a message that arrives within
// the second of the previous one to the same recipient takes the next free second.
func arrive(ctx context.Context, tx *sql.Tx, msisdn string, at int64) (next, stamp int64, err error) {
	var held, last sql.Null[int64]

	err = tx.QueryRowContext(ctx, `SELECT held_until, last_time_stamp FROM recipients WHERE msisdn = ?`, msisdn).
		Scan(&held, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}

	next = at
	if held.Valid && held.V > at {
		next = held.V
	}

	second := int64(time.Second)

	stamp = at - at%second
	if last.Valid && last.V >= stamp {
		stamp = last.V + second
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, last_time_stamp, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET last_time_stamp = excluded.last_time_stamp, updated_at = excluded.updated_at`,
		msisdn, stamp, at)
	if err != nil {
		return 0, 0, err
	}

	return next, stamp, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	at := m.SubmittedAt.UTC().UnixNano()

	next, stamp, err := arrive(ctx, tx, m.MSISDN, at)
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi,
		msisdn, origin, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, time_stamp,
		expires_at, next_attempt_at, retries, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN, m.Origin,
		m.MessageReference, m.ProtocolIdentifier, m.TPDU, StatusPending, m.SingleShot, at, stamp,
		m.ExpiresAt.UTC().UnixNano(), next, at)
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

	next, stamp, err := arrive(ctx, tx, m.MSISDN, at)
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE messages SET recipient = ?, recipient_ton = ?, recipient_npi = ?, msisdn = ?, message_reference = ?,
		tpdu = ?, single_shot = ?, submitted_at = ?, time_stamp = ?, expires_at = ?, next_attempt_at = ?, retries = 0,
		updated_at = ?
		WHERE id = ?`,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN, m.MessageReference, m.TPDU,
		m.SingleShot, at, stamp, m.ExpiresAt.UTC().UnixNano(), next, at, id)
	if err != nil {
		return 0, err
	}

	return id, nil
}

func isDuplicate(ctx context.Context, tx *sql.Tx, m NewMessage) (bool, error) {
	var held int

	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE originator = ? AND originator_ton = ? AND originator_npi = ? AND origin = ?
		AND message_reference = ? AND recipient = ? AND recipient_ton = ? AND recipient_npi = ? AND status = ?`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, OriginMobile, m.MessageReference,
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
		AND origin = ? ORDER BY id DESC LIMIT 1`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, OriginMobile).
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
	origin, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, time_stamp, expires_at,
	next_attempt_at, retries, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (Message, error) {
	var (
		m                                                           Message
		submittedAt, timeStamp, expiresAt, nextAttemptAt, updatedAt int64
	)

	err := row.Scan(&m.ID, &m.Originator.Digits, &m.Originator.TypeOfNumber, &m.Originator.NumberingPlan,
		&m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan, &m.MSISDN, &m.Origin,
		&m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status, &m.SingleShot,
		&submittedAt, &timeStamp, &expiresAt, &nextAttemptAt, &m.Retries, &updatedAt)
	if err != nil {
		return Message{}, err
	}

	m.SubmittedAt = time.Unix(0, submittedAt).UTC()
	m.TimeStamp = time.Unix(0, timeStamp).UTC()
	m.ExpiresAt = time.Unix(0, expiresAt).UTC()
	m.NextAttemptAt = time.Unix(0, nextAttemptAt).UTC()
	m.UpdatedAt = time.Unix(0, updatedAt).UTC()

	return m, nil
}

func (d *DB) GetMessage(ctx context.Context, id int64) (_ Message, err error) {
	defer d.observe(&err)()

	m, err := scanMessage(d.conn.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}

	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}

	return m, nil
}

type MessageFilter struct {
	MSISDN     string
	Originator string
	Status     MessageStatus
}

func (d *DB) ListMessages(ctx context.Context, f MessageFilter, page, perPage int) (_ []Message, _ int, err error) {
	defer d.observe(&err)()

	var (
		where []string
		args  []any
	)

	if f.MSISDN != "" {
		where, args = append(where, "msisdn = ?"), append(args, f.MSISDN)
	}

	if f.Originator != "" {
		where, args = append(where, "originator = ?"), append(args, f.Originator)
	}

	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list messages: %w", err)
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list messages: %w", err)
	}

	defer func() { _ = rows.Close() }()

	messages := []Message{}

	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list messages: %w", err)
		}

		messages = append(messages, m)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list messages: %w", err)
	}

	return messages, total, nil
}

func (d *DB) NextDue(ctx context.Context, now time.Time, busy []string) (_ Message, _ bool, err error) {
	defer d.observe(&err)()

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

func (d *DB) NextWakeup(ctx context.Context, busy []string) (_ time.Time, _ bool, err error) {
	defer d.observe(&err)()

	var at sql.Null[int64]

	exclude, args := excludeMSISDNs(busy)

	err = d.conn.QueryRowContext(ctx,
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

// ScheduleRetry schedules the next attempt of a pending message. With hold, it also holds the message's recipient
// until then, in the same transaction: the recipient's other pending messages, and those that arrive meanwhile, wait
// for that attempt, unless an alert ends the hold first.
func (d *DB) ScheduleRetry(ctx context.Context, id int64, at, now time.Time, hold bool) (err error) {
	defer d.observe(&err)()

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	next, updated := at.UTC().UnixNano(), now.UTC().UnixNano()

	var msisdn string

	err = tx.QueryRowContext(ctx,
		`UPDATE messages SET next_attempt_at = ?, retries = retries + 1, updated_at = ? WHERE id = ? AND status = ?
		RETURNING msisdn`,
		next, updated, id, StatusPending).Scan(&msisdn)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}

	if err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	if hold {
		if err := holdRecipient(ctx, tx, msisdn, next, updated); err != nil {
			return fmt.Errorf("schedule retry: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schedule retry: %w", err)
	}

	return nil
}

// CountPending returns how many messages wait for delivery at now: those due, which NextDue returns, and those
// waiting for a later attempt.
func (d *DB) CountPending(ctx context.Context, now time.Time) (due, waiting int, err error) {
	defer d.observe(&err)()

	var total int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(next_attempt_at <= ?), 0), COUNT(*) FROM messages WHERE status = ?`,
		now.UTC().UnixNano(), StatusPending).Scan(&due, &total); err != nil {
		return 0, 0, fmt.Errorf("count pending messages: %w", err)
	}

	return due, total - due, nil
}

// NextDueFor returns the next message to the recipient, other than the given one, that is due and unexpired at now:
// the message that the SC sends the recipient next, in the order NextDue would.
func (d *DB) NextDueFor(ctx context.Context, msisdn string, excludeID int64, now time.Time) (_ Message, _ bool, err error) {
	defer d.observe(&err)()

	at := now.UTC().UnixNano()

	m, err := scanMessage(d.conn.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE msisdn = ? AND status = ? AND next_attempt_at <= ? AND expires_at > ?
		AND id != ? ORDER BY next_attempt_at, id LIMIT 1`, msisdn, StatusPending, at, at, excludeID))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}

	if err != nil {
		return Message{}, false, fmt.Errorf("next due message for a recipient: %w", err)
	}

	return m, true, nil
}

func holdRecipient(ctx context.Context, tx *sql.Tx, msisdn string, until, now int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, held_until, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET held_until = MAX(COALESCE(held_until, 0), excluded.held_until),
		updated_at = excluded.updated_at`,
		msisdn, until, now)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE messages SET next_attempt_at = ?, updated_at = ? WHERE msisdn = ? AND status = ? AND next_attempt_at < ?`,
		until, now, msisdn, StatusPending, until)

	return err
}

func (d *DB) AlertRecipient(ctx context.Context, msisdn string, now time.Time) (_ []string, err error) {
	defer d.observe(&err)()

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

func (d *DB) SetAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string, now time.Time) (err error) {
	defer d.observe(&err)()

	alert := sql.Null[string]{V: alertMSISDN, Valid: alertMSISDN != "" && alertMSISDN != msisdn}

	_, err = d.conn.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, alert_msisdn, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET alert_msisdn = excluded.alert_msisdn, updated_at = excluded.updated_at`,
		msisdn, alert, now.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("set alert MSISDN: %w", err)
	}

	return nil
}

func (d *DB) SetMessageStatus(ctx context.Context, id int64, status MessageStatus, at time.Time) (err error) {
	defer d.observe(&err)()

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
