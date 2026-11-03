package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Identity struct {
	ID          int64
	UserID      int64
	Issuer      string
	Subject     string
	Email       string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

type NewIdentity struct {
	Issuer  string
	Subject string
	Email   string
	At      time.Time
}

func (d *DB) GetIdentity(ctx context.Context, issuer, subject string) (_ Identity, err error) {
	defer d.observe(&err)()

	var (
		i                    Identity
		createdAt, lastLogin int64
	)

	err = d.conn.QueryRowContext(ctx, `SELECT id, user_id, issuer, subject, email, created_at, last_login_at
		FROM user_identities WHERE issuer = ? AND subject = ?`, issuer, subject).Scan(&i.ID, &i.UserID, &i.Issuer,
		&i.Subject, &i.Email, &createdAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}

	if err != nil {
		return Identity{}, fmt.Errorf("get identity: %w", err)
	}

	i.CreatedAt = time.Unix(0, createdAt).UTC()
	i.LastLoginAt = time.Unix(0, lastLogin).UTC()

	return i, nil
}

func (d *DB) CreateIdentity(ctx context.Context, userID int64, i NewIdentity) (_ int64, err error) {
	defer d.observe(&err)()

	id, err := createIdentity(ctx, d.conn, userID, i)
	if err != nil {
		return 0, fmt.Errorf("create identity: %w", err)
	}

	return id, nil
}

func (d *DB) CreateUserWithIdentity(ctx context.Context, u NewUser, i NewIdentity, maxUsers int) (userID, identityID int64,
	err error,
) {
	defer d.observe(&err)()

	err = d.inTx(ctx, "create user with identity", func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}

		if n >= maxUsers {
			return ErrTooManyUsers
		}

		if userID, err = createUser(ctx, tx, u); err != nil {
			return err
		}

		identityID, err = createIdentity(ctx, tx, userID, i)

		return err
	})
	if err != nil {
		return 0, 0, err
	}

	return userID, identityID, nil
}

func createIdentity(ctx context.Context, conn execer, userID int64, i NewIdentity) (int64, error) {
	res, err := conn.ExecContext(ctx, `INSERT INTO user_identities
		(user_id, issuer, subject, email, created_at, last_login_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, i.Issuer, i.Subject, i.Email, i.At.UnixNano(), i.At.UnixNano())
	if isUnique(err) {
		return 0, ErrAlreadyExists
	}

	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func (d *DB) TouchIdentity(ctx context.Context, id int64, email string, at time.Time) (err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `UPDATE user_identities SET email = ?, last_login_at = ? WHERE id = ?`,
		email, at.UnixNano(), id)
	if err != nil {
		return fmt.Errorf("touch identity: %w", err)
	}

	return affectedOne(res, "touch identity")
}

func (d *DB) DeleteIdentities(ctx context.Context, userID int64) (err error) {
	defer d.observe(&err)()

	if _, err := d.conn.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete identities: %w", err)
	}

	return nil
}
