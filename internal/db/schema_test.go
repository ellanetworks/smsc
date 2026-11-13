package db

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func withMigration(t *testing.T, sql string) {
	t.Helper()

	saved := migrations
	migrations = append(slices.Clone(migrations), sql)

	t.Cleanup(func() { migrations = saved })
}

func TestSchemaAdvancesThroughTheLog(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	withMigration(t, `CREATE TABLE upgraded (id INTEGER PRIMARY KEY)`)

	if d.Schema() != baselineSchema {
		t.Fatalf("a new binary migrated an existing database at open: schema %d", d.Schema())
	}

	if err := d.Migrate(ctx, 3); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a skipped schema = %v, want %v", err, ErrInvalid)
	}

	if err := d.Migrate(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := d.Migrate(ctx, 2); err != nil || d.Schema() != 2 {
		t.Fatalf("a replayed migration = %v, schema %d", err, d.Schema())
	}

	if _, err := d.writer.ExecContext(ctx, `INSERT INTO upgraded (id) VALUES (1)`); err != nil {
		t.Fatalf("the migration did not run: %v", err)
	}
}

func TestMigrationBeyondTheBinaryHalts(t *testing.T) {
	d := openTestDB(t)

	raw, err := json.Marshal(migrate{Target: baselineSchema + 1})
	if err != nil {
		t.Fatal(err)
	}

	entry, err := EncodeBatch(Batch{LeaderTime: 1, Commands: []Command{{Type: cmdMigrate, Version: 1, Payload: raw}}})
	if err != nil {
		t.Fatal(err)
	}

	var fatal *FatalError
	if _, err := d.Apply(d.AppliedIndex()+1, 1, entry); !errors.As(err, &fatal) {
		t.Fatalf("a migration this binary lacks = %v, want a fatal error", err)
	}
}

func TestCommandsWaitForTheirSchema(t *testing.T) {
	d := openTestDB(t)

	minSchema[cmdDeleteExpiredSessions] = baselineSchema + 1

	t.Cleanup(func() { delete(minSchema, cmdDeleteExpiredSessions) })

	if err := d.DeleteExpiredSessions(context.Background(), time.Now()); !errors.Is(err, ErrUpgradePending) {
		t.Fatalf("a command above the schema = %v, want %v", err, ErrUpgradePending)
	}
}
