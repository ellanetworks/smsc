package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/internal/tracing"
)

type MessageStatus string

const (
	StatusPending   MessageStatus = "pending"
	StatusDelivered MessageStatus = "delivered"
	StatusFailed    MessageStatus = "failed"
	StatusExpired   MessageStatus = "expired"
	StatusReplaced  MessageStatus = "replaced"
)

type MessageKind string

const (
	KindMessage      MessageKind = "message"
	KindStatusReport MessageKind = "status_report"
)

type MessageOrigin string

const (
	OriginMobile MessageOrigin = "mobile"
	OriginAPI    MessageOrigin = "api"
	OriginSMPP   MessageOrigin = "smpp"
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
	ID                 int64
	Kind               MessageKind
	ReportFor          int64
	ReportStatus       uint8
	Originator         Address
	Recipient          Address
	MSISDN             string
	Origin             MessageOrigin
	SubmittedBy        int64
	MessageReference   uint8
	ProtocolIdentifier uint8
	TPDU               []byte
	Status             MessageStatus
	SingleShot         bool
	SubmittedAt        time.Time
	TimeStamp          time.Time
	ExpiresAt          time.Time
	NextAttemptAt      time.Time
	Retries            int
	UpdatedAt          time.Time
	TraceParent        string
}

type NewMessage struct {
	Originator         Address
	Recipient          Address
	MSISDN             string
	Origin             MessageOrigin
	SubmittedBy        int64
	MessageReference   uint8
	ProtocolIdentifier uint8
	RejectDuplicates   bool
	Replace            bool
	TPDU               []byte
	SingleShot         bool
	SubmittedAt        time.Time
	ExpiresAt          time.Time
	ReportReplaced     bool
	SMPP               *NewSMPPMessage
	TraceParent        string
}

// SMPP 3.4 section 2.11
type NewSMPPMessage struct {
	ESMEID               int64
	SourceNPI            uint8
	Receipt              uint8
	SIMAnswer            bool
	DataCoding           uint8
	UserMessageReference *uint16
}

// SMPP 3.4 section 5.2.17
const (
	ReceiptNone    uint8 = 0
	ReceiptFinal   uint8 = 1
	ReceiptFailure uint8 = 2
)

type NewStatusReport struct {
	Originator Address
	Status     uint8
	TPDU       []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

func (d *DB) CreateMessage(ctx context.Context, m NewMessage) (int64, error) {
	ids, err := d.CreateMessages(ctx, []NewMessage{m})
	if err != nil {
		return 0, err
	}

	return ids[0], nil
}

type storeMessages struct {
	Messages []NewMessage
}

type storedMessages struct {
	IDs      []int64
	Replaced int
}

func (d *DB) CreateMessages(ctx context.Context, messages []NewMessage) (_ []int64, err error) {
	defer d.observe(&err)()

	tp := tracing.TraceParent(ctx)

	for i := range messages {
		if messages[i].TraceParent == "" {
			messages[i].TraceParent = tp
		}
	}

	r, replayed, err := submitReplay[storedMessages](ctx, d, cmdStoreMessages, storeMessages{Messages: messages})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return nil, err
		}

		return nil, fmt.Errorf("create message: %w", err)
	}

	if !replayed {
		for range r.Replaced {
			d.metrics.count([]completion{{KindMessage, StatusReplaced}})
		}
	}

	return r.IDs, nil
}

func applyStoreMessages(a *applyCtx, p storeMessages) (storedMessages, error) {
	var r storedMessages

	for _, m := range p.Messages {
		id, replaced, err := createMessage(a, m)
		if err != nil {
			return storedMessages{}, err
		}

		r.IDs = append(r.IDs, id)

		if replaced {
			r.Replaced++
		}
	}

	a.events.Messages = true

	return r, nil
}

