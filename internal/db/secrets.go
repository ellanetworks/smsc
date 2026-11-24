package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/google/uuid"
)

const (
	jwtSecretLength     = 32
	oidcCookieKeyLength = 32
	joinKeyLength       = 32
)

var (
	jwtSecretAt    = encryption.Location{Table: "jwt_secret", Column: "secret"}
	cookieKeyAt    = encryption.Location{Table: "oidc_cookie_key", Column: "key"}
	joinKeyAt      = encryption.Location{Table: "cluster", Column: "join_key"}
	clientSecretAt = encryption.Location{Table: "oidc", Column: "client_secret"}
	ErrKeysLocked  = fmt.Errorf("%w: %w", ErrBusy, encryption.ErrLocked)
)

type Secrets struct {
	JWT           []byte
	OIDCCookieKey []byte
	ClusterID     string
	JoinKey       []byte
}

type secretRow struct {
	JWT           []byte
	OIDCCookieKey []byte
	ClusterID     string
	JoinKey       []byte
}

var secretKeys = []struct {
	at     encryption.Location
	length int
	field  func(*secretRow) *[]byte
}{
	{jwtSecretAt, jwtSecretLength, func(s *secretRow) *[]byte { return &s.JWT }},
	{cookieKeyAt, oidcCookieKeyLength, func(s *secretRow) *[]byte { return &s.OIDCCookieKey }},
	{joinKeyAt, joinKeyLength, func(s *secretRow) *[]byte { return &s.JoinKey }},
}

func (d *DB) SetKeys(k *encryption.Keys) {
	d.keys = k
}

func (d *DB) unlocked() bool {
	return d.keys != nil && d.keys.Unlocked()
}

func (d *DB) Secrets(ctx context.Context) (_ Secrets, err error) {
	defer d.observe(&err)()

	raw, err := readSecretRow(ctx, d.reader())
	if err != nil {
		return Secrets{}, err
	}

	var plain secretRow

	for _, k := range secretKeys {
		v, err := d.openSecret(*k.field(&raw), k.at)
		if err != nil {
			return Secrets{}, fmt.Errorf("read the %s: %w", k.at.Table, err)
		}

		if len(v) != k.length {
			return Secrets{}, fmt.Errorf("read the %s: %w: %d bytes, want %d", k.at.Table, encryption.ErrMalformed,
				len(v), k.length)
		}

		*k.field(&plain) = v
	}

	return Secrets{JWT: plain.JWT, OIDCCookieKey: plain.OIDCCookieKey, ClusterID: raw.ClusterID, JoinKey: plain.JoinKey},
		nil
}

func (d *DB) ClusterID(ctx context.Context) (_ string, err error) {
	defer d.observe(&err)()

	return readClusterID(ctx, d.reader())
}

func (d *DB) JWTSecret(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.JWT, err
}

func (d *DB) OIDCCookieKey(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.OIDCCookieKey, err
}

type createCluster struct {
	secretRow
	Keys clusterKeys
}

func (d *DB) CreateCluster(ctx context.Context, sealed encryption.Sealed) (string, error) {
	return d.CreateClusterVia(ctx, d.proposer, sealed)
}

func (d *DB) CreateClusterVia(ctx context.Context, via Proposer, sealed encryption.Sealed) (_ string, err error) {
	defer d.observe(&err)()

	if !d.unlocked() {
		return "", ErrKeysLocked
	}

	term, err := d.keys.ActiveTerm()
	if err != nil {
		return "", err
	}

	p := createCluster{secretRow: secretRow{ClusterID: uuid.Must(uuid.NewV7()).String()}, Keys: newClusterKeys(sealed, term)}

	for _, k := range secretKeys {
		value := make([]byte, k.length)
		_, _ = rand.Read(value)

		sealed, err := d.keys.Encrypt(value, k.at)
		clear(value)

		if err != nil {
			return "", fmt.Errorf("create the cluster: %w", err)
		}

		*k.field(&p.secretRow) = sealed
	}

	if err := p.Keys.validate(); err != nil {
		return "", err
	}

	id, _, err := submitVia[string](ctx, via, cmdCreateCluster, p)
	if err != nil {
		return "", fmt.Errorf("create the cluster: %w", err)
	}

	return id, nil
}

func applyCreateCluster(a *applyCtx, p createCluster) (string, error) {
	ctx, tx := a.ctx, a.tx

	if _, err := readSecretRow(ctx, tx); err == nil {
		return readClusterID(ctx, tx)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	for _, k := range secretKeys {
		if err := requireSealed(*k.field(&p.secretRow)); err != nil {
			return "", fmt.Errorf("%w: the %s", err, k.at.Table)
		}
	}

	if err := insertClusterKeys(a, p.Keys); err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO jwt_secret (id, secret) VALUES (1, ?)`, p.JWT); err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_cookie_key (id, key) VALUES (1, ?)`, p.OIDCCookieKey); err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO cluster (id, cluster_id, join_key) VALUES (1, ?, ?)`,
		p.ClusterID, p.JoinKey); err != nil {
		return "", err
	}

	return p.ClusterID, nil
}

func readClusterID(ctx context.Context, q querier) (string, error) {
	var id string

	err := q.QueryRowContext(ctx, `SELECT cluster_id FROM cluster WHERE id = 1`).Scan(&id)

	return id, err
}

func readSecretRow(ctx context.Context, q querier) (secretRow, error) {
	var s secretRow

	err := q.QueryRowContext(ctx, `SELECT j.secret, o.key, c.cluster_id, c.join_key
		FROM jwt_secret j, oidc_cookie_key o, cluster c WHERE j.id = 1 AND o.id = 1 AND c.id = 1`).
		Scan(&s.JWT, &s.OIDCCookieKey, &s.ClusterID, &s.JoinKey)

	return s, err
}

func (d *DB) sealSecret(plaintext []byte, at encryption.Location) ([]byte, error) {
	if !d.unlocked() {
		return nil, ErrKeysLocked
	}

	return d.keys.Encrypt(plaintext, at)
}

func (d *DB) openSecret(raw []byte, at encryption.Location) ([]byte, error) {
	if !d.unlocked() {
		return nil, ErrKeysLocked
	}

	return d.keys.Decrypt(raw, at)
}

func requireSealed(v []byte) error {
	if v == nil {
		return nil
	}

	if _, err := encryption.Term(v); err != nil {
		return fmt.Errorf("%w: a value is not encrypted: %w", ErrInvalid, err)
	}

	return nil
}
