package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrUpgradePending = fmt.Errorf("%w: the cluster has not been upgraded to support this yet", ErrBusy)

var minSchema = map[CommandType]int{
	cmdCloseMODelivery:    routingSchema,
	cmdRetryMODelivery:    routingSchema,
	cmdReleaseMODelivery:  routingSchema,
	cmdExpireMODeliveries: routingSchema,
}

func commandSchema(t CommandType) int {
	if s, ok := minSchema[t]; ok {
		return s
	}

	return baselineSchema
}

type migrate struct {
	Target int `json:"target"`
}

func (d *DB) Migrate(ctx context.Context, target int) (err error) {
	defer d.observe(&err)()

	_, err = submit[struct{}](ctx, d, cmdMigrate, migrate{Target: target})

	return err
}

func applyMigrate(a *applyCtx, p migrate) (struct{}, error) {
	var current int

	if err := a.tx.QueryRowContext(a.ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return struct{}{}, err
	}

	switch {
	case p.Target <= current:
		return struct{}{}, nil
	case p.Target != current+1:
		return struct{}{}, fmt.Errorf("%w: migration to schema %d from %d", ErrInvalid, p.Target, current)
	case p.Target > len(migrations):
		return struct{}{}, errors.New("the log needs a newer binary: schema " + fmt.Sprint(p.Target))
	}

	if err := runMigration(a.ctx, a.tx, p.Target); err != nil {
		return struct{}{}, fmt.Errorf("migration %d: %w", p.Target, err)
	}

	if _, err := a.tx.ExecContext(a.ctx, fmt.Sprintf("PRAGMA user_version = %d", p.Target)); err != nil {
		return struct{}{}, err
	}

	a.events.Migrated = p.Target

	return struct{}{}, nil
}

var schemaEdits = map[int]func(ctx context.Context, tx *sql.Tx) error{
	routingSchema: allowDelegatedStatus,
}

func runMigration(ctx context.Context, tx *sql.Tx, target int) error {
	if edit, ok := schemaEdits[target]; ok {
		if err := edit(ctx, tx); err != nil {
			return err
		}
	}

	_, err := tx.ExecContext(ctx, migrations[target-1])

	return err
}

const (
	statusCheck          = `CHECK (status IN ('pending', 'delivered', 'failed', 'expired', 'replaced'))`
	delegatedStatusCheck = `CHECK (status IN ('pending', 'delivered', 'failed', 'expired', 'replaced', 'delegated'))`
)

func allowDelegatedStatus(ctx context.Context, tx *sql.Tx) error {
	var table string

	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'messages'`).
		Scan(&table); err != nil {
		return fmt.Errorf("read the messages table: %w", err)
	}

	if n := strings.Count(table, statusCheck); n != 1 {
		return fmt.Errorf("the messages table has %d status checks to widen, want 1", n)
	}

	if err := rewriteTable(ctx, tx, "messages", strings.Replace(table, statusCheck, delegatedStatusCheck, 1)); err != nil {
		return fmt.Errorf("widen the status check of messages: %w", err)
	}

	var widened string

	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'messages'`).
		Scan(&widened); err != nil {
		return fmt.Errorf("read the widened messages table: %w", err)
	}

	if !strings.Contains(widened, delegatedStatusCheck) {
		return errors.New("the messages table was not widened")
	}

	if _, err := tx.ExecContext(ctx, `SELECT COUNT(*) FROM messages WHERE status = 'delegated' AND 0`); err != nil {
		return fmt.Errorf("the widened messages table does not parse: %w", err)
	}

	return nil
}

func rewriteTable(ctx context.Context, tx *sql.Tx, name, definition string) (err error) {
	var version int

	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&version); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return err
	}

	defer func() {
		if _, reset := tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA writable_schema = RESET`); reset != nil {
			err = errors.Join(err, fmt.Errorf("reset the schema: %w", reset))
		}
	}()

	res, err := tx.ExecContext(ctx, `UPDATE sqlite_schema SET sql = ? WHERE type = 'table' AND name = ?`, definition, name)
	if err != nil {
		return err
	}

	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("%d tables named %s", n, name)
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, version+1)); err != nil {
		return err
	}

	var bumped int

	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&bumped); err != nil {
		return err
	}

	if bumped != version+1 {
		return fmt.Errorf("schema_version is %d after setting it to %d", bumped, version+1)
	}

	return nil
}
