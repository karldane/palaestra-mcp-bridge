package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type InternalConfig struct {
	Server   ServerConfig             `yaml:"server"`
	Backends map[string]BackendConfig `yaml:"backends"`
}

type ServerConfig struct {
	Port     string `yaml:"port"`
	LogLevel string `yaml:"logLevel"`
}

type BackendConfig struct {
	Command    string            `yaml:"command"`
	PoolSize   int               `yaml:"poolSize"`
	Env        map[string]string `yaml:"env"`
	Secrets    []SecretRef       `yaml:"secrets"`
	ToolPrefix string            `yaml:"toolPrefix"`
}

type SecretRef struct {
	Name    string `yaml:"name"`
	EnvKey  string `yaml:"envKey"`
	Context string `yaml:"context"`
}

func Load(path string) (*InternalConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg InternalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.LogLevel == "" {
		cfg.Server.LogLevel = "info"
	}

	return &cfg, nil
}
