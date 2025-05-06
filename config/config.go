package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LogLevel string `yaml:"log_level"`

	original string
}

func LoadFile(filename string) (*Config, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	// Set default config
	// *cfg = DefaultConfig
	if err := yaml.Unmarshal(content, cfg); err != nil {
		return nil, err
	}

	cfg.original = filename

	return cfg, nil
}
