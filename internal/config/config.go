package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	defaultDiameterPort = 3868
	defaultAPIPort      = 5010
	defaultSMPPPort     = 2775
	defaultClusterPort  = 7000
)

type Config struct {
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
	Address   netip.Addr `yaml:"address"`
	Port      int        `yaml:"port"`
	Advertise string     `yaml:"advertise"`
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
	Name    string     `yaml:"name"`
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
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

	if c.Advertise == "" {
		return nil
	}

	host, port, err := net.SplitHostPort(c.Advertise)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("cluster.advertise %q is not a host:port", c.Advertise)
	}

	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		return fmt.Errorf("cluster.advertise %q must be a specific address", c.Advertise)
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
