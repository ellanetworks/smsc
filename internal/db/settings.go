package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

func (d *DB) GetSettings(ctx context.Context) (settings.Settings, error) {
	var (
		s        settings.Settings
		validity int64
	)

	o := &s.Operator

	if err := d.conn.QueryRowContext(ctx, `SELECT mcc, mnc, service_centre_address, country_code, national_prefix,
		international_prefix FROM operator WHERE id = 1`).Scan(&o.MCC, &o.MNC, &o.ServiceCentreAddress,
		&o.Numbering.CountryCode, &o.Numbering.NationalPrefix, &o.Numbering.InternationalPrefix); err != nil {
		return settings.Settings{}, fmt.Errorf("get operator: %w", err)
	}

	if err := d.conn.QueryRowContext(ctx, `SELECT default_validity FROM delivery WHERE id = 1`).Scan(&validity); err != nil {
		return settings.Settings{}, fmt.Errorf("get delivery: %w", err)
	}

	s.Delivery.DefaultValidity = time.Duration(validity)

	var err error

	if s.Delivery.RetryIntervals, err = d.retryIntervals(ctx); err != nil {
		return settings.Settings{}, err
	}

	return s, nil
}

func (d *DB) retryIntervals(ctx context.Context) ([]time.Duration, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT interval FROM retry_intervals ORDER BY attempt`)
	if err != nil {
		return nil, fmt.Errorf("get retry intervals: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var intervals []time.Duration

	for rows.Next() {
		var interval int64
		if err := rows.Scan(&interval); err != nil {
			return nil, fmt.Errorf("get retry intervals: %w", err)
		}

		intervals = append(intervals, time.Duration(interval))
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get retry intervals: %w", err)
	}

	return intervals, nil
}

func (d *DB) UpdateOperator(ctx context.Context, o settings.Operator) error {
	if _, err := d.conn.ExecContext(ctx, `UPDATE operator SET mcc = ?, mnc = ?, service_centre_address = ?,
		country_code = ?, national_prefix = ?, international_prefix = ? WHERE id = 1`, o.MCC, o.MNC,
		o.ServiceCentreAddress, o.Numbering.CountryCode, o.Numbering.NationalPrefix,
		o.Numbering.InternationalPrefix); err != nil {
		return fmt.Errorf("update operator: %w", err)
	}

	return nil
}

func (d *DB) UpdateDelivery(ctx context.Context, dl settings.Delivery) error {
	return d.inTx(ctx, "update delivery", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE delivery SET default_validity = ? WHERE id = 1`,
			int64(dl.DefaultValidity)); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM retry_intervals`); err != nil {
			return err
		}

		for i, interval := range dl.RetryIntervals {
			if _, err := tx.ExecContext(ctx, `INSERT INTO retry_intervals (attempt, interval) VALUES (?, ?)`,
				i+1, int64(interval)); err != nil {
				return err
			}
		}

		return nil
	})
}

func (d *DB) inTx(ctx context.Context, what string, fn func(tx *sql.Tx) error) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}
