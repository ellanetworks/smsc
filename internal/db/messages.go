package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type MessageStatus string

const (
	StatusPending   MessageStatus = "pending"
	StatusDelivered MessageStatus = "delivered"
	StatusFailed    MessageStatus = "failed"
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
	MessageReference   uint8
	ProtocolIdentifier uint8
	TPDU               []byte
	Status             MessageStatus
	SingleShot         bool
	SubmittedAt        time.Time
	ExpiresAt          time.Time
	UpdatedAt          time.Time
}

type NewMessage struct {
	Originator         Address
	Recipient          Address
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

func insertMessage(ctx context.Context, tx *sql.Tx, m NewMessage) (int64, error) {
	at := m.SubmittedAt.UTC().UnixNano()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages (originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi,
		message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Originator.Digits, m.Originator.TypeOfNumber, m.Originator.NumberingPlan,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan,
		m.MessageReference, m.ProtocolIdentifier, m.TPDU, StatusPending, m.SingleShot, at, nullableTime(m.ExpiresAt), at)
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

	_, err = tx.ExecContext(ctx,
		`UPDATE messages SET recipient = ?, recipient_ton = ?, recipient_npi = ?, message_reference = ?, tpdu = ?,
		single_shot = ?, submitted_at = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
		m.Recipient.Digits, m.Recipient.TypeOfNumber, m.Recipient.NumberingPlan, m.MessageReference, m.TPDU,
		m.SingleShot, at, nullableTime(m.ExpiresAt), at, id)
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

func (d *DB) GetMessage(ctx context.Context, id int64) (Message, error) {
	var (
		m                      Message
		submittedAt, updatedAt int64
		expiresAt              sql.Null[int64]
	)

	err := d.conn.QueryRowContext(ctx,
		`SELECT id, originator, originator_ton, originator_npi, recipient, recipient_ton, recipient_npi,
		message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at, expires_at, updated_at
		FROM messages WHERE id = ?`, id).
		Scan(&m.ID, &m.Originator.Digits, &m.Originator.TypeOfNumber, &m.Originator.NumberingPlan,
			&m.Recipient.Digits, &m.Recipient.TypeOfNumber, &m.Recipient.NumberingPlan,
			&m.MessageReference, &m.ProtocolIdentifier, &m.TPDU, &m.Status, &m.SingleShot,
			&submittedAt, &expiresAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}

	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}

	m.SubmittedAt = time.Unix(0, submittedAt).UTC()
	m.UpdatedAt = time.Unix(0, updatedAt).UTC()

	if expiresAt.Valid {
		m.ExpiresAt = time.Unix(0, expiresAt.V).UTC()
	}

	return m, nil
}

func nullableTime(t time.Time) sql.Null[int64] {
	if t.IsZero() {
		return sql.Null[int64]{}
	}

	return sql.Null[int64]{V: t.UTC().UnixNano(), Valid: true}
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
