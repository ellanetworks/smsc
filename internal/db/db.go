package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/mattn/go-sqlite3"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

// isUnique reports whether err is the violation of a UNIQUE constraint.
func isUnique(err error) bool {
	var e sqlite3.Error

	return errors.As(err, &e) && e.ExtendedCode == sqlite3.ErrConstraintUnique
}

type DB struct {
	conn *sql.DB
	// path is the database file, whose size the metrics report.
	path    string
	metrics metrics
}

var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		role_id INTEGER NOT NULL CHECK (role_id IN (1, 2, 3, 4)),
		password_hash TEXT NOT NULL
	);
	CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		token_hash BLOB NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX sessions_user_id ON sessions (user_id, created_at);
	CREATE INDEX sessions_expires_at ON sessions (expires_at);
	CREATE TABLE api_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		token_hash BLOB NOT NULL,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER,
		last_used_at INTEGER,
		UNIQUE (user_id, name)
	);
	CREATE TABLE jwt_secret (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		secret BLOB NOT NULL CHECK (length(secret) >= 32)
	);
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL CHECK (kind IN ('message', 'status_report')),
		report_for INTEGER REFERENCES messages (id) ON DELETE CASCADE,
		report_status INTEGER CHECK (report_status BETWEEN 0 AND 255),
		originator TEXT NOT NULL,
		originator_ton INTEGER NOT NULL,
		originator_npi INTEGER NOT NULL,
		recipient TEXT NOT NULL,
		recipient_ton INTEGER NOT NULL,
		recipient_npi INTEGER NOT NULL,
		msisdn TEXT NOT NULL,
		origin TEXT NOT NULL CHECK (origin IN ('mobile', 'api', 'smpp')),
		submitted_by INTEGER REFERENCES users (id) ON DELETE SET NULL,
		message_reference INTEGER NOT NULL,
		protocol_identifier INTEGER NOT NULL,
		tpdu BLOB NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending', 'delivered', 'failed', 'expired', 'replaced')),
		single_shot INTEGER NOT NULL CHECK (single_shot IN (0, 1)),
		submitted_at INTEGER NOT NULL,
		time_stamp INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		next_attempt_at INTEGER NOT NULL,
		retries INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		CHECK (kind = 'message' AND report_for IS NULL AND report_status IS NULL
			OR kind = 'status_report' AND report_for IS NOT NULL AND report_status IS NOT NULL),
		CHECK (kind = 'message' OR status != 'replaced'),
		CHECK (origin IN ('api', 'smpp') OR submitted_by IS NULL)
	);
	CREATE INDEX messages_due ON messages (status, next_attempt_at);
	CREATE INDEX messages_report_for ON messages (report_for);
	CREATE INDEX messages_originator ON messages (originator, originator_ton, originator_npi, id);
	CREATE INDEX messages_msisdn ON messages (msisdn, status, next_attempt_at);
	CREATE INDEX messages_submitted_by ON messages (submitted_by, id);
	CREATE INDEX messages_submitted_at ON messages (submitted_at);
	CREATE INDEX messages_purgeable ON messages (updated_at) WHERE kind = 'message' AND status != 'pending';
	CREATE TABLE delivery_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
		started_at INTEGER NOT NULL,
		completed_at INTEGER NOT NULL,
		step TEXT NOT NULL CHECK (step IN ('routing', 'delivery')),
		node TEXT NOT NULL,
		node_type TEXT CHECK (node_type IN ('mme', 'sgsn', 'ip_sm_gw', 'smsf_3gpp', 'smsf_non_3gpp')),
		outcome TEXT NOT NULL,
		result_code INTEGER,
		vendor_id INTEGER,
		failure_cause TEXT,
		tp_failure_cause TEXT,
		absent_user_diagnostic_mme TEXT,
		absent_user_diagnostic_msc TEXT,
		absent_user_diagnostic_sgsn TEXT,
		absent_user_diagnostic_ip_sm_gw TEXT,
		absent_user_diagnostic_smsf_3gpp TEXT,
		absent_user_diagnostic_smsf_non_3gpp TEXT,
		deliver_report BLOB
	);
	CREATE INDEX delivery_attempts_message_id ON delivery_attempts (message_id);
	CREATE TABLE recipients (
		msisdn TEXT PRIMARY KEY,
		alert_msisdn TEXT,
		held_until INTEGER,
		last_time_stamp INTEGER,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX recipients_alert_msisdn ON recipients (alert_msisdn);
	CREATE TABLE operator (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		mcc TEXT NOT NULL,
		mnc TEXT NOT NULL,
		service_centre_address TEXT NOT NULL,
		country_code TEXT NOT NULL,
		national_prefix TEXT NOT NULL,
		international_prefix TEXT NOT NULL
	);
	INSERT INTO operator VALUES (1, '001', '01', '15550000000', '1', '1', '011');
	CREATE TABLE delivery (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		default_validity INTEGER NOT NULL CHECK (default_validity > 0)
	);
	INSERT INTO delivery VALUES (1, 604800000000000);
	CREATE TABLE retry_intervals (
		attempt INTEGER PRIMARY KEY CHECK (attempt > 0),
		interval INTEGER NOT NULL CHECK (interval > 0)
	);
	INSERT INTO retry_intervals VALUES (1, 60000000000), (2, 300000000000), (3, 900000000000), (4, 3600000000000);
	CREATE TABLE retention (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		messages INTEGER NOT NULL CHECK (messages >= 86400000000000)
	);
	INSERT INTO retention VALUES (1, 2592000000000000);
	CREATE TABLE diameter_peers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		host TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
		port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535)
	);
	CREATE UNIQUE INDEX diameter_peers_host ON diameter_peers (host) WHERE host != '';
	CREATE TABLE diameter_peer_addresses (
		peer_id INTEGER NOT NULL REFERENCES diameter_peers (id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		address TEXT NOT NULL,
		PRIMARY KEY (peer_id, position),
		UNIQUE (peer_id, address)
	);
	CREATE TABLE diameter_routes (
		peer_id INTEGER NOT NULL REFERENCES diameter_peers (id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		realm TEXT NOT NULL COLLATE NOCASE,
		application TEXT NOT NULL CHECK (application IN ('s6c', 'sgd')),
		priority INTEGER NOT NULL CHECK (priority >= 0),
		PRIMARY KEY (peer_id, position),
		UNIQUE (peer_id, realm, application),
		-- An empty realm is the default route, which only SGd takes.
		CHECK (realm != '' OR application = 'sgd')
	);
	CREATE TABLE esmes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		system_id TEXT NOT NULL UNIQUE CHECK (length(system_id) BETWEEN 1 AND 15),
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		password_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX esmes_user_id ON esmes (user_id);
	CREATE TABLE esme_networks (
		esme_id INTEGER NOT NULL REFERENCES esmes (id) ON DELETE CASCADE,
		prefix TEXT NOT NULL,
		PRIMARY KEY (esme_id, prefix)
	);
	CREATE TABLE smpp_messages (
		message_id INTEGER PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
		esme_id INTEGER NOT NULL REFERENCES esmes (id) ON DELETE CASCADE,
		-- registered_delivery bits 1-0 (SMPP 3.4 section 5.2.17).
		receipt INTEGER NOT NULL CHECK (receipt IN (0, 1, 2)),
		sim_answer INTEGER NOT NULL CHECK (sim_answer IN (0, 1)),
		-- source_addr_npi as submitted, which the notifications echo (SMPP 3.4 section 2.11).
		source_npi INTEGER NOT NULL CHECK (source_npi BETWEEN 0 AND 255),
		data_coding INTEGER NOT NULL CHECK (data_coding BETWEEN 0 AND 255),
		user_message_reference INTEGER CHECK (user_message_reference BETWEEN 0 AND 65535)
	);
	CREATE INDEX smpp_messages_esme_id ON smpp_messages (esme_id);
	CREATE TABLE smpp_notifications (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		-- The ESME is the message's, in smpp_messages.
		message_id INTEGER NOT NULL REFERENCES smpp_messages (message_id) ON DELETE CASCADE,
		type TEXT NOT NULL CHECK (type IN ('receipt', 'sim_answer')),
		-- A receipt's TP-Status (TS 23.040 clause 9.2.3.15). Its message_state is the message's status.
		tp_status INTEGER,
		-- A SIM answer's delivery attempt, whose SMS-DELIVER-REPORT it forwards.
		attempt_id INTEGER REFERENCES delivery_attempts (id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL,
		next_attempt_at INTEGER NOT NULL,
		attempts INTEGER NOT NULL,
		result TEXT CHECK (result IN ('acknowledged', 'rejected', 'expired', 'no_user_data')),
		UNIQUE (message_id, type),
		CHECK (type = 'receipt' AND tp_status IS NOT NULL AND attempt_id IS NULL
			OR type = 'sim_answer' AND attempt_id IS NOT NULL AND tp_status IS NULL)
	);
	CREATE INDEX smpp_notifications_due ON smpp_notifications (next_attempt_at) WHERE result IS NULL;
	CREATE INDEX smpp_notifications_message_id ON smpp_notifications (message_id);`,
}

func Open(ctx context.Context, path string) (*DB, error) {
	if err := restrict(path); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_stmt_cache_size=64")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	d := &DB{conn: conn, path: path, metrics: newMetrics()}
	if err := d.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return d, nil
}

// Ping checks that the database answers. It is not observed: see TestEveryCallIsObserved.
func (d *DB) Ping(ctx context.Context) error {
	return d.conn.PingContext(ctx)
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

// restrict creates the database file if absent, readable and writable by its owner only, and makes an existing one so:
// it holds the password hashes and the key that signs the access tokens. SQLite gives its journal files the
// permissions of the database file.
func restrict(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
}
