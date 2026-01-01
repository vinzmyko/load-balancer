// Package config handles loading and validation of the load balancer configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config represents the load balancer configuration
type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Backends []BackendConfig `yaml:"backends"`
}

// Validate the configuration file
func (cfg *Config) Validate() error {
	isValidServerPort := cfg.Server.Port >= 1 && cfg.Server.Port <= 65535
	hasAtLeastOneBackendServer := len(cfg.Backends) > 0

	if !isValidServerPort {
		return fmt.Errorf("invalid port %d: must be 1-65535", cfg.Server.Port)
	}
	if !hasAtLeastOneBackendServer {
		return fmt.Errorf("needs to have at least one backend server")
	}

	for i, backendServer := range cfg.Backends {
		if backendServer.URL == "" {
			return fmt.Errorf("backend server #%d is empty", i)
		}
		if backendServer.Weight <= 0 {
			return fmt.Errorf("backend server #%d has a negative weight", i)
		}

	}

	return nil
}

// ServerConfig holds the server specific settings
type ServerConfig struct {
	Port int `yaml:"port"`
}

// BackendConfig represents a single backend server configuration
type BackendConfig struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	err = yaml.Unmarshal(bytes, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file: %w", err)
	}

	err = cfg.Validate()
	if err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}
