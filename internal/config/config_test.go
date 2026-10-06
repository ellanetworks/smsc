package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validConfig = `logging:
  level: debug
db:
  path: /var/lib/smsc/smsc.db
api:
  address: 127.0.0.1
  port: 8080
diameter:
  address: 192.0.2.10
  port: 3869
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "smsc.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{
		Logging: Logging{Level: slog.LevelDebug},
		DB:      DB{Path: "/var/lib/smsc/smsc.db"},
		API:     API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080},
		Diameter: Diameter{
			Address: netip.MustParseAddr("192.0.2.10"),
			Port:    3869,
		},
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadDefaultsDiameterPort(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(validConfig, "  port: 3869\n", "", 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Diameter.Port != 3868 {
		t.Fatalf("diameter.port = %d, want 3868", cfg.Diameter.Port)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := map[string][2]string{
		"missing db.path":          {"  path: /var/lib/smsc/smsc.db\n", ""},
		"missing diameter address": {"  address: 192.0.2.10\n", ""},
		"invalid diameter address": {"192.0.2.10", "not-an-ip"},
		"unspecified ipv4 address": {"192.0.2.10", "0.0.0.0"},
		"unspecified ipv6 address": {"192.0.2.10", "'::'"},
		"port out of range":        {"port: 3869", "port: 70000"},
		"unknown field":            {"db:\n", "unknown: true\ndb:\n"},
		"retired settings field":   {"db:\n", "service_centre:\n  address: \"15550000000\"\ndb:\n"},
		"unknown log level":        {"level: debug", "level: trace"},
		"missing api address":      {"  address: 127.0.0.1\n", ""},
		"api port out of range":    {"port: 8080", "port: 70000"},
		"invalid api address":      {"  address: 127.0.0.1\n", "  address: localhost\n"},
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(validConfig, edit[0], edit[1], 1)
			if content == validConfig {
				t.Fatalf("test edit %q did not apply", edit[0])
			}

			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadAPIDefaultPort(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(validConfig, "  port: 8080\n", "", 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.API.Port != 5010 {
		t.Fatalf("API = %+v", cfg.API)
	}
}
