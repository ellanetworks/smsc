package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestLoadDiameterName(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(validConfig, "  address: 192.0.2.10\n", "  name: eth0\n", 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if want := (Diameter{Name: "eth0", Port: 3869}); cfg.Diameter != want {
		t.Fatalf("diameter = %+v, want %+v", cfg.Diameter, want)
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
		"missing db.path":           {"  path: /var/lib/smsc/smsc.db\n", ""},
		"missing diameter address":  {"  address: 192.0.2.10\n", ""},
		"diameter name and address": {"  address: 192.0.2.10\n", "  name: eth0\n  address: 192.0.2.10\n"},
		"invalid diameter address":  {"192.0.2.10", "not-an-ip"},
		"unspecified ipv4 address":  {"192.0.2.10", "0.0.0.0"},
		"unspecified ipv6 address":  {"192.0.2.10", "'::'"},
		"port out of range":         {"port: 3869", "port: 70000"},
		"unknown field":             {"db:\n", "unknown: true\ndb:\n"},
		"retired settings field":    {"db:\n", "service_centre:\n  address: \"15550000000\"\ndb:\n"},
		"unknown log level":         {"level: debug", "level: trace"},
		"missing api address":       {"  address: 127.0.0.1\n", ""},
		"api port out of range":     {"port: 8080", "port: 70000"},
		"invalid api address":       {"  address: 127.0.0.1\n", "  address: localhost\n"},
		"negative drain delay":      {"  port: 8080\n", "  port: 8080\n  drain_delay: -1s\n"},
		"drain delay without unit":  {"  port: 8080\n", "  port: 8080\n  drain_delay: 5\n"},
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

func TestLoadAPITLS(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")

	for _, path := range []string{cert, key} {
		if err := os.WriteFile(path, []byte("pem"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	withTLS := func(tls string) string {
		return strings.Replace(validConfig, "  port: 8080\n", "  port: 8080\n  tls:\n"+tls, 1)
	}

	cfg, err := Load(writeConfig(t, withTLS("    cert: "+cert+"\n    key: "+key+"\n")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if want := (TLS{Cert: cert, Key: key}); cfg.API.TLS == nil || *cfg.API.TLS != want {
		t.Fatalf("API.TLS = %+v, want %+v", cfg.API.TLS, want)
	}

	if cfg, err := Load(writeConfig(t, validConfig)); err != nil || cfg.API.TLS != nil {
		t.Fatalf("Load without tls = %+v, %v", cfg.API.TLS, err)
	}

	for name, tls := range map[string]string{
		"cert only":     "    cert: " + cert + "\n",
		"key only":      "    key: " + key + "\n",
		"empty":         "    cert: \"\"\n",
		"missing cert":  "    cert: " + filepath.Join(dir, "absent.pem") + "\n    key: " + key + "\n",
		"missing key":   "    cert: " + cert + "\n    key: " + filepath.Join(dir, "absent.pem") + "\n",
		"unknown field": "    cert: " + cert + "\n    key: " + key + "\n    ca: " + cert + "\n",
	} {
		if _, err := Load(writeConfig(t, withTLS(tls))); err == nil {
			t.Errorf("%s: no error", name)
		}
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

func TestLoadAPIDrainDelay(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(validConfig, "  port: 8080\n", "  port: 8080\n  drain_delay: 5s\n", 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.API.DrainDelay != 5*time.Second {
		t.Fatalf("API.DrainDelay = %s", cfg.API.DrainDelay)
	}
}

func TestLoadSMPP(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "smpp.pem"), filepath.Join(dir, "smpp.key")

	for _, p := range []string{cert, key} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil || cfg.SMPP != nil {
		t.Fatalf("Load without smpp = %+v, %v; want no listener", cfg.SMPP, err)
	}

	cfg, err = Load(writeConfig(t, validConfig+"smpp:\n  address: 0.0.0.0\n"))
	if err != nil {
		t.Fatal(err)
	}

	if want := (SMPP{Address: netip.MustParseAddr("0.0.0.0"), Port: 2775}); *cfg.SMPP != want {
		t.Fatalf("smpp = %+v, want %+v", *cfg.SMPP, want)
	}

	cfg, err = Load(writeConfig(t, validConfig+"smpp:\n  address: \"::\"\n  port: 3550\n  tls:\n    cert: "+cert+
		"\n    key: "+key+"\n"))
	if err != nil {
		t.Fatal(err)
	}

	if want := (TLS{Cert: cert, Key: key}); cfg.SMPP.Port != 3550 || *cfg.SMPP.TLS != want {
		t.Fatalf("smpp = %+v, tls %+v", *cfg.SMPP, cfg.SMPP.TLS)
	}

	for name, block := range map[string]string{
		"no address":   "smpp:\n  port: 2775\n",
		"port":         "smpp:\n  address: 0.0.0.0\n  port: 70000\n",
		"API port":     "smpp:\n  address: 0.0.0.0\n  port: 8080\n",
		"cert only":    "smpp:\n  address: 0.0.0.0\n  tls:\n    cert: " + cert + "\n",
		"missing file": "smpp:\n  address: 0.0.0.0\n  tls:\n    cert: " + cert + "\n    key: " + filepath.Join(dir, "none") + "\n",
		"unknown key":  "smpp:\n  address: 0.0.0.0\n  window: 10\n",
	} {
		if _, err := Load(writeConfig(t, validConfig+block)); err == nil {
			t.Errorf("%s: Load succeeded", name)
		}
	}
}
