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
	// StatusReplaced is the status of a message that a later one replaced before its delivery (TS 23.040 clause 6.2).
	StatusReplaced MessageStatus = "replaced"
)

// MessageKind tells what the SC delivers: a short message, or an SMS-STATUS-REPORT on one to its originator, which
// the SC delivers as it does a short message (TS 23.040 clause 3.2.9).
type MessageKind string

const (
	KindMessage      MessageKind = "message"
	KindStatusReport MessageKind = "status_report"
)

type MessageOrigin string

const (
	OriginMobile MessageOrigin = "mobile"
	OriginAPI    MessageOrigin = "api"
)

var (
	ErrDuplicate  = errors.New("duplicate message")
	ErrNotPending = errors.New("message is no longer pending")
)

type Address struct {
	Digits        string
	TypeOfNumber  uint8
	NumberingPlan uint8
}

type Message struct {
	ID   int64
	Kind MessageKind
	// ReportFor is the message that a status report is on, and ReportStatus its TP-Status. Both are zero for a message.
	ReportFor    int64
	ReportStatus uint8
	Originator   Address
	Recipient    Address
	MSISDN       string
	Origin       MessageOrigin
	// SubmittedBy is the user that submitted a message over the API, or zero.
	SubmittedBy        int64
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
	Originator Address
	Recipient  Address
	MSISDN     string
	Origin     MessageOrigin
	// SubmittedBy is the user that submits a message over the API, or zero.
	SubmittedBy        int64
	MessageReference   uint8
	ProtocolIdentifier uint8
	RejectDuplicates   bool
	Replace            bool
	TPDU               []byte
	SingleShot         bool
	SubmittedAt        time.Time
	ExpiresAt          time.Time
	// ReportReplaced returns the status report on the message that this one replaces, or nil when there is none to
	// send. The report is stored with the replacement. It runs in the transaction that stores them, so it must not use
	// the database.
	ReportReplaced func(replaced Message) (*NewStatusReport, error)
}

// NewStatusReport is an SMS-STATUS-REPORT on a message from an MS, to that MS.
type NewStatusReport struct {
	// Originator is the address that the report shows as its sender: the SC's.
	Originator Address
	Status     uint8
	// TPDU is the SMS-STATUS-REPORT, of which the SC sets TP-MMS when it sends it.
	TPDU      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
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

	var completed []completion

	for _, m := range messages {
		id, err := createMessage(ctx, tx, m, &completed)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create message: %w", err)
	}

	d.metrics.count(completed)

	return ids, nil
}

func createMessage(ctx context.Context, tx *sql.Tx, m NewMessage, completed *[]completion) (int64, error) {
	if m.RejectDuplicates {
		duplicate, err := isDuplicate(ctx, tx, m)
		if err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}

		if duplicate {
			return 0, ErrDuplicate
		}
	}

	if m.Replace {
		if err := replacePending(ctx, tx, m, completed); err != nil {
			return 0, fmt.Errorf("create message: %w", err)
		}
	}

	id, err := insertMessage(ctx, tx, m)
	if err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}

	return id, nil
}

// due returns when a message to the recipient that arrives at the given time is first due: no earlier than the end of
// a hold on the recipient, and no later than the message expires.
func due(ctx context.Context, tx *sql.Tx, msisdn string, at, expiresAt int64) (int64, error) {
	var held sql.Null[int64]

	err := tx.QueryRowContext(ctx, `SELECT held_until FROM recipients WHERE msisdn = ?`, msisdn).Scan(&held)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	next := at
	if held.Valid && held.V > at {
		next = held.V
	}

	return min(next, expiresAt), nil
}

