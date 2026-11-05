package db

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func databaseAt(t *testing.T, path string, index uint64) {
	t.Helper()

	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.RecordConfiguration(index, 1, nil); err != nil {
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func appliedAfterOpen(t *testing.T, path string) uint64 {
	t.Helper()

	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	if _, err := os.Stat(replacedPath(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the replaced database is still there: %v", err)
	}

	return d.AppliedIndex()
}

func TestOpenRestoresADatabaseLeftAsideByAnInterruptedInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smsc.db")

	databaseAt(t, path, 7)

	if err := os.Rename(path, replacedPath(path)); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path+"-wal", []byte("not a WAL of the old database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := appliedAfterOpen(t, path); got != 7 {
		t.Fatalf("applied index %d after recovery, want 7", got)
	}
}

func TestOpenKeepsTheDatabaseOfAnInstallInterruptedAfterItsRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smsc.db")

	databaseAt(t, path, 7)
	databaseAt(t, filepath.Join(dir, "install.db"), 12)

	if err := os.Rename(path, replacedPath(path)); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(filepath.Join(dir, "install.db"), path); err != nil {
		t.Fatal(err)
	}

	if got := appliedAfterOpen(t, path); got != 12 {
		t.Fatalf("applied index %d after recovery, want 12", got)
	}
}
