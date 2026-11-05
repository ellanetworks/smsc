package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

func (d *DB) GetSettings(ctx context.Context) (_ settings.Settings, err error) {
	defer d.observe(&err)()

	return getSettings(ctx, d.reader())
}

func getSettings(ctx context.Context, q querier) (_ settings.Settings, err error) {
	var (
		s        settings.Settings
		validity int64
	)

	o := &s.Operator

	if err := q.QueryRowContext(ctx, `SELECT mcc, mnc, service_centre_address, country_code, national_prefix,
		international_prefix FROM operator WHERE id = 1`).Scan(&o.MCC, &o.MNC, &o.ServiceCentreAddress,
		&o.Numbering.CountryCode, &o.Numbering.NationalPrefix, &o.Numbering.InternationalPrefix); err != nil {
		return settings.Settings{}, fmt.Errorf("get operator: %w", err)
	}

	if err := q.QueryRowContext(ctx, `SELECT default_validity FROM delivery WHERE id = 1`).Scan(&validity); err != nil {
		return settings.Settings{}, fmt.Errorf("get delivery: %w", err)
	}

	s.Delivery.DefaultValidity = time.Duration(validity)

	var retention int64
	if err := q.QueryRowContext(ctx, `SELECT messages FROM retention WHERE id = 1`).Scan(&retention); err != nil {
		return settings.Settings{}, fmt.Errorf("get retention: %w", err)
	}

	s.Retention.Messages = time.Duration(retention)

	if s.Delivery.RetryIntervals, err = retryIntervals(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.Peers, err = peers(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	if s.Labels, err = memberLabels(ctx, q); err != nil {
		return settings.Settings{}, err
	}

	return s, nil
}

func retryIntervals(ctx context.Context, q querier) ([]time.Duration, error) {
	rows, err := q.QueryContext(ctx, `SELECT interval FROM retry_intervals ORDER BY attempt`)
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

func (d *DB) UpdateOperator(ctx context.Context, o settings.Operator) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdUpdateOperator, o); err != nil {
		return fmt.Errorf("update operator: %w", err)
	}

	return nil
}

func applyUpdateOperator(a *applyCtx, o settings.Operator) (struct{}, error) {
	peers, err := peers(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	labels, err := memberLabels(a.ctx, a.tx)
	if err != nil {
		return struct{}{}, err
	}

	if err := o.Conflict(peers, labels); err != nil {
		return struct{}{}, err
	}

	if _, err := a.tx.ExecContext(a.ctx, `UPDATE operator SET mcc = ?, mnc = ?, service_centre_address = ?,
		country_code = ?, national_prefix = ?, international_prefix = ? WHERE id = 1`, o.MCC, o.MNC,
		o.ServiceCentreAddress, o.Numbering.CountryCode, o.Numbering.NationalPrefix,
		o.Numbering.InternationalPrefix); err != nil {
		return struct{}{}, err
	}

	a.events.Settings = true

	return struct{}{}, nil
}

func (d *DB) UpdateDelivery(ctx context.Context, dl settings.Delivery) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdUpdateDelivery, dl); err != nil {
		return fmt.Errorf("update delivery: %w", err)
	}

	return nil
}

func applyUpdateDelivery(a *applyCtx, dl settings.Delivery) (struct{}, error) {
	ctx, tx := a.ctx, a.tx

	if _, err := tx.ExecContext(ctx, `UPDATE delivery SET default_validity = ? WHERE id = 1`,
		int64(dl.DefaultValidity)); err != nil {
		return struct{}{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM retry_intervals`); err != nil {
		return struct{}{}, err
	}

	for i, interval := range dl.RetryIntervals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO retry_intervals (attempt, interval) VALUES (?, ?)`,
			i+1, int64(interval)); err != nil {
			return struct{}{}, err
		}
	}

	a.events.Settings = true

	return struct{}{}, nil
}

func (d *DB) UpdateRetention(ctx context.Context, r settings.Retention) (err error) {
	defer d.observe(&err)()

	if _, err := submit[struct{}](ctx, d, cmdUpdateRetention, r); err != nil {
		return fmt.Errorf("update retention: %w", err)
	}

	return nil
}

func applyUpdateRetention(a *applyCtx, r settings.Retention) (struct{}, error) {
	if _, err := a.tx.ExecContext(a.ctx, `UPDATE retention SET messages = ? WHERE id = 1`, int64(r.Messages)); err != nil {
		return struct{}{}, err
	}

	a.events.Settings = true

	return struct{}{}, nil
}
