package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Role is what a user may do. The values are Ella Core's, plus Submitter.
type Role int

const (
	RoleAdmin          Role = 1
	RoleReadOnly       Role = 2
	RoleNetworkManager Role = 3
	// RoleSubmitter submits messages and reads only those it submitted.
	RoleSubmitter Role = 4
)

type User struct {
	ID    int64
	Email string
	Role  Role
	// PasswordHash is the bcrypt hash of the password.
	PasswordHash string
}

type NewUser struct {
	Email        string
	Role         Role
	PasswordHash string
}

func (d *DB) CountUsers(ctx context.Context) (_ int, err error) {
	defer d.observe(&err)()

	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}

	return n, nil
}

// CreateUser stores a user, or returns ErrAlreadyExists when one has the same email, whatever its case.
func (d *DB) CreateUser(ctx context.Context, u NewUser) (_ int64, err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `INSERT INTO users (email, role_id, password_hash) VALUES (?, ?, ?)`,
		u.Email, u.Role, u.PasswordHash)
	if isUnique(err) {
		return 0, ErrAlreadyExists
	}

	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}

	return res.LastInsertId()
}

// GetUser returns the user with an email, whatever its case.
func (d *DB) GetUser(ctx context.Context, email string) (_ User, err error) {
	defer d.observe(&err)()

	return getUser(d.conn.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

func (d *DB) GetUserByID(ctx context.Context, id int64) (_ User, err error) {
	defer d.observe(&err)()

	return getUser(d.conn.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// ListUsers returns a page of the users, oldest first, and their total.
func (d *DB) ListUsers(ctx context.Context, page, perPage int) (_ []User, _ int, err error) {
	defer d.observe(&err)()

	var total int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	rows, err := d.conn.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id LIMIT ? OFFSET ?`,
		perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	defer func() { _ = rows.Close() }()

	users := []User{}

	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list users: %w", err)
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	return users, total, nil
}

// UpdateUserRole changes the role of a user and ends all of its sessions.
func (d *DB) UpdateUserRole(ctx context.Context, id int64, role Role) (err error) {
	defer d.observe(&err)()

	return d.inTx(ctx, "update user role", func(tx *sql.Tx) error {
		if err := updateUser(ctx, tx, `UPDATE users SET role_id = ? WHERE id = ?`, role, id); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)

		return err
	})
}

// UpdateUserPassword changes the password of a user and ends all of its sessions but the one whose token hash is
// keepSession, if any.
func (d *DB) UpdateUserPassword(ctx context.Context, id int64, passwordHash string, keepSession []byte) (err error) {
	defer d.observe(&err)()

	return d.inTx(ctx, "update user password", func(tx *sql.Tx) error {
		if err := updateUser(ctx, tx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash IS NOT ?`, id, keepSession)

		return err
	})
}

// DeleteUser deletes a user with its sessions and API tokens. The messages it submitted stay, with no submitter.
func (d *DB) DeleteUser(ctx context.Context, id int64) (err error) {
	defer d.observe(&err)()

	res, err := d.conn.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}

	return affectedOne(res, "delete user")
}

const userColumns = `id, email, role_id, password_hash`

func getUser(row *sql.Row) (User, error) {
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}

	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}

	return u, nil
}

func scanUser(row rowScanner) (User, error) {
	var u User

	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.PasswordHash)

	return u, err
}

func updateUser(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}

// affectedOne returns ErrNotFound when a statement changed no row.
func affectedOne(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	if n == 0 {
		return ErrNotFound
	}

	return nil
}
