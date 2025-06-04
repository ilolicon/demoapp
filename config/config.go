package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DateFormat string `yaml:"date_format"`

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

func (c Config) String() string {
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Sprintf("<error creating config string: %s>", err)
	}
	return string(b)
}
