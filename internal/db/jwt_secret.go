package db

import (
	"context"
	"crypto/rand"
	"fmt"
)

const (
	jwtSecretLength     = 32
	oidcCookieKeyLength = 32
)

type Secrets struct {
	JWT           []byte
	OIDCCookieKey []byte
}

func (d *DB) Secrets(ctx context.Context) (_ Secrets, err error) {
	defer d.observe(&err)()

	candidate := Secrets{JWT: make([]byte, jwtSecretLength), OIDCCookieKey: make([]byte, oidcCookieKeyLength)}
	_, _ = rand.Read(candidate.JWT)
	_, _ = rand.Read(candidate.OIDCCookieKey)

	s, err := submit[Secrets](ctx, d, cmdInitSecrets, candidate)
	if err != nil {
		return Secrets{}, fmt.Errorf("init secrets: %w", err)
	}

	return s, nil
}

func (d *DB) JWTSecret(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.JWT, err
}

func (d *DB) OIDCCookieKey(ctx context.Context) ([]byte, error) {
	s, err := d.Secrets(ctx)
	return s.OIDCCookieKey, err
}

func applyInitSecrets(a *applyCtx, candidate Secrets) (Secrets, error) {
	ctx, tx := a.ctx, a.tx

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jwt_secret (id, secret) VALUES (1, ?)`,
		candidate.JWT); err != nil {
		return Secrets{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO oidc_cookie_key (id, key) VALUES (1, ?)`,
		candidate.OIDCCookieKey); err != nil {
		return Secrets{}, err
	}

	var s Secrets

	if err := tx.QueryRowContext(ctx, `SELECT secret FROM jwt_secret WHERE id = 1`).Scan(&s.JWT); err != nil {
		return Secrets{}, err
	}

	if err := tx.QueryRowContext(ctx, `SELECT key FROM oidc_cookie_key WHERE id = 1`).Scan(&s.OIDCCookieKey); err != nil {
		return Secrets{}, err
	}

	return s, nil
}