// stamp records that a short message to the recipient arrived at the given time, and returns its SC time stamp. TS
// 23.040 clause 6.2 requires the time stamps of the messages to an MS to differ, changed as little as possible, so a
// message that arrives within the second of the previous one to the same recipient takes the next free second.
func stamp(ctx context.Context, tx *sql.Tx, msisdn string, at int64) (int64, error) {
	var last sql.Null[int64]

	err := tx.QueryRowContext(ctx, `SELECT last_time_stamp FROM recipients WHERE msisdn = ?`, msisdn).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	second := int64(time.Second)

	s := at - at%second
	if last.Valid && last.V >= s {
		s = last.V + second
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO recipients (msisdn, last_time_stamp, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET last_time_stamp = excluded.last_time_stamp, updated_at = excluded.updated_at`,
		msisdn, s, at)
	if err != nil {
		return 0, err
	}

	return s, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	at, expiresAt := m.SubmittedAt.UTC().UnixNano(), m.ExpiresAt.UTC().UnixNano()

	next, err := due(ctx, tx, m.MSISDN, at, expiresAt)
	if err != nil {
		return 0, err
	}

	timeStamp, err := stamp(ctx, tx, m.MSISDN, at)
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (kind, originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi,
		msisdn, origin, submitted_by, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at,
		time_stamp, expires_at, next_attempt_at, retries, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN, m.Origin,
		sql.Null[int64]{V: m.SubmittedBy, Valid: m.SubmittedBy != 0}, m.MessageReference, m.ProtocolIdentifier, m.TPDU, StatusPending, m.SingleShot, at, timeStamp,
		expiresAt, next, at)
	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

// insertStatusReport stores a status report on a message from an MS, to be delivered to that MS. A status report
// carries the time stamp of the message it is on, so it takes none of its recipient's time stamps.
func insertStatusReport(ctx context.Context, tx *sql.Tx, messageID int64, r NewStatusReport) error {
	var (
		recipient   Address
		reference   uint8
		submittedAt int64
	)

	err := tx.QueryRowContext(ctx,
		`SELECT originator, originator_ton, originator_npi, message_reference, submitted_at FROM messages
		WHERE id = ? AND kind = ? AND origin = ?`, messageID, KindMessage, OriginMobile).
		Scan(&recipient.Digits, &recipient.TypeOfNumber, &recipient.NumberingPlan, &reference, &submittedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("status report on message %d: %w: no message from an MS", messageID, ErrNotFound)
	}

	if err != nil {
		return err
	}

	at, expiresAt := r.CreatedAt.UTC().UnixNano(), r.ExpiresAt.UTC().UnixNano()

	// The originator of a message from an MS is the MSISDN of the MS, in international format.
	next, err := due(ctx, tx, recipient.Digits, at, expiresAt)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO messages (kind, report_for, report_status, originator, originator_ton, originator_npi, recipient,
		recipient_ton, recipient_npi, msisdn, origin, message_reference, protocol_identifier, tpdu, status, single_shot,
		submitted_at, time_stamp, expires_at, next_attempt_at, retries, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 0, ?, ?, ?, ?, 0, ?)`,
		KindStatusReport, messageID, r.Status, r.Originator.Digits, r.Originator.TypeOfNumber, r.Originator.NumberingPlan,
		recipient.Digits, recipient.TypeOfNumber, recipient.NumberingPlan, recipient.Digits, OriginMobile, reference,
		r.TPDU, StatusPending, at, submittedAt, expiresAt, next, at)

	return err
}

// replacePending marks replaced the pending message that the new one replaces, if any: the latest from the same
// originator, of the same replace type, whatever its recipient (TS 23.040 clause 9.2.3.9: "In MO short messages the SC
// reacts similarly but only the address of the originating MS or any other source is checked"). It stores the status
// report on it, if any.
func replacePending(ctx context.Context, tx *sql.Tx, m NewMessage, completed *[]completion) error {
	replaced, err := scanMessage(tx.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE kind = ? AND originator = ? AND originator_ton = ?
		AND originator_npi = ? AND protocol_identifier = ? AND status = ? ORDER BY id DESC LIMIT 1`,
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.ProtocolIdentifier, StatusPending))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`,
		StatusReplaced, m.SubmittedAt.UTC().UnixNano(), replaced.ID); err != nil {
		return err
	}

	replaced.Status = StatusReplaced
	*completed = append(*completed, completion{KindMessage, StatusReplaced})

	if m.ReportReplaced == nil {
		return nil
	}

	report, err := m.ReportReplaced(replaced)
	if err != nil || report == nil {
		return err
	}

	return insertStatusReport(ctx, tx, replaced.ID, *report)
}

func isDuplicate(ctx context.Context, tx *sql.Tx, m NewMessage) (bool, error) {
	var held int

	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE kind = ? AND originator = ? AND originator_ton = ? AND originator_npi = ?
		AND origin = ? AND message_reference = ? AND recipient = ? AND recipient_ton = ? AND recipient_npi = ? AND status = ?`,
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, OriginMobile, m.MessageReference,
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
		`SELECT message_reference FROM messages WHERE kind = ? AND originator = ? AND originator_ton = ?
		AND originator_npi = ? AND origin = ? ORDER BY id DESC LIMIT 1`,
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan, OriginMobile).
		Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return previous == m.MessageReference, nil
}

const messageColumns = `id, kind, report_for, report_status, originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi, msisdn,
	origin, submitted_by, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, time_stamp, expires_at,
	next_attempt_at, retries, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (Message, error) {
	var (
		m                                                           Message
		reportFor                                                   sql.Null[int64]
		reportStatus                                                sql.Null[uint8]
		submittedBy                                                 sql.Null[int64]
		submittedAt, timeStamp, expiresAt, nextAttemptAt, updatedAt int64
	)

	err := row.Scan(&m.ID, &m.Kind, &reportFor, &reportStatus, &m.Originator.Digits, &m.Originator.TypeOfNumber, &m.Originator.NumberingPlan,
		&m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan, &m.MSISDN, &m.Origin, &submittedBy,
		&m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status, &m.SingleShot,
		&submittedAt, &timeStamp, &expiresAt, &nextAttemptAt, &m.Retries, &updatedAt)
	if err != nil {
		return Message{}, err
	}

	m.ReportFor, m.ReportStatus, m.SubmittedBy = reportFor.V, reportStatus.V, submittedBy.V
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
	// SubmittedBy, when not zero, keeps only the messages that user submitted.
	SubmittedBy int64
	// Start and End, when not zero, keep only the messages submitted at or after Start, and before End.
	Start, End time.Time
}

func (d *DB) ListMessages(ctx context.Context, f MessageFilter, page, perPage int) (_ []Message, _ int, err error) {
	defer d.observe(&err)()

	// Status reports show with the message they are on.
	where, args := []string{"kind = ?"}, []any{KindMessage}

	if f.MSISDN != "" {
		where, args = append(where, "msisdn = ?"), append(args, f.MSISDN)
	}

	if f.Originator != "" {
		where, args = append(where, "originator = ?"), append(args, f.Originator)
	}

	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}

	if f.SubmittedBy != 0 {
		where, args = append(where, "submitted_by = ?"), append(args, f.SubmittedBy)
	}

	if !f.Start.IsZero() {
		where, args = append(where, "submitted_at >= ?"), append(args, f.Start.UTC().UnixNano())
	}

	if !f.End.IsZero() {
		where, args = append(where, "submitted_at < ?"), append(args, f.End.UTC().UnixNano())
	}

	clause := " WHERE " + strings.Join(where, " AND ")

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
// for that attempt, unless an alert ends the hold first. A message is never due later than it expires, so that it ends
// expired on time rather than at an attempt after its validity period.
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
		`UPDATE messages SET next_attempt_at = MIN(?, expires_at), retries = retries + 1, updated_at = ?
		WHERE id = ? AND status = ?
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

// PendingCount is how many messages of a kind wait for delivery: those due, which NextDue returns, and those waiting
// for a later attempt.
type PendingCount struct {
	Due     int
	Waiting int
}

// CountPending returns how many messages of each kind wait for delivery at now.
func (d *DB) CountPending(ctx context.Context, now time.Time) (_ map[MessageKind]PendingCount, err error) {
	defer d.observe(&err)()

	rows, err := d.conn.QueryContext(ctx,
		`SELECT kind, COALESCE(SUM(next_attempt_at <= ?), 0), COUNT(*) FROM messages WHERE status = ? GROUP BY kind`,
		now.UTC().UnixNano(), StatusPending)
	if err != nil {
		return nil, fmt.Errorf("count pending messages: %w", err)
	}

	defer func() { _ = rows.Close() }()

	counts := map[MessageKind]PendingCount{KindMessage: {}, KindStatusReport: {}}

	for rows.Next() {
		var (
			kind       MessageKind
			due, total int
		)

		if err := rows.Scan(&kind, &due, &total); err != nil {
			return nil, fmt.Errorf("count pending messages: %w", err)
		}

		counts[kind] = PendingCount{Due: due, Waiting: total - due}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count pending messages: %w", err)
	}

	return counts, nil
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
		`UPDATE messages SET next_attempt_at = MIN(?, expires_at), updated_at = ?
		WHERE msisdn = ? AND status = ? AND next_attempt_at < MIN(?, expires_at)`,
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

// CompleteMessage sets the final status of a pending message, and stores the status report on it, if any, in the same
// transaction, so that the report is neither lost nor stored twice. It returns ErrNotPending, and leaves the message as
// is, when the message already has a final status, as a message that a later one replaced during its delivery does.
func (d *DB) CompleteMessage(ctx context.Context, id int64, status MessageStatus, at time.Time, report *NewStatusReport) (err error) {
	defer d.observe(&err)()

	switch status {
	case StatusDelivered, StatusFailed, StatusExpired:
	default:
		return fmt.Errorf("complete message: %q is not a final status of a delivery", status)
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete message: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var kind MessageKind

	err = tx.QueryRowContext(ctx,
		`UPDATE messages SET status = ?, updated_at = ? WHERE id = ? AND status = ? RETURNING kind`,
		status, at.UTC().UnixNano(), id, StatusPending).Scan(&kind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("complete message: %w", err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE id = ?)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("complete message: %w", err)
		}

		if exists {
			return ErrNotPending
		}

		return ErrNotFound
	}

	if report != nil {
		if err := insertStatusReport(ctx, tx, id, *report); err != nil {
			return fmt.Errorf("complete message: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complete message: %w", err)
	}

	d.metrics.count([]completion{{kind, status}})

	return nil
}

const purgeableMessages = `SELECT m.id FROM messages m
	WHERE m.kind = 'message' AND m.status != 'pending' AND m.updated_at < ?1
	AND NOT EXISTS (SELECT 1 FROM messages r WHERE r.report_for = m.id AND (r.status = 'pending' OR r.updated_at >= ?1))
	ORDER BY m.updated_at LIMIT ?2`

// PurgeMessages deletes up to limit short messages that reached their final status before cutoff, with their delivery
// attempts and status reports, and returns how many messages it deleted. It keeps a message with a status report that
// is pending or that reached its final status after cutoff, so that a report always goes with its message.
func (d *DB) PurgeMessages(ctx context.Context, cutoff time.Time, limit int) (_ int, err error) {
	defer d.observe(&err)()

	at := cutoff.UTC().UnixNano()

	// DELETE ... LIMIT needs a compile option of SQLite, so the limit is on the subquery. The kind and status are
	// literals, so that the query matches the WHERE of the messages_purgeable index. The changes it counts leave out the
	// rows that foreign keys delete: they are the messages only.
	res, err := d.conn.ExecContext(ctx, `DELETE FROM messages WHERE id IN (`+purgeableMessages+`)`, at, limit)
	if err != nil {
		return 0, fmt.Errorf("purge messages: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge messages: %w", err)
	}

	d.metrics.purged.Add(float64(n))

	return int(n), nil
}

// StatusReportsFor returns the status reports on each of the messages that has some, oldest first.
func (d *DB) StatusReportsFor(ctx context.Context, messageIDs []int64) (_ map[int64][]Message, err error) {
	defer d.observe(&err)()

	reports := map[int64][]Message{}
	if len(messageIDs) == 0 {
		return reports, nil
	}

	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		args[i] = id
	}

	rows, err := d.conn.QueryContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE report_for IN (?`+strings.Repeat(`, ?`, len(messageIDs)-1)+`)
		ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list status reports: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("list status reports: %w", err)
		}

		reports[m.ReportFor] = append(reports[m.ReportFor], m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list status reports: %w", err)
	}

	return reports, nil
}
