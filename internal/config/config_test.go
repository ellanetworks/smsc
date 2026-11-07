package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func writeSecret(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

const testSecret = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=\n"

func cluster(t *testing.T, lines string) string {
	t.Helper()

	return validConfig + "cluster:\n" + lines + "  secret_file: " + writeSecret(t, testSecret) + "\n"
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

func TestLoadTelemetry(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig+"telemetry:\n  enabled: true\n  otlp_endpoint: collector:4317\n"))
	if err != nil {
		t.Fatal(err)
	}

	if want := (Telemetry{Enabled: true, OTLPEndpoint: "collector:4317"}); cfg.Telemetry != want {
		t.Fatalf("telemetry = %+v, want %+v", cfg.Telemetry, want)
	}

	if _, err := Load(writeConfig(t, validConfig+"telemetry:\n  otlp_endpoint: collector:4317\n")); err != nil {
		t.Fatalf("Load with telemetry disabled: %v", err)
	}

	if _, err := Load(writeConfig(t, validConfig+"telemetry:\n  enabled: true\n")); err == nil {
		t.Fatal("Load without telemetry.otlp_endpoint succeeded")
	}
}

func TestLoadCluster(t *testing.T) {
	cfg, err := Load(writeConfig(t, cluster(t, "  address: 10.0.0.1\n")))
	if err != nil {
		t.Fatal(err)
	}

	c := cfg.Cluster
	if c == nil || c.Port != defaultClusterPort || c.AdvertiseAddress() != "10.0.0.1:7000" {
		t.Fatalf("cluster = %+v", c)
	}

	cfg, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n  advertise: smsc-0.smsc:7000\n")))
	if err != nil || cfg.Cluster.AdvertiseAddress() != "smsc-0.smsc:7000" {
		t.Fatalf("cluster = %+v, %v", cfg.Cluster, err)
	}

	for name, block := range map[string]string{
		"no address":             "  port: 7000\n",
		"unspecified, no advert": "  address: 0.0.0.0\n",
		"advertise without port": "  address: 10.0.0.1\n  advertise: smsc-0\n",
		"unspecified advertise":  "  address: 10.0.0.1\n  advertise: 0.0.0.0:7000\n",
		"expect of 1":            "  address: 10.0.0.1\n  join: [smsc:7000]\n  bootstrap_expect: 1\n",
		"negative expect":        "  address: 10.0.0.1\n  join: [smsc:7000]\n  bootstrap_expect: -3\n",
		"expect without join":    "  address: 10.0.0.1\n  bootstrap_expect: 3\n",
		"join with a bad port":   "  address: 10.0.0.1\n  join: [smsc:70000]\n",
	} {
		if _, err := Load(writeConfig(t, cluster(t, block))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadClusterJoin(t *testing.T) {
	cfg, err := Load(writeConfig(t, cluster(t,
		"  address: 0.0.0.0\n  advertise: smsc-0.smsc:7000\n  port: 7001\n  join: [smsc.ella.svc:7000, 10.0.0.2]\n  bootstrap_expect: 3\n")))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := cfg.Cluster.JoinAddresses(), []string{"smsc.ella.svc:7000", "10.0.0.2:7001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("join = %v, want %v", got, want)
	}

	if cfg.Cluster.BootstrapExpect != 3 || len(cfg.Warnings()) != 0 {
		t.Fatalf("bootstrap_expect = %d, warnings %v", cfg.Cluster.BootstrapExpect, cfg.Warnings())
	}

	for expect, warns := range map[int]int{2: 1, 4: 1, 5: 0, 6: 2, 7: 1} {
		cfg, err := Load(writeConfig(t, cluster(t, "  address: 10.0.0.1\n  join: [smsc:7000]\n  bootstrap_expect: "+
			strconv.Itoa(expect)+"\n")))
		if err != nil {
			t.Fatal(err)
		}

		if got := len(cfg.Warnings()); got != warns {
			t.Errorf("bootstrap_expect %d: warnings %v, want %d", expect, cfg.Warnings(), warns)
		}
	}
}

func TestLoadClusterSecret(t *testing.T) {
	if _, err := Load(writeConfig(t, validConfig+"cluster:\n  address: 10.0.0.1\n")); err != nil {
		t.Fatalf("Load without join and secret_file = %v", err)
	}

	if _, err := Load(writeConfig(t, validConfig+"cluster:\n  address: 10.0.0.1\n  join: [smsc:7000]\n")); err == nil ||
		!strings.Contains(err.Error(), "secret_file") {
		t.Fatalf("Load with join and without secret_file = %v", err)
	}

	for name, content := range map[string]string{
		"empty":      "\n\n",
		"short":      "c2hvcnQ=\n",
		"not base64": "not base64 at all!\n",
	} {
		block := validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + writeSecret(t, content) + "\n"
		if _, err := Load(writeConfig(t, block)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	block := validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + filepath.Join(t.TempDir(), "missing") + "\n"
	if _, err := Load(writeConfig(t, block)); err == nil {
		t.Error("missing secret file: expected an error")
	}

	rotated := testSecret + "\n" + "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=\n"
	block = validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + writeSecret(t, rotated) + "\n"

	if _, err := Load(writeConfig(t, block)); err != nil {
		t.Fatalf("two secrets: %v", err)
	}
}

func TestLoadDiameterHostLabel(t *testing.T) {
	tests := []struct {
		label string
		valid bool
	}{
		{label: "smsc-a", valid: true},
		{label: "a", valid: true},
		{label: "0smsc", valid: true},
		{label: "smsc-01a1ac75e354", valid: true},
		{label: strings.Repeat("a", 63), valid: true},
		{label: strings.Repeat("a", 64)},
		{label: "SMSC-A"},
		{label: "smsc.a"},
		{label: "-smsc"},
		{label: "smsc-"},
		{label: "smsc_a"},
		{label: "smsc a"},
		{label: "smsé"},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, validConfig+"  host_label: \""+tt.label+"\"\n"))

			switch {
			case tt.valid && err != nil:
				t.Fatalf("Load: %v", err)
			case tt.valid && cfg.Diameter.HostLabel != tt.label:
				t.Fatalf("HostLabel = %q, want %q", cfg.Diameter.HostLabel, tt.label)
			case !tt.valid && err == nil:
				t.Fatalf("Load accepted the host label %q", tt.label)
			case !tt.valid && !strings.Contains(err.Error(), "diameter.host_label"):
				t.Fatalf("Load = %v, want a diameter.host_label error", err)
			}
		})
	}
}

func TestLoadHostLabelFromEnvironment(t *testing.T) {
	t.Setenv(EnvDiameterHostLabel, "ella-smsc-1")

	cfg, err := Load(writeConfig(t, validConfig+"  host_label: smsc-a\n"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Diameter.HostLabel != "ella-smsc-1" {
		t.Fatalf("HostLabel = %q, want the environment's", cfg.Diameter.HostLabel)
	}

	cfg, err = Load(writeConfig(t, validConfig))
	if err != nil || cfg.Diameter.HostLabel != "ella-smsc-1" {
		t.Fatalf("HostLabel = %q, %v", cfg.Diameter.HostLabel, err)
	}

	t.Setenv(EnvDiameterHostLabel, "Smsc_1")

	_, err = Load(writeConfig(t, validConfig+"  host_label: smsc-a\n"))
	if err == nil || !strings.Contains(err.Error(), EnvDiameterHostLabel) {
		t.Fatalf("Load = %v, want an error naming %s", err, EnvDiameterHostLabel)
	}

	t.Setenv(EnvDiameterHostLabel, "")

	cfg, err = Load(writeConfig(t, validConfig+"  host_label: smsc-a\n"))
	if err != nil || cfg.Diameter.HostLabel != "smsc-a" {
		t.Fatalf("HostLabel = %q, %v, want the file's when the variable is empty", cfg.Diameter.HostLabel, err)
	}
}

func TestLoadAdvertiseFromEnvironment(t *testing.T) {
	t.Setenv(EnvClusterAdvertise, "ella-smsc-2.ella-smsc-internal:7000")

	cfg, err := Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n  advertise: smsc-0.smsc:7000\n")))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Cluster.AdvertiseAddress(); got != "ella-smsc-2.ella-smsc-internal:7000" {
		t.Fatalf("advertise = %s, want the environment's", got)
	}

	cfg, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n")))
	if err != nil || cfg.Cluster.AdvertiseAddress() != "ella-smsc-2.ella-smsc-internal:7000" {
		t.Fatalf("cluster = %+v, %v", cfg.Cluster, err)
	}

	cfg, err = Load(writeConfig(t, validConfig))
	if err != nil || cfg.Cluster != nil {
		t.Fatalf("Load without a cluster section = %+v, %v, want it to ignore %s", cfg.Cluster, err, EnvClusterAdvertise)
	}

	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], EnvClusterAdvertise) {
		t.Fatalf("warnings = %v, want one naming %s", w, EnvClusterAdvertise)
	}

	t.Setenv(EnvClusterAdvertise, "ella-smsc-2")

	_, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n")))
	if err == nil || !strings.Contains(err.Error(), EnvClusterAdvertise) {
		t.Fatalf("Load = %v, want an error naming %s", err, EnvClusterAdvertise)
	}
}
