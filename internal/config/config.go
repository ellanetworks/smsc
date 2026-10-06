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
	defaultDiameterPort   = 3868
	defaultValidity       = 7 * 24 * time.Hour
	defaultAttemptTimeout = 30 * time.Second
	defaultConcurrency    = 20
	defaultAPIPort        = 5010
)

var defaultRetryIntervals = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

type Config struct {
	Logging       Logging       `yaml:"logging"`
	DB            DB            `yaml:"db"`
	ServiceCentre ServiceCentre `yaml:"service_centre"`
	Diameter      Diameter      `yaml:"diameter"`
	HSS           HSS           `yaml:"hss"`
	Numbering     Numbering     `yaml:"numbering"`
	Delivery      Delivery      `yaml:"delivery"`
	API           API           `yaml:"api"`
}

type Logging struct {
	Level slog.Level `yaml:"level"`
}

type API struct {
	Address netip.Addr `yaml:"address"`
	Port    int        `yaml:"port"`
}

type Numbering struct {
	CountryCode         string `yaml:"country_code"`
	NationalPrefix      string `yaml:"national_prefix"`
	InternationalPrefix string `yaml:"international_prefix"`
}

type Delivery struct {
	DefaultValidity time.Duration   `yaml:"default_validity"`
	RetryIntervals  []time.Duration `yaml:"retry_intervals"`
	AttemptTimeout  time.Duration   `yaml:"attempt_timeout"`
	Concurrency     int             `yaml:"concurrency"`
}

type DB struct {
	Path string `yaml:"path"`
}

type ServiceCentre struct {
	Address string `yaml:"address"`
}

type HSS struct {
	Realm           string         `yaml:"realm"`
	AllowedNetworks []netip.Prefix `yaml:"allowed_networks"`
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

	if cfg.API.Port == 0 {
		cfg.API.Port = defaultAPIPort
	}

	if cfg.Delivery.DefaultValidity == 0 {
		cfg.Delivery.DefaultValidity = defaultValidity
	}

	if cfg.Delivery.RetryIntervals == nil {
		cfg.Delivery.RetryIntervals = defaultRetryIntervals
	}

	if cfg.Delivery.AttemptTimeout == 0 {
		cfg.Delivery.AttemptTimeout = defaultAttemptTimeout
	}

	if cfg.Delivery.Concurrency == 0 {
		cfg.Delivery.Concurrency = defaultConcurrency
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
	case c.HSS.Realm == "":
		return errors.New("hss.realm is required")
	case !isDigits(c.Numbering.CountryCode) || len(c.Numbering.CountryCode) > 3:
		return errors.New("numbering.country_code must be 1 to 3 digits")
	case c.Numbering.NationalPrefix != "" && !isDigits(c.Numbering.NationalPrefix):
		return errors.New("numbering.national_prefix must be digits")
	case c.Numbering.InternationalPrefix != "" && !isDigits(c.Numbering.InternationalPrefix):
		return errors.New("numbering.international_prefix must be digits")
	case c.Delivery.DefaultValidity < 0:
		return errors.New("delivery.default_validity must not be negative")
	case c.Delivery.AttemptTimeout < time.Second:
		return errors.New("delivery.attempt_timeout must be at least 1s")
	case c.Delivery.Concurrency < 1:
		return errors.New("delivery.concurrency must be at least 1")
	case !c.API.Address.IsValid():
		return errors.New("api.address is required")
	case c.API.Port < 1 || c.API.Port > 65535:
		return fmt.Errorf("api.port %d is out of range", c.API.Port)
	}

	for _, network := range c.HSS.AllowedNetworks {
		if !network.IsValid() {
			return errors.New("hss.allowed_networks must be IP prefixes such as 192.0.2.0/24")
		}
	}

	for _, interval := range c.Delivery.RetryIntervals {
		if interval <= 0 {
			return errors.New("delivery.retry_intervals must be positive")
		}
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
