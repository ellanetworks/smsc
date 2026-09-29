package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
  realm: epc.mnc001.mcc001.3gppnetwork.org
  allowed_networks: [192.0.2.0/24, "2001:db8::/32"]
numbering:
  country_code: "33"
  national_prefix: "0"
  international_prefix: "00"
delivery:
  default_validity: 48h
  retry_intervals: [30s, 10m]
  attempt_timeout: 20s
  concurrency: 5
api:
  address: 127.0.0.1
  port: 8080
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
			Realm:           "epc.mnc001.mcc001.3gppnetwork.org",
			AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8::/32")},
		},
		Numbering: Numbering{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"},
		Delivery: Delivery{
			DefaultValidity: 48 * time.Hour,
			RetryIntervals:  []time.Duration{30 * time.Second, 10 * time.Minute},
			AttemptTimeout:  20 * time.Second,
			Concurrency:     5,
		},
		API: API{Address: netip.MustParseAddr("127.0.0.1"), Port: 8080},
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
		"missing db.path":           {"  path: /var/lib/smsc/smsc.db\n", ""},
		"missing sc address":        {`  address: "15550000000"` + "\n", ""},
		"non-digit sc address":      {`"15550000000"`, `"+15550000000"`},
		"missing origin host":       {"  origin_host: smsc.example.org\n", ""},
		"missing origin realm":      {"  origin_realm: example.org\n", ""},
		"missing diameter address":  {"  address: 192.0.2.10\n", ""},
		"invalid diameter address":  {"192.0.2.10", "not-an-ip"},
		"unspecified ipv4 address":  {"192.0.2.10", "0.0.0.0"},
		"unspecified ipv6 address":  {"192.0.2.10", "'::'"},
		"port out of range":         {"port: 3869", "port: 70000"},
		"missing hss realm":         {"  realm: epc.mnc001.mcc001.3gppnetwork.org\n", ""},
		"retired hss host":          {"hss:\n", "hss:\n  host: hss.example.org\n"},
		"invalid hss network":       {"192.0.2.0/24", "192.0.2.1"},
		"missing country code":      {"  country_code: \"33\"\n", ""},
		"non-digit country code":    {"\"33\"", "\"+33\""},
		"long country code":         {"\"33\"", "\"3333\""},
		"non-digit national prefix": {"national_prefix: \"0\"", "national_prefix: \"+\""},
		"non-digit intl prefix":     {"international_prefix: \"00\"", "international_prefix: \"+\""},
		"zero retry interval":       {"[30s, 10m]", "[30s, 0s]"},
		"short attempt timeout":     {"attempt_timeout: 20s", "attempt_timeout: 100ms"},
		"negative validity":         {"default_validity: 48h", "default_validity: -1h"},
		"negative concurrency":      {"concurrency: 5", "concurrency: -1"},
		"unknown field":             {"db:\n", "unknown: true\ndb:\n"},
		"missing api address":       {"  address: 127.0.0.1\n", ""},
		"api port out of range":     {"port: 8080", "port: 70000"},
		"invalid api address":       {"  address: 127.0.0.1\n", "  address: localhost\n"},
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

func TestLoadDeliveryDefaults(t *testing.T) {
	i, j := strings.Index(validConfig, "delivery:"), strings.Index(validConfig, "api:")

	cfg, err := Load(writeConfig(t, validConfig[:i]+validConfig[j:]))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Delivery{
		DefaultValidity: 7 * 24 * time.Hour,
		RetryIntervals:  []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour},
		AttemptTimeout:  30 * time.Second,
		Concurrency:     20,
	}

	if !reflect.DeepEqual(cfg.Delivery, want) {
		t.Fatalf("Delivery = %+v, want %+v", cfg.Delivery, want)
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
