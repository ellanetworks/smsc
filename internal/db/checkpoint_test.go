package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fastCheckpoints(t *testing.T) {
	previous := checkpointInterval
	checkpointInterval = 5 * time.Millisecond

	t.Cleanup(func() { checkpointInterval = previous })
}

func openBench(t *testing.T) *applyBench {
	ctx := context.Background()
	d := openTestDB(t)

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	user, err := d.CreateUser(ctx, NewUser{Email: "esme@example.com", Role: RoleSubmitter, PasswordHash: "h"}, 10)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateESME(ctx, ESME{SystemID: "esme", UserID: user, PasswordHash: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	return &applyBench{d: d, index: d.AppliedIndex(), now: now}
}

func walFrames(t *testing.T, d *DB) int {
	info, err := os.Stat(d.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}

	var pageSize int
	if err := d.reader().QueryRowContext(context.Background(), "PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}

	return int(info.Size()) / (pageSize + 24)
}

func stopped(c *checkpointer) bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func TestCheckpointerKeepsWALBounded(t *testing.T) {
	fastCheckpoints(t)

	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.writer.ExecContext(ctx, "CREATE TABLE filler (b BLOB)"); err != nil {
		t.Fatal(err)
	}

	peak := 0

	for range 6 * walCheckpointPages / 10 {
		if _, err := d.writer.ExecContext(ctx, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 10) INSERT INTO filler SELECT randomblob(4000) FROM n"); err != nil {
			t.Fatal(err)
		}

		peak = max(peak, walFrames(t, d))
	}

	if peak > walCheckpointPages*3 {
		t.Fatalf("WAL reached %d frames, want at most %d", peak, walCheckpointPages*3)
	}
}

func TestCheckpointerSyncsCheckpoints(t *testing.T) {
	d := openTestDB(t)

	var level int
	if err := d.checkpoints.conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&level); err != nil {
		t.Fatal(err)
	}

	if level < 1 {
		t.Fatalf("checkpoint connection synchronous = %d, want NORMAL or FULL", level)
	}
}

func TestCheckpointerStopsOnClose(t *testing.T) {
	fastCheckpoints(t)

	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	c := d.checkpoints

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if !stopped(c) {
		t.Fatal("checkpointer still running after Close")
	}
}

func TestCheckpointerRestartsOnInstall(t *testing.T) {
	fastCheckpoints(t)

	ctx := context.Background()
	a := openBench(t)

	for range 50 {
		a.apply(t, a.entry())
	}

	for range 3 {
		c, err := a.d.CopyTo(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		before := a.d.checkpoints

		if err := a.d.Install(ctx, c.Path, nil); err != nil {
			t.Fatal(err)
		}

		if !stopped(before) {
			t.Fatal("previous checkpointer still running after Install")
		}

		if stopped(a.d.checkpoints) {
			t.Fatal("no checkpointer running after Install")
		}

		a.index = a.d.AppliedIndex()

		for range 20 {
			a.apply(t, a.entry())
		}
	}
}
