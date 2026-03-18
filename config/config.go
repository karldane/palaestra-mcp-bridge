package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type InternalConfig struct {
	Server   ServerConfig             `yaml:"server"`
	Backends map[string]BackendConfig `yaml:"backends"`
}

type ServerConfig struct {
	Port           string `yaml:"port"`
	LogLevel       string `yaml:"logLevel"`
	AuthCodeTTL    string `yaml:"authCodeTTL"`
	AccessTokenTTL string `yaml:"accessTokenTTL"`

	// Parsed durations (set after Load)
	AuthCodeTTLParsed    time.Duration `yaml:"-"`
	AccessTokenTTLParsed time.Duration `yaml:"-"`
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

func parseDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Handle "28d" format - convert to hours
	if strings.HasSuffix(s, "d") {
		days := strings.TrimSuffix(s, "d")
		if n, err := strconv.Atoi(days); err == nil {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	// Use standard Go duration parsing for "24h", "10m", etc.
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
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

	// Parse duration strings
	cfg.Server.AuthCodeTTLParsed = parseDuration(cfg.Server.AuthCodeTTL)
	if cfg.Server.AuthCodeTTLParsed == 0 {
		cfg.Server.AuthCodeTTLParsed = 10 * time.Minute
	}

	cfg.Server.AccessTokenTTLParsed = parseDuration(cfg.Server.AccessTokenTTL)
	if cfg.Server.AccessTokenTTLParsed == 0 {
		cfg.Server.AccessTokenTTLParsed = 24 * time.Hour
	}

	return &cfg, nil
}
