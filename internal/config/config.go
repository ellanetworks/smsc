package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"gopkg.in/yaml.v3"
)

const defaultDiameterPort = 3868

type Config struct {
	DB            DB            `yaml:"db"`
	ServiceCentre ServiceCentre `yaml:"service_centre"`
	Diameter      Diameter      `yaml:"diameter"`
}

type DB struct {
	Path string `yaml:"path"`
}

type ServiceCentre struct {
	Address string `yaml:"address"`
}

type Diameter struct {
	OriginHost  string     `yaml:"origin_host"`
	OriginRealm string     `yaml:"origin_realm"`
	Address     netip.Addr `yaml:"address"`
	Port        int        `yaml:"port"`
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

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	switch {
	case c.DB.Path == "":
		return errors.New("db.path is required")
	case !isDigits(c.ServiceCentre.Address):
		return errors.New("service_centre.address must be a non-empty string of digits")
	case c.Diameter.OriginHost == "":
		return errors.New("diameter.origin_host is required")
	case c.Diameter.OriginRealm == "":
		return errors.New("diameter.origin_realm is required")
	case !c.Diameter.Address.IsValid():
		return errors.New("diameter.address is required")
	case c.Diameter.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	case c.Diameter.Port < 1 || c.Diameter.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", c.Diameter.Port)
	}

	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
