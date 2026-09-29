package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type AttemptStep string

const (
	StepRouting  AttemptStep = "routing"
	StepDelivery AttemptStep = "delivery"
)

type DeliveryAttempt struct {
	ID          int64
	MessageID   int64
	AttemptedAt time.Time
	Step        AttemptStep
	Node        string
	Outcome     string
	ResultCode  *uint32
	VendorID    *uint32
}

func (d *DB) CreateDeliveryAttempt(ctx context.Context, a DeliveryAttempt) (int64, error) {
	res, err := d.conn.ExecContext(ctx,
		`INSERT INTO delivery_attempts (message_id, attempted_at, step, node, outcome, result_code, vendor_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.AttemptedAt.UTC().UnixNano(), a.Step, a.Node, a.Outcome, nullable(a.ResultCode), nullable(a.VendorID))
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
		`SELECT id, message_id, attempted_at, step, node, outcome, result_code, vendor_id
		FROM delivery_attempts WHERE message_id = ? ORDER BY id`,
		messageID)
	if err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var attempts []DeliveryAttempt

	for rows.Next() {
		var (
			a                    DeliveryAttempt
			attemptedAt          int64
			resultCode, vendorID sql.Null[uint32]
		)

		if err := rows.Scan(&a.ID, &a.MessageID, &attemptedAt, &a.Step, &a.Node, &a.Outcome, &resultCode, &vendorID); err != nil {
			return nil, fmt.Errorf("list delivery attempts: %w", err)
		}

		a.AttemptedAt = time.Unix(0, attemptedAt).UTC()
		a.ResultCode = pointer(resultCode)
		a.VendorID = pointer(vendorID)
		attempts = append(attempts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}

	return attempts, nil
}

func nullable(v *uint32) sql.Null[uint32] {
	if v == nil {
		return sql.Null[uint32]{}
	}

	return sql.Null[uint32]{V: *v, Valid: true}
}

func pointer(v sql.Null[uint32]) *uint32 {
	if !v.Valid {
		return nil
	}

	return &v.V
}
