package db

import (
	"context"
	"fmt"
	"time"
)

type DeliveryAttempt struct {
	ID          int64
	MessageID   int64
	AttemptedAt time.Time
	ServingNode string
	ResultCode  uint32
}

func (d *DB) CreateDeliveryAttempt(ctx context.Context, messageID int64, servingNode string, resultCode uint32, attemptedAt time.Time) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO delivery_attempts (message_id, attempted_at, serving_node, result_code) VALUES (?, ?, ?, ?)`,
		messageID, attemptedAt.UTC().UnixNano(), servingNode, resultCode)
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create delivery attempt: %w", err)
	}

	return id, nil
}

func (d *DB) ListDeliveryAttempts(ctx context.Context, messageID int64) ([]DeliveryAttempt, error) {
	rows, err := d.conn.QueryContext(ctx,
		`SELECT id, message_id, attempted_at, serving_node, result_code FROM delivery_attempts WHERE message_id = ? ORDER BY id`,
		messageID)
	if err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var attempts []DeliveryAttempt

	for rows.Next() {
		var (
			a           DeliveryAttempt
			attemptedAt int64
		)

		if err := rows.Scan(&a.ID, &a.MessageID, &attemptedAt, &a.ServingNode, &a.ResultCode); err != nil {
			return nil, fmt.Errorf("list delivery attempts: %w", err)
		}

		a.AttemptedAt = time.Unix(0, attemptedAt).UTC()
		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}

	return attempts, nil
}
