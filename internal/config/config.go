package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultDiameterPort = 3868
	defaultAPIPort      = 5010
	// defaultSMPPPort is the port IANA registers as "smpp"; SMPP 3.4 names none.
	defaultSMPPPort = 2775
)

type Config struct {
	Logging  Logging  `yaml:"logging"`
	DB       DB       `yaml:"db"`
	API      API      `yaml:"api"`
	Diameter Diameter `yaml:"diameter"`
	// SMPP, when set, runs the SMPP 3.4 listener for ESMEs.
	SMPP *SMPP `yaml:"smpp"`
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
	// TLS, when set, serves the API and UI over HTTPS.
	TLS *TLS `yaml:"tls"`
	// DrainDelay is how long the SMSC keeps serving, with GET /api/v1/readyz failing, once told to stop, so that
	// a load balancer stops sending it requests before it closes its listener.
	DrainDelay time.Duration `yaml:"drain_delay"`
}

// TLS is a PEM certificate chain, and its private key.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// SMPP is the listener of the SMPP 3.4 interface to ESMEs.
type SMPP struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
	// TLS, when set, wraps each connection in TLS from its first octet. SMPP 3.4 runs over TCP/IP or X.25 (section
	// 2.4): TLS is a convention outside it.
	TLS *TLS `yaml:"tls"`
}

// Diameter listens on Address, or on the addresses of the interface Name.
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
	case c.API.DrainDelay < 0:
		return fmt.Errorf("api.drain_delay %s is negative", c.API.DrainDelay)
	case c.Diameter.Name != "" && c.Diameter.Address.IsValid():
		return errors.New("diameter.name and diameter.address cannot both be set")
	case c.Diameter.Name == "" && !c.Diameter.Address.IsValid():
		return errors.New("diameter.name or diameter.address is required")
	case c.Diameter.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	case c.Diameter.Port < 1 || c.Diameter.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", c.Diameter.Port)
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

	return nil
}

// validate checks the TLS of the block named prefix, if any: both files set and present.
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
