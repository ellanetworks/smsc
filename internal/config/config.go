package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"

	"github.com/ellanetworks/smsc/internal/cluster/pki"
	"github.com/ellanetworks/smsc/internal/settings"
	"gopkg.in/yaml.v3"
)

const (
	defaultDiameterPort = 3868
	defaultAPIPort      = 5010
	defaultSMPPPort     = 2775
	defaultClusterPort  = 7000
)

const (
	EnvDiameterHostLabel = "SMSC_DIAMETER_HOST_LABEL"
	EnvClusterAdvertise  = "SMSC_CLUSTER_ADVERTISE"
)

type Config struct {
	warnings []string

	Logging   Logging   `yaml:"logging"`
	DB        DB        `yaml:"db"`
	API       API       `yaml:"api"`
	Diameter  Diameter  `yaml:"diameter"`
	SMPP      *SMPP     `yaml:"smpp"`
	Cluster   *Cluster  `yaml:"cluster"`
	Telemetry Telemetry `yaml:"telemetry"`
}

type Logging struct {
	Level slog.Level `yaml:"level"`
}

type DB struct {
	Path string `yaml:"path"`
}

type API struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
	TLS     *TLS       `yaml:"tls"`
}

type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type SMPP struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
	TLS     *TLS       `yaml:"tls"`
}

type Cluster struct {
	Address         netip.Addr `yaml:"address"`
	Port            int        `yaml:"port"`
	Advertise       string     `yaml:"advertise"`
	Join            []string   `yaml:"join"`
	BootstrapExpect int        `yaml:"bootstrap_expect"`
	SecretFile      string     `yaml:"secret_file"`
}

func (c Cluster) JoinAddresses() []string {
	out := make([]string, 0, len(c.Join))

	for _, j := range c.Join {
		if _, _, err := net.SplitHostPort(j); err != nil {
			j = net.JoinHostPort(j, strconv.Itoa(c.Port))
		}

		out = append(out, j)
	}

	return out
}

func (c Cluster) AdvertiseAddress() string {
	if c.Advertise != "" {
		return c.Advertise
	}

	return netip.AddrPortFrom(c.Address, uint16(c.Port)).String()
}

type Telemetry struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

