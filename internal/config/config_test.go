package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `db:
  path: /var/lib/smsc/smsc.db
service_centre:
  address: "15550000000"
diameter:
  origin_host: smsc.example.org
  origin_realm: example.org
  address: 192.0.2.10
  port: 3869
hss:
  host: hss.example.org
  realm: example.org
  address: 192.0.2.20
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
		DB:            DB{Path: "/var/lib/smsc/smsc.db"},
		ServiceCentre: ServiceCentre{Address: "15550000000"},
		Diameter: Diameter{
			OriginHost:  "smsc.example.org",
			OriginRealm: "example.org",
			Address:     netip.MustParseAddr("192.0.2.10"),
			Port:        3869,
		},
		HSS: HSS{
			Host:    "hss.example.org",
			Realm:   "example.org",
			Address: netip.MustParseAddr("192.0.2.20"),
			Port:    3868,
		},
	}

	if cfg != want {
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
		"missing sc address":       {`  address: "15550000000"` + "\n", ""},
		"non-digit sc address":     {`"15550000000"`, `"+15550000000"`},
		"missing origin host":      {"  origin_host: smsc.example.org\n", ""},
		"missing origin realm":     {"  origin_realm: example.org\n", ""},
		"missing diameter address": {"  address: 192.0.2.10\n", ""},
		"invalid diameter address": {"192.0.2.10", "not-an-ip"},
		"unspecified ipv4 address": {"192.0.2.10", "0.0.0.0"},
		"unspecified ipv6 address": {"192.0.2.10", "'::'"},
		"port out of range":        {"port: 3869", "port: 70000"},
		"missing hss host":         {"  host: hss.example.org\n", ""},
		"missing hss realm":        {"  realm: example.org\n", ""},
		"missing hss address":      {"  address: 192.0.2.20\n", ""},
		"unspecified hss address":  {"192.0.2.20", "0.0.0.0"},
		"hss port out of range":    {"  address: 192.0.2.20\n", "  address: 192.0.2.20\n  port: 99999\n"},
		"unknown field":            {"db:\n", "unknown: true\ndb:\n"},
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
