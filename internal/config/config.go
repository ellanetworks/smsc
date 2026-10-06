package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	defaultDiameterPort = 3868
	defaultAPIPort      = 5010
)

type Config struct {
	Logging  Logging  `yaml:"logging"`
	DB       DB       `yaml:"db"`
	API      API      `yaml:"api"`
	Diameter Diameter `yaml:"diameter"`
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
}

type Diameter struct {
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
	case !c.Diameter.Address.IsValid():
		return errors.New("diameter.address is required")
	case c.Diameter.Address.IsUnspecified():
		return errors.New("diameter.address must be a specific address, not 0.0.0.0 or ::, since it is advertised to peers")
	case c.Diameter.Port < 1 || c.Diameter.Port > 65535:
		return fmt.Errorf("diameter.port %d is out of range", c.Diameter.Port)
	}

	return nil
}
