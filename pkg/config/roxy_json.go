package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	roxyConfigFile = "roxy.json"
	maxPortNumber  = 65535
)

// RoxyConfig represents a roxy.json file with multiple service definitions.
type RoxyConfig struct {
	Schema   string                   `json:"$schema,omitempty"`
	Services map[string]ServiceConfig `json:"services"`
}

// ServiceConfig defines a single service in roxy.json.
type ServiceConfig struct {
	Cmd        string `json:"cmd"`
	Name       string `json:"name,omitempty"`
	Port       int    `json:"port,omitempty"`
	TLS        bool   `json:"tls,omitempty"`
	ListenPort int    `json:"listen-port,omitempty"`
	Public     bool   `json:"public,omitempty"`
}

// LoadRoxyJSON reads roxy.json from the given directory.
// Returns nil, nil if the file doesn't exist.
func LoadRoxyJSON(dir string) (*RoxyConfig, error) {
	path := filepath.Join(dir, roxyConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg RoxyConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse roxy.json: %w", err)
	}

	if err := validateRoxyConfig(cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validateRoxyConfig(cfg RoxyConfig) error {
	for name, svc := range cfg.Services {
		if strings.TrimSpace(svc.Cmd) == "" {
			return fmt.Errorf("service %q: cmd is required", name)
		}

		if svc.Port != 0 {
			if err := validatePort(name, "port", svc.Port); err != nil {
				return err
			}
		}

		if svc.ListenPort != 0 {
			if err := validatePort(name, "listen-port", svc.ListenPort); err != nil {
				return err
			}
		}
	}

	return nil
}

func validatePort(serviceName, field string, value int) error {
	if value < 1 || value > maxPortNumber {
		return fmt.Errorf("service %q: %s must be between 1 and %d", serviceName, field, maxPortNumber)
	}
	return nil
}