func createMessage(a *applyCtx, m NewMessage) (int64, bool, error) {
	ctx, tx := a.ctx, a.tx

	if m.RejectDuplicates {
		duplicate, err := isDuplicate(ctx, tx, m)
		if err != nil {
			return 0, false, err
		}

		if duplicate {
			return 0, false, ErrDuplicate
		}
	}

	var replaced bool

	if m.Replace {
		var err error
		if replaced, err = replacePending(a, m); err != nil {
			return 0, false, err
		}
	}

	id, err := insertMessage(ctx, tx, m)
	if err != nil {
		return 0, false, err
	}

	if r := m.SMPP; r != nil {
		var reference sql.Null[uint16]
		if r.UserMessageReference != nil {
			reference = sql.Null[uint16]{V: *r.UserMessageReference, Valid: true}
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO smpp_messages (message_id, esme_id, receipt, sim_answer, source_npi,
			data_coding, user_message_reference) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, r.ESMEID, r.Receipt, r.SIMAnswer,
			r.SourceNPI, r.DataCoding, reference); err != nil {
			return 0, false, err
		}
	}

	return id, replaced, nil
}

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

// TS 23.040 clause 6.2
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
		time_stamp, expires_at, next_attempt_at, retries, updated_at, traceparent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MSISDN, m.Origin,
		sql.Null[int64]{V: m.SubmittedBy, Valid: m.SubmittedBy != 0}, m.MessageReference, m.ProtocolIdentifier, m.TPDU, StatusPending, m.SingleShot, at, timeStamp,
		expiresAt, next, at, nullableString(m.TraceParent))
	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func insertStatusReport(ctx context.Context, tx *sql.Tx, messageID int64, r NewStatusReport, traceparent string) error {
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

	next, err := due(ctx, tx, recipient.Digits, at, expiresAt)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO messages (kind, report_for, report_status, originator, originator_ton, originator_npi, recipient,
		recipient_ton, recipient_npi, msisdn, origin, message_reference, protocol_identifier, tpdu, status, single_shot,
		submitted_at, time_stamp, expires_at, next_attempt_at, retries, updated_at, traceparent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 0, ?, ?, ?, ?, 0, ?, ?)`,
		KindStatusReport, messageID, r.Status, r.Originator.Digits, r.Originator.TypeOfNumber, r.Originator.NumberingPlan,
		recipient.Digits, recipient.TypeOfNumber, recipient.NumberingPlan, recipient.Digits, OriginMobile, reference,
		r.TPDU, StatusPending, at, submittedAt, expiresAt, next, at, nullableString(traceparent))

	return err
}

// TS 23.040 clause 2.1.1
// TS 23.040 clause 9.2.3.9
func replacePending(a *applyCtx, m NewMessage) (bool, error) {
	ctx, tx := a.ctx, a.tx

	query := `SELECT ` + messageColumns + ` FROM messages WHERE kind = ? AND originator = ? AND originator_ton = ?
		AND originator_npi = ? AND protocol_identifier = ? AND status = ? AND origin = ?`

	args := []any{
		KindMessage, m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.ProtocolIdentifier, StatusPending, m.Origin,
	}

	switch m.Origin {
	case OriginMobile:
	case OriginSMPP:
		if m.SMPP == nil {
			return false, nil
		}

		query += ` AND id IN (SELECT message_id FROM smpp_messages WHERE esme_id = ?)`

		args = append(args, m.SMPP.ESMEID)
	default:
		if m.SubmittedBy == 0 {
			return false, nil
		}

		query += ` AND submitted_by = ?`

		args = append(args, m.SubmittedBy)
	}

	replaced, err := scanMessage(tx.QueryRowContext(ctx, query+` ORDER BY id DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	at := m.SubmittedAt.UTC().UnixNano()

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`,
		StatusReplaced, at, replaced.ID); err != nil {
		return false, err
	}

	replaced.Status = StatusReplaced

	queued, err := queueNotifications(ctx, tx, replaced.ID, StatusReplaced, uint8(tpdu.StatusReplaced), at)
	if err != nil {
		return false, err
	}

	a.events.Notifications = a.events.Notifications || queued

	if !m.ReportReplaced {
		return true, nil
	}

	serviceCentre, validity, err := reportSettings(ctx, tx)
	if err != nil {
		return false, err
	}

	report, err := StatusReportFor(replaced, tpdu.StatusReplaced, m.SubmittedAt, serviceCentre, validity)
	if err != nil || report == nil {
		return true, nil
	}

	return true, insertStatusReport(ctx, tx, replaced.ID, *report, m.TraceParent)
}

func reportSettings(ctx context.Context, q querier) (string, time.Duration, error) {
	var (
		serviceCentre string
		validity      int64
	)

	if err := q.QueryRowContext(ctx, `SELECT service_centre_address FROM operator WHERE id = 1`).
		Scan(&serviceCentre); err != nil {
		return "", 0, err
	}

	if err := q.QueryRowContext(ctx, `SELECT default_validity FROM delivery WHERE id = 1`).Scan(&validity); err != nil {
		return "", 0, err
	}

	return serviceCentre, time.Duration(validity), nil
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
	next_attempt_at, retries, updated_at, traceparent`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (Message, error) {
	var (
		m                                                           Message
		reportFor                                                   sql.Null[int64]
		reportStatus                                                sql.Null[uint8]
		submittedBy                                                 sql.Null[int64]
		traceparent                                                 sql.Null[string]
		submittedAt, timeStamp, expiresAt, nextAttemptAt, updatedAt int64
	)

	err := row.Scan(&m.ID, &m.Kind, &reportFor, &reportStatus, &m.Originator.Digits, &m.Originator.TypeOfNumber, &m.Originator.NumberingPlan,
		&m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan, &m.MSISDN, &m.Origin, &submittedBy,
		&m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status, &m.SingleShot,
		&submittedAt, &timeStamp, &expiresAt, &nextAttemptAt, &m.Retries, &updatedAt, &traceparent)
	if err != nil {
		return Message{}, err
	}

	m.ReportFor, m.ReportStatus, m.SubmittedBy, m.TraceParent = reportFor.V, reportStatus.V, submittedBy.V, traceparent.V
	m.SubmittedAt = time.Unix(0, submittedAt).UTC()
	m.TimeStamp = time.Unix(0, timeStamp).UTC()
	m.ExpiresAt = time.Unix(0, expiresAt).UTC()
	m.NextAttemptAt = time.Unix(0, nextAttemptAt).UTC()
	m.UpdatedAt = time.Unix(0, updatedAt).UTC()

	return m, nil
}

func (d *DB) GetMessage(ctx context.Context, id int64) (_ Message, err error) {
	defer d.observe(&err)()

	m, err := scanMessage(d.reader().QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}

	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}

	return m, nil
}

