package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

var ErrNotFound = errors.New("not found")

type DB struct {
	conn *sql.DB
}

var migrations = []string{
	`CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		originator TEXT NOT NULL,
		originator_ton INTEGER NOT NULL,
		originator_npi INTEGER NOT NULL,
		recipient TEXT NOT NULL,
		recipient_ton INTEGER NOT NULL,
		recipient_npi INTEGER NOT NULL,
		msisdn TEXT NOT NULL,
		origin TEXT NOT NULL CHECK (origin IN ('mobile', 'api')),
		message_reference INTEGER NOT NULL,
		protocol_identifier INTEGER NOT NULL,
		tpdu BLOB NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending', 'delivered', 'failed', 'expired')),
		single_shot INTEGER NOT NULL CHECK (single_shot IN (0, 1)),
		submitted_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		next_attempt_at INTEGER NOT NULL,
		retries INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX messages_due ON messages (status, next_attempt_at);
	CREATE INDEX messages_originator ON messages (originator, originator_ton, originator_npi, id);
	CREATE INDEX messages_msisdn ON messages (msisdn, status, next_attempt_at);
	CREATE TABLE delivery_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
		attempted_at INTEGER NOT NULL,
		step TEXT NOT NULL CHECK (step IN ('routing', 'delivery')),
		node TEXT NOT NULL,
		outcome TEXT NOT NULL,
		result_code INTEGER,
		vendor_id INTEGER,
		failure_cause TEXT,
		tp_failure_cause TEXT,
		absent_diagnostic TEXT,
		absent_diagnostic_mme TEXT,
		absent_diagnostic_msc TEXT,
		absent_diagnostic_sgsn TEXT,
		absent_diagnostic_smsf_3gpp TEXT,
		absent_diagnostic_smsf_non_3gpp TEXT
	);
	CREATE INDEX delivery_attempts_message_id ON delivery_attempts (message_id);
	CREATE TABLE recipients (
		msisdn TEXT PRIMARY KEY,
		alert_msisdn TEXT,
		held_until INTEGER,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX recipients_alert_msisdn ON recipients (alert_msisdn);`,
}

func Open(ctx context.Context, path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	d := &DB{conn: conn}
	if err := d.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return d, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := d.conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}

	return nil
}
