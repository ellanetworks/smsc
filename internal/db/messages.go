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

type Message struct {
	ID          int64
	Originator  string
	Recipient   string
	TPDU        []byte
	Status      MessageStatus
	SubmittedAt time.Time
	UpdatedAt   time.Time
}

func (d *DB) CreateMessage(ctx context.Context, originator, recipient string, tpdu []byte, submittedAt time.Time) (int64, error) {
	at := submittedAt.UTC().UnixNano()

	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO messages (originator, recipient, tpdu, status, submitted_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		originator, recipient, tpdu, StatusPending, at, at)
	if err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create message: %w", err)
	}

	return id, nil
}

func (d *DB) GetMessage(ctx context.Context, id int64) (Message, error) {
	var (
		m                      Message
		submittedAt, updatedAt int64
	)

	err := d.conn.QueryRowContext(ctx,
		`SELECT id, originator, recipient, tpdu, status, submitted_at, updated_at FROM messages WHERE id = ?`, id).
		Scan(&m.ID, &m.Originator, &m.Recipient, &m.TPDU, &m.Status, &submittedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}

	if err != nil {
		return Message{}, fmt.Errorf("get message: %w", err)
	}

	m.SubmittedAt = time.Unix(0, submittedAt).UTC()
	m.UpdatedAt = time.Unix(0, updatedAt).UTC()

	return m, nil
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
