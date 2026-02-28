package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RoxyConfig represents a roxy.yaml file with multiple service definitions.
type RoxyConfig struct {
	Services map[string]ServiceConfig `yaml:"services"`
}

// ServiceConfig defines a single service in roxy.yaml.
type ServiceConfig struct {
	Cmd        string `yaml:"cmd"`
	Name       string `yaml:"name"`
	Port       int    `yaml:"port"`
	TLS        bool   `yaml:"tls"`
	ListenPort int    `yaml:"listen-port"`
}

// LoadRoxyYAML reads roxy.yaml from the given directory.
// Returns nil, nil if the file doesn't exist.
func LoadRoxyYAML(dir string) (*RoxyConfig, error) {
	path := filepath.Join(dir, "roxy.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cfg RoxyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
