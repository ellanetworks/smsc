package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "smsc.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfig(t, "db:\n  path: /var/lib/smsc/smsc.db\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DB.Path != "/var/lib/smsc/smsc.db" {
		t.Fatalf("db.path = %q", cfg.DB.Path)
	}
}

func TestLoadRejectsMissingDBPath(t *testing.T) {
	if _, err := Load(writeConfig(t, "db: {}\n")); err == nil {
		t.Fatal("expected an error for a missing db.path")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	if _, err := Load(writeConfig(t, "db:\n  path: smsc.db\nunknown: true\n")); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