type Diameter struct {
	Name      string     `yaml:"name"`
	Address   netip.Addr `yaml:"address"`
	Port      int        `yaml:"port"`
	HostLabel string     `yaml:"host_label"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyEnv()

	if cfg.Diameter.Port == 0 {
		cfg.Diameter.Port = defaultDiameterPort
	}

	if cfg.API.Port == 0 {
		cfg.API.Port = defaultAPIPort
	}

	if cfg.SMPP != nil && cfg.SMPP.Port == 0 {
		cfg.SMPP.Port = defaultSMPPPort
	}

	if c := cfg.Cluster; c != nil {
		if c.Port == 0 {
			c.Port = defaultClusterPort
		}
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv(EnvDiameterHostLabel); v != "" {
		c.Diameter.HostLabel = v
	}

	if v := os.Getenv(EnvClusterAdvertise); v != "" {
		if c.Cluster == nil {
			c.warnings = append(c.warnings, EnvClusterAdvertise+" is set, but the configuration has no cluster section: ignoring it")
			return
		}

		c.Cluster.Advertise = v
	}
}

func (c Config) Warnings() []string {
	out := append([]string(nil), c.warnings...)

	if cl := c.Cluster; cl != nil {
		switch {
		case cl.BootstrapExpect == 2:
			out = append(out, "cluster.bootstrap_expect is 2: a cluster of 2 voters tolerates no failure")
		case cl.BootstrapExpect > 2 && cl.BootstrapExpect%2 == 0:
			out = append(out, fmt.Sprintf("cluster.bootstrap_expect is %d, an even number: it tolerates no more failures than %d",
				cl.BootstrapExpect, cl.BootstrapExpect-1))
		}

		if cl.BootstrapExpect > 5 {
			out = append(out, fmt.Sprintf("cluster.bootstrap_expect is %d: the cluster keeps at most 5 voters, the other "+
				"nodes stay standbys", cl.BootstrapExpect))
		}
	}

	return out
}

func hostLabelSource() string {
	if os.Getenv(EnvDiameterHostLabel) != "" {
		return EnvDiameterHostLabel
	}

	return "diameter.host_label"
}

func advertiseSource() string {
	if os.Getenv(EnvClusterAdvertise) != "" {
		return EnvClusterAdvertise
	}

	return "cluster.advertise"
}

func (c Config) validate() error {
	switch {
	case c.DB.Path == "":
		return errors.New("db.path is required")
	case !c.API.Address.IsValid():
		return errors.New("api.address is required")
	case c.API.Port < 1 || c.API.Port > 65535:
		return fmt.Errorf("api.port %d is out of range", c.API.Port)
	case c.Diameter.Name != "" && c.Diameter.Address.IsValid():
		return errors.New("diameter.name and diameter.address cannot both be set")
	case c.Diameter.Name == "" && !c.Diameter.Address.IsValid():
		return errors.New("diameter.name or diameter.address is required")
	case c.Diameter.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	case c.Diameter.Port < 1 || c.Diameter.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", c.Diameter.Port)
	case c.Telemetry.Enabled && c.Telemetry.OTLPEndpoint == "":
		return errors.New("telemetry.otlp_endpoint is required when telemetry.enabled is true")
	}

	if c.Diameter.HostLabel != "" {
		if err := settings.ValidLabel(c.Diameter.HostLabel); err != nil {
			return fmt.Errorf("%s %q %w", hostLabelSource(), c.Diameter.HostLabel, err)
		}
	}

	if err := c.API.TLS.validate("api"); err != nil {
		return err
	}

	if s := c.SMPP; s != nil {
		switch {
		case !s.Address.IsValid():
			return errors.New("smpp.address is required")
		case s.Port < 1 || s.Port > 65535:
			return fmt.Errorf("smpp.port %d is out of range", s.Port)
		case s.Port == c.API.Port && (s.Address == c.API.Address || s.Address.IsUnspecified() || c.API.Address.IsUnspecified()):
			return fmt.Errorf("smpp.port %d is the API's", s.Port)
		}

		if err := s.TLS.validate("smpp"); err != nil {
			return err
		}
	}

	if c := c.Cluster; c != nil {
		if err := c.validate(); err != nil {
			return err
		}
	}

	return nil
}

func (c Cluster) validate() error {
	switch {
	case !c.Address.IsValid():
		return errors.New("cluster.address is required")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("cluster.port %d is out of range", c.Port)
	case c.Advertise == "" && c.Address.IsUnspecified():
		return errors.New("cluster.advertise is required when cluster.address is unspecified")
	}

	if err := c.validateJoin(); err != nil {
		return err
	}

	if len(c.Join) > 0 && c.SecretFile == "" {
		return errors.New("cluster.secret_file is required with cluster.join")
	}

	if c.SecretFile != "" {
		if _, err := pki.ReadSecrets(c.SecretFile); err != nil {
			return fmt.Errorf("cluster.secret_file: %w", err)
		}
	}

	if c.Advertise == "" {
		return nil
	}

	host, port, err := net.SplitHostPort(c.Advertise)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%s %q is not a host:port", advertiseSource(), c.Advertise)
	}

	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		return fmt.Errorf("%s %q must be a specific address", advertiseSource(), c.Advertise)
	}

	return nil
}

func (c Cluster) validateJoin() error {
	switch {
	case c.BootstrapExpect < 0 || c.BootstrapExpect == 1:
		return fmt.Errorf("cluster.bootstrap_expect %d must be at least 2, or unset", c.BootstrapExpect)
	case c.BootstrapExpect > 0 && len(c.Join) == 0:
		return errors.New("cluster.bootstrap_expect requires cluster.join")
	}

	for _, j := range c.JoinAddresses() {
		host, port, err := net.SplitHostPort(j)
		if err != nil || host == "" {
			return fmt.Errorf("cluster.join %q is not a host or host:port", j)
		}

		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("cluster.join %q has an invalid port", j)
		}
	}

	return nil
}

func (t *TLS) validate(prefix string) error {
	if t == nil {
		return nil
	}

	if t.Cert == "" || t.Key == "" {
		return fmt.Errorf("%s.tls.cert and %s.tls.key must both be set", prefix, prefix)
	}

	for name, path := range map[string]string{prefix + ".tls.cert": t.Cert, prefix + ".tls.key": t.Key} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	return nil
}