type MessageFilter struct {
	MSISDN             string
	Originator         string
	OriginatorContains bool
	OriginatorTON      *uint8
	Status             MessageStatus
	SubmittedBy        int64
	Start, End         time.Time
}

func (f MessageFilter) where() (string, []any) {
	where, args := []string{"kind = ?"}, []any{KindMessage}

	if f.MSISDN != "" {
		where, args = append(where, "instr(msisdn, ?) > 0"), append(args, f.MSISDN)
	}

	switch {
	case f.Originator != "" && f.OriginatorContains:
		where, args = append(where, "instr(originator, ?) > 0"), append(args, f.Originator)
	case f.Originator != "":
		where, args = append(where, "originator = ? COLLATE NOCASE"), append(args, f.Originator)
	}

	if f.OriginatorTON != nil {
		where, args = append(where, "originator_ton = ?"), append(args, *f.OriginatorTON)
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

	return " WHERE " + strings.Join(where, " AND "), args
}

func (d *DB) ListMessages(ctx context.Context, f MessageFilter, page, perPage int) (_ []Message, _ int, err error) {
	defer d.observe(&err)()

	clause, args := f.where()

	var total int
	if err := d.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list messages: %w", err)
	}

	rows, err := d.reader().QueryContext(ctx, `SELECT `+messageColumns+` FROM messages`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
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

func (d *DB) CountMessages(ctx context.Context, f MessageFilter) (_ map[MessageStatus]int, err error) {
	defer d.observe(&err)()

	clause, args := f.where()

	rows, err := d.reader().QueryContext(ctx, `SELECT status, COUNT(*) FROM messages`+clause+` GROUP BY status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}

	defer func() { _ = rows.Close() }()

	counts := map[MessageStatus]int{}

	for rows.Next() {
		var (
			status MessageStatus
			n      int
		)

		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("count messages: %w", err)
		}

		counts[status] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}

	return counts, nil
}

func (d *DB) DueRecipients(ctx context.Context, now time.Time, limit int, skip func(msisdn string) bool) (
	_ []string, _ time.Time, err error,
) {
	defer d.observe(&err)()

	rows, err := d.reader().QueryContext(ctx,
		`SELECT msisdn, next_attempt_at FROM recipients WHERE next_attempt_at IS NOT NULL ORDER BY next_attempt_at, msisdn`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("due recipients: %w", err)
	}

	defer func() { _ = rows.Close() }()

	at := now.UTC().UnixNano()

	var due []string

	for len(due) < limit && rows.Next() {
		var (
			msisdn string
			next   int64
		)

		if err := rows.Scan(&msisdn, &next); err != nil {
			return nil, time.Time{}, fmt.Errorf("due recipients: %w", err)
		}

		if skip != nil && skip(msisdn) {
			continue
		}

		if next > at {
			return due, time.Unix(0, next).UTC(), nil
		}

		due = append(due, msisdn)
	}

	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("due recipients: %w", err)
	}

	return due, time.Time{}, nil
}

func (d *DB) NextDue(ctx context.Context, msisdn string, now time.Time) (_ Message, _ bool, err error) {
	defer d.observe(&err)()

	m, err := scanMessage(d.reader().QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages WHERE msisdn = ? AND status = ? AND next_attempt_at <= ?
		ORDER BY next_attempt_at, id LIMIT 1`, msisdn, StatusPending, now.UTC().UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}

	if err != nil {
		return Message{}, false, fmt.Errorf("next due message: %w", err)
	}

	return m, true, nil
}

type scheduleRetry struct {
	ID       int64
	At       time.Time
	Now      time.Time
	Hold     bool
	Since    time.Time
	Fence    Fence
	Attempts []DeliveryAttempt `json:",omitempty"`
}

func (d *DB) ScheduleRetry(ctx context.Context, id int64, at, now time.Time, hold bool, since time.Time, f Fence,
	attempts ...DeliveryAttempt,
) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdScheduleRetry, scheduleRetry{
		ID: id, At: at, Now: now, Hold: hold, Since: since, Fence: f, Attempts: attempts,
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrStaleTerm) {
		return fmt.Errorf("schedule retry: %w", err)
	}

	return err
}

func applyScheduleRetry(a *applyCtx, p scheduleRetry) (struct{}, error) {
	ctx, tx := a.ctx, a.tx

	next, updated := p.At.UTC().UnixNano(), p.Now.UTC().UnixNano()

	var msisdn string

	if err := tx.QueryRowContext(ctx, `SELECT msisdn FROM messages WHERE id = ?`, p.ID).Scan(&msisdn); errors.Is(err,
		sql.ErrNoRows) {
		return struct{}{}, ErrNotFound
	} else if err != nil {
		return struct{}{}, err
	}

	if err := fenced(a, msisdn, p.Fence); err != nil {
		return struct{}{}, err
	}

	if err := insertAttempts(a, p.Attempts); err != nil {
		return struct{}{}, err
	}

	err := tx.QueryRowContext(ctx,
		`UPDATE messages SET next_attempt_at = MIN(?, expires_at), retries = retries + 1, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING msisdn`,
		next, updated, p.ID, StatusPending).Scan(&msisdn)
	if errors.Is(err, sql.ErrNoRows) {
		return struct{}{}, ErrNotFound
	}

	if err != nil {
		return struct{}{}, err
	}

	if p.Hold {
		alerted, err := alertedSince(a, msisdn, p.Since)
		if err != nil {
			return struct{}{}, err
		}

		if !alerted {
			if err := holdRecipient(ctx, tx, msisdn, next, updated); err != nil {
				return struct{}{}, err
			}
		}
	}

	if p.Fence.Keep {
		return struct{}{}, nil
	}

	return struct{}{}, release(a, msisdn, p.Fence.Claim)
}

type PendingCount struct {
	Due     int
	Waiting int
}

func (d *DB) CountPending(ctx context.Context, now time.Time) (_ map[MessageKind]PendingCount, err error) {
	defer d.observe(&err)()

	rows, err := d.reader().QueryContext(ctx,
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

func (d *DB) NextDueFor(ctx context.Context, msisdn string, excludeID int64, now time.Time) (_ Message, _ bool, err error) {
	defer d.observe(&err)()

	at := now.UTC().UnixNano()

	m, err := scanMessage(d.reader().QueryRowContext(ctx,
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

type alertRecipient struct {
	MSISDN string
	Now    time.Time
}

func (d *DB) AlertRecipient(ctx context.Context, msisdn string, now time.Time) (_ []string, err error) {
	defer d.observe(&err)()

	recipients, err := submit[[]string](ctx, d, cmdAlertRecipient, alertRecipient{MSISDN: msisdn, Now: now})
	if err != nil {
		return nil, fmt.Errorf("alert recipient: %w", err)
	}

	return recipients, nil
}

func applyAlertRecipient(a *applyCtx, p alertRecipient) ([]string, error) {
	ctx, tx := a.ctx, a.tx

	recipients := []string{p.MSISDN}

	rows, err := tx.QueryContext(ctx, `SELECT msisdn FROM recipients WHERE alert_msisdn = ? AND msisdn != ?
		ORDER BY msisdn`, p.MSISDN, p.MSISDN)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			_ = rows.Close()
			return nil, err
		}

		recipients = append(recipients, r)
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	at := p.Now.UTC().UnixNano()

	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO recipients (msisdn, alerted_at, updated_at) VALUES (?1, ?2, ?2)
			ON CONFLICT (msisdn) DO UPDATE SET held_until = NULL, alerted_at = ?2, updated_at = ?2`, r, at); err != nil {
			return nil, err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE messages SET next_attempt_at = ?, updated_at = ? WHERE msisdn = ? AND status = ? AND next_attempt_at > ?`,
			at, at, r, StatusPending, at); err != nil {
			return nil, err
		}
	}

	a.events.Messages = true
	a.events.Alerted = append(a.events.Alerted, recipients...)

	return recipients, nil
}

type setAlertMSISDN struct {
	MSISDN      string
	AlertMSISDN string
	Now         time.Time
}

func (d *DB) SetAlertMSISDN(ctx context.Context, msisdn, alertMSISDN string, now time.Time) (err error) {
	defer d.observe(&err)()

	var current sql.NullString

	err = d.reader().QueryRowContext(ctx, `SELECT alert_msisdn FROM recipients WHERE msisdn = ?`, msisdn).Scan(&current)
	if err == nil && current.String == alertMSISDNOf(msisdn, alertMSISDN).V {
		return nil
	}

	_, err = submit[struct{}](ctx, d, cmdSetAlertMSISDN, setAlertMSISDN{MSISDN: msisdn, AlertMSISDN: alertMSISDN, Now: now})
	if err != nil {
		return fmt.Errorf("set alert MSISDN: %w", err)
	}

	return nil
}

func alertMSISDNOf(msisdn, alertMSISDN string) sql.Null[string] {
	if alertMSISDN == "" || alertMSISDN == msisdn {
		return sql.Null[string]{}
	}

	return sql.Null[string]{V: alertMSISDN, Valid: true}
}

func applySetAlertMSISDN(a *applyCtx, p setAlertMSISDN) (struct{}, error) {
	alert := alertMSISDNOf(p.MSISDN, p.AlertMSISDN)

	_, err := a.tx.ExecContext(a.ctx,
		`INSERT INTO recipients (msisdn, alert_msisdn, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (msisdn) DO UPDATE SET alert_msisdn = excluded.alert_msisdn, updated_at = excluded.updated_at`,
		p.MSISDN, alert, p.Now.UTC().UnixNano())

	return struct{}{}, err
}

type completeMessage struct {
	ID          int64
	Status      MessageStatus
	At          time.Time
	Report      *NewStatusReport
	TPStatus    uint8
	TraceParent string
	Fence       Fence
	Attempts    []DeliveryAttempt `json:",omitempty"`
}

func (d *DB) CompleteMessage(ctx context.Context, id int64, status MessageStatus, at time.Time, report *NewStatusReport,
	tpStatus uint8, f Fence, attempts ...DeliveryAttempt,
) (err error) {
	defer d.observe(&err)()

	kind, replayed, err := submitReplay[MessageKind](ctx, d, cmdCompleteMessage, completeMessage{
		ID: id, Status: status, At: at, Report: report, TPStatus: tpStatus, TraceParent: tracing.TraceParent(ctx),
		Fence: f, Attempts: attempts,
	})
	if errors.Is(err, ErrNotPending) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStaleTerm) {
		return err
	}

	if err != nil {
		return fmt.Errorf("complete message: %w", err)
	}

	if !replayed {
		d.metrics.count([]completion{{kind, status}})
	}

	return nil
}

func applyCompleteMessage(a *applyCtx, p completeMessage) (MessageKind, error) {
	ctx, tx := a.ctx, a.tx

	switch p.Status {
	case StatusDelivered, StatusFailed, StatusExpired:
	default:
		return "", fmt.Errorf("%w: %q is not a final status of a delivery", ErrInvalid, p.Status)
	}

	at := p.At.UTC().UnixNano()

	var msisdn string

	if err := tx.QueryRowContext(ctx, `SELECT msisdn FROM messages WHERE id = ?`, p.ID).Scan(&msisdn); errors.Is(err,
		sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}

	var kind MessageKind

	err := tx.QueryRowContext(ctx,
		`UPDATE messages SET status = ?, updated_at = ? WHERE id = ? AND status = ? RETURNING kind`,
		p.Status, at, p.ID, StatusPending).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM messages WHERE id = ?)`, p.ID).Scan(&exists); err != nil {
			return "", err
		}

		if exists {
			return "", ErrNotPending
		}

		return "", ErrNotFound
	}

	if err != nil {
		return "", err
	}

	if err := insertAttempts(a, p.Attempts); err != nil {
		return "", err
	}

	if p.Report != nil {
		if err := insertStatusReport(ctx, tx, p.ID, *p.Report, p.TraceParent); err != nil {
			return "", err
		}

		a.events.Messages = true
	}

	queued, err := queueNotifications(ctx, tx, p.ID, p.Status, p.TPStatus, at)
	if err != nil {
		return "", err
	}

	a.events.Notifications = a.events.Notifications || queued

	if p.Fence.Keep {
		return kind, nil
	}

	return kind, release(a, msisdn, p.Fence.Claim)
}

const purgeableMessages = `SELECT m.id FROM messages m
	WHERE m.kind = 'message' AND m.status != 'pending' AND m.updated_at < ?1
	AND NOT EXISTS (SELECT 1 FROM messages r WHERE r.report_for = m.id AND (r.status = 'pending' OR r.updated_at >= ?1))
	AND NOT EXISTS (SELECT 1 FROM smpp_notifications n WHERE n.message_id = m.id AND n.result IS NULL AND n.expires_at >= ?1)
	ORDER BY m.updated_at, m.id LIMIT ?2`

type purgeMessages struct {
	Cutoff time.Time
	Limit  int
}

func (d *DB) PurgeMessages(ctx context.Context, cutoff time.Time, limit int) (_ int, err error) {
	defer d.observe(&err)()

	n, err := submit[int](ctx, d, cmdPurgeMessages, purgeMessages{Cutoff: cutoff, Limit: limit})
	if err != nil {
		return 0, fmt.Errorf("purge messages: %w", err)
	}

	d.metrics.purged.Add(float64(n))

	return n, nil
}

func applyPurgeMessages(a *applyCtx, p purgeMessages) (int, error) {
	res, err := a.tx.ExecContext(a.ctx, `DELETE FROM messages WHERE id IN (`+purgeableMessages+`)`,
		p.Cutoff.UTC().UnixNano(), p.Limit)
	if err != nil {
		return 0, err
	}

	n, err := res.RowsAffected()

	return int(n), err
}

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

	rows, err := d.reader().QueryContext(ctx,
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

func alertedSince(a *applyCtx, msisdn string, since time.Time) (bool, error) {
	if since.IsZero() {
		return false, nil
	}

	var alerted sql.NullInt64

	err := a.tx.QueryRowContext(a.ctx, `SELECT alerted_at FROM recipients WHERE msisdn = ?`, msisdn).Scan(&alerted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	return alerted.Valid && alerted.Int64 >= since.UTC().UnixNano(), err
}
