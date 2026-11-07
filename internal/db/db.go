package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-sqlite3"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrLastPasswordAdmin = errors.New("the last admin with a password")
	ErrTooManyUsers      = errors.New("too many users")
	ErrNoPassword        = errors.New("the user has no password")
)

func isUnique(err error) bool {
	var e sqlite3.Error

	return errors.As(err, &e) && (e.ExtendedCode == sqlite3.ErrConstraintUnique || e.ExtendedCode == sqlite3.ErrConstraintPrimaryKey)
}

type DB struct {
	writer      *sql.DB
	readers     atomic.Pointer[sql.DB]
	checkpoints *checkpointer
	path        string
	metrics     metrics
	proposer    Proposer
	applyMu     sync.Mutex
	applied     atomic.Uint64
	appliedMu   sync.Mutex
	appliedCh   chan struct{}
	leaderTime  atomic.Int64
	schema      atomic.Int64
	hashMu      sync.Mutex
	hashes      []appliedHash
	subscribers []func(Events)
}

const baselineSchema = 1

var migrations = []string{
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		role_id INTEGER NOT NULL CHECK (role_id IN (1, 2, 3, 4)),
		role_pinned INTEGER NOT NULL DEFAULT 0 CHECK (role_pinned IN (0, 1)),
		password_hash TEXT CHECK (password_hash != '')
	);
	CREATE TABLE user_identities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		issuer TEXT NOT NULL CHECK (issuer != ''),
		subject TEXT NOT NULL CHECK (length(CAST(subject AS BLOB)) BETWEEN 1 AND 255),
		email TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_login_at INTEGER NOT NULL,
		UNIQUE (issuer, subject),
		UNIQUE (user_id, issuer)
	);
	CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		token_hash BLOB NOT NULL UNIQUE,
		method TEXT NOT NULL CHECK (method IN ('password', 'oidc')),
		identity_id INTEGER REFERENCES user_identities (id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		CHECK ((method = 'oidc') = (identity_id IS NOT NULL))
	);
	CREATE INDEX sessions_user_id ON sessions (user_id, created_at);
	CREATE INDEX sessions_expires_at ON sessions (expires_at);
	CREATE INDEX sessions_identity_id ON sessions (identity_id);
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
	CREATE TABLE oidc (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
		external_url TEXT NOT NULL,
		issuer TEXT NOT NULL,
		client_id TEXT NOT NULL,
		client_secret TEXT NOT NULL,
		display_name TEXT NOT NULL,
		scopes TEXT NOT NULL,
		session_lifetime INTEGER NOT NULL CHECK (session_lifetime > 0),
		trust_unverified_email INTEGER NOT NULL CHECK (trust_unverified_email IN (0, 1)),
		ca_certificate TEXT NOT NULL,
		groups_claim TEXT NOT NULL,
		CHECK (enabled = 0 OR external_url != '' AND issuer != '' AND client_id != '' AND display_name != ''
			AND scopes != '')
	);
	INSERT INTO oidc VALUES (1, 0, '', '', '', '', '', 'openid email profile', 86400000000000, 0, '', '');
	CREATE TABLE oidc_group_roles (
		position INTEGER PRIMARY KEY CHECK (position > 0),
		group_name TEXT NOT NULL UNIQUE CHECK (group_name != ''),
		role_id INTEGER NOT NULL CHECK (role_id IN (1, 2, 3, 4))
	);
	CREATE TABLE oidc_cookie_key (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		key BLOB NOT NULL CHECK (length(key) >= 32)
	);
	CREATE TABLE oidc_used_states (
		state_hash BLOB PRIMARY KEY CHECK (length(state_hash) = 32),
		expires_at INTEGER NOT NULL
	) WITHOUT ROWID;
	CREATE INDEX oidc_used_states_expires_at ON oidc_used_states (expires_at);
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
		-- The W3C traceparent of the request that created the message, which its deliveries link to.
		traceparent TEXT,
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
		claim_term INTEGER,
		claim_index INTEGER,
		claimed_at INTEGER,
		alerted_at INTEGER,
		next_attempt_at INTEGER,
		updated_at INTEGER NOT NULL,
		CHECK ((claim_term IS NULL) = (claim_index IS NULL) AND (claim_term IS NULL) = (claimed_at IS NULL))
	);
	CREATE INDEX recipients_claim_term ON recipients (claim_term) WHERE claim_term IS NOT NULL;
	CREATE INDEX recipients_alert_msisdn ON recipients (alert_msisdn);
	CREATE INDEX recipients_due ON recipients (next_attempt_at, msisdn) WHERE next_attempt_at IS NOT NULL;
	CREATE TRIGGER messages_due_insert AFTER INSERT ON messages WHEN NEW.status = 'pending' BEGIN
		INSERT INTO recipients (msisdn, next_attempt_at, updated_at) VALUES (NEW.msisdn, NEW.next_attempt_at, NEW.updated_at)
		ON CONFLICT (msisdn) DO UPDATE SET next_attempt_at = COALESCE(MIN(next_attempt_at, excluded.next_attempt_at),
		excluded.next_attempt_at);
	END;
	CREATE TRIGGER messages_due_update AFTER UPDATE OF status, next_attempt_at ON messages
	WHEN OLD.status = 'pending' OR NEW.status = 'pending' BEGIN
		UPDATE recipients SET next_attempt_at = (SELECT MIN(next_attempt_at) FROM messages
		WHERE msisdn = NEW.msisdn AND status = 'pending') WHERE msisdn = NEW.msisdn;
	END;
	CREATE TRIGGER messages_due_delete AFTER DELETE ON messages WHEN OLD.status = 'pending' BEGIN
		UPDATE recipients SET next_attempt_at = (SELECT MIN(next_attempt_at) FROM messages
		WHERE msisdn = OLD.msisdn AND status = 'pending') WHERE msisdn = OLD.msisdn;
	END;
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
		claimed_by TEXT,
		result TEXT CHECK (result IN ('acknowledged', 'rejected', 'expired', 'no_user_data')),
		UNIQUE (message_id, type),
		CHECK (type = 'receipt' AND tp_status IS NOT NULL AND attempt_id IS NULL
			OR type = 'sim_answer' AND attempt_id IS NOT NULL AND tp_status IS NULL)
	);
	CREATE INDEX smpp_notifications_due ON smpp_notifications (next_attempt_at) WHERE result IS NULL;
	CREATE INDEX smpp_notifications_message_id ON smpp_notifications (message_id);
	CREATE TABLE smpp_sessions (
		node_id TEXT NOT NULL,
		session_id INTEGER NOT NULL,
		esme_id INTEGER NOT NULL REFERENCES esmes (id) ON DELETE CASCADE,
		bind_type TEXT NOT NULL CHECK (bind_type IN ('transmitter', 'receiver', 'transceiver')),
		remote TEXT NOT NULL,
		bound_at INTEGER NOT NULL,
		PRIMARY KEY (node_id, session_id)
	) WITHOUT ROWID;
	CREATE INDEX smpp_sessions_esme_id ON smpp_sessions (esme_id);
	CREATE TABLE fsm_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		applied_index INTEGER NOT NULL,
		applied_term INTEGER NOT NULL,
		hash BLOB NOT NULL,
		leader_time INTEGER NOT NULL,
		configuration BLOB,
		configuration_index INTEGER NOT NULL,
		durable_writes INTEGER NOT NULL
	);
	INSERT INTO fsm_state VALUES (1, 0, 0, zeroblob(32), 0, NULL, 0, 0);
	CREATE TABLE applied_requests (
		request_id BLOB PRIMARY KEY CHECK (length(request_id) = 16),
		result BLOB NOT NULL,
		applied_at INTEGER NOT NULL
	) WITHOUT ROWID;
	CREATE INDEX applied_requests_applied_at ON applied_requests (applied_at);
	CREATE TABLE cluster (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		cluster_id TEXT NOT NULL CHECK (length(cluster_id) = 36),
		join_key BLOB NOT NULL CHECK (length(join_key) >= 32)
	);
	CREATE TABLE cluster_members (
		node_id TEXT PRIMARY KEY CHECK (length(node_id) = 36),
		address TEXT NOT NULL,
		fingerprint TEXT UNIQUE,
		schema_version INTEGER NOT NULL,
		version TEXT NOT NULL,
		joined_at INTEGER NOT NULL,
		drain_state TEXT NOT NULL DEFAULT 'active' CHECK (drain_state IN ('active', 'draining', 'drained')),
		display_name TEXT NOT NULL DEFAULT '',
		diameter_label TEXT NOT NULL UNIQUE CHECK (length(diameter_label) BETWEEN 1 AND 63),
		label_generated INTEGER NOT NULL CHECK (label_generated IN (0, 1))
	);
	CREATE UNIQUE INDEX cluster_members_address ON cluster_members (address) WHERE address != '';
	CREATE TABLE cluster_removed_members (
		node_id TEXT PRIMARY KEY CHECK (length(node_id) = 36),
		fingerprint TEXT,
		removed_at INTEGER NOT NULL,
		reason TEXT NOT NULL CHECK (reason IN ('drained', 'forced'))
	);
	CREATE TABLE cluster_join_tokens (
		token_id TEXT PRIMARY KEY CHECK (token_id != ''),
		expires_at INTEGER NOT NULL,
		node_id TEXT,
		fingerprint TEXT,
		CHECK ((node_id IS NULL) = (fingerprint IS NULL))
	);`,
}

type Option func(*DB)

func WithClock(now func() time.Time) Option {
	return func(d *DB) {
		if l, ok := d.proposer.(*localProposer); ok {
			l.now = now
		}
	}
}

func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	if err := recoverInstall(path); err != nil {
		return nil, fmt.Errorf("open database: recover an interrupted install: %w", err)
	}

	if err := restrict(path); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	d := &DB{path: path, metrics: newMetrics()}

	if err := d.openConns(ctx); err != nil {
		return nil, err
	}

	state, err := readFSMState(ctx, d.writer)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("open database: read state: %w", err)
	}

	d.setApplied(state.appliedIndex)
	d.leaderTime.Store(state.leaderTime)
	d.proposer = &localProposer{d: d, now: time.Now, last: state.leaderTime}

	for _, opt := range opts {
		opt(d)
	}

	return d, nil
}

const dsnParams = "_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_stmt_cache_size=64"

func (d *DB) openConns(ctx context.Context) error {
	writer, err := sql.Open("sqlite3", "file:"+d.path+"?"+dsnParams+"&_txlock=immediate")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)

	if _, err := writer.ExecContext(ctx, "PRAGMA wal_autocheckpoint = 0"); err != nil {
		_ = writer.Close()
		return fmt.Errorf("open database: %w", err)
	}

	if _, err := writer.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		_ = writer.Close()
		return fmt.Errorf("open database: %w", err)
	}

	d.writer = writer

	if err := d.migrate(ctx); err != nil {
		_ = writer.Close()
		return err
	}

	reader, err := sql.Open("sqlite3", "file:"+d.path+"?"+dsnParams+"&_query_only=1")
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("open database: %w", err)
	}

	reader.SetMaxOpenConns(readConns)
	reader.SetMaxIdleConns(readConns)

	if err := reader.PingContext(ctx); err != nil {
		_ = writer.Close()
		_ = reader.Close()

		return fmt.Errorf("open database: %w", err)
	}

	checkpoints, err := startCheckpointer(ctx, d.path, writer)
	if err != nil {
		_ = writer.Close()
		_ = reader.Close()

		return fmt.Errorf("open database: %w", err)
	}

	d.checkpoints = checkpoints

	if old := d.readers.Swap(reader); old != nil {
		_ = old.Close()
	}

	return nil
}

func (d *DB) closeConns() error {
	return errors.Join(d.checkpoints.stop(), d.reader().Close(), d.writer.Close())
}

func (d *DB) reader() *sql.DB {
	return d.readers.Load()
}

const readConns = 8

func (d *DB) Ping(ctx context.Context) error {
	return d.reader().PingContext(ctx)
}

func (d *DB) Close() error {
	return d.closeConns()
}

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}

	d.schema.Store(int64(version))

	if version > 0 {
		return nil
	}

	for i := range baselineSchema {
		tx, err := d.writer.BeginTx(ctx, nil)
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

		d.schema.Store(int64(i + 1))
	}

	return nil
}

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
