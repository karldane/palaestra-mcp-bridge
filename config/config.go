package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"

	"gopkg.in/yaml.v3"
)

type InternalConfig struct {
	Server     ServerConfig             `yaml:"server"`
	Backends   map[string]BackendConfig `yaml:"backends"`
	Encryption EncryptionConfig         `yaml:"encryption"`
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
	Command       string            `yaml:"command"`
	PoolSize      int               `yaml:"poolSize"`
	Env           map[string]string `yaml:"env"`
	Secrets       []SecretRef       `yaml:"secrets"`
	ToolPrefix    string            `yaml:"toolPrefix"`
	SelfReporting bool              `yaml:"selfReporting"`
}

type SecretRef struct {
	Name    string `yaml:"name"`
	EnvKey  string `yaml:"envKey"`
	Context string `yaml:"context"`
}

type EncryptionConfig struct {
	Provider          string `yaml:"provider"`
	KeyEnv            string `yaml:"keyEnv"`
	KeyFileEnv        string `yaml:"keyFileEnv"`
	K8sSecretPath     string `yaml:"k8sSecretPath"`
	K8sKeyName        string `yaml:"k8sKeyName"`
	RequireEncryption bool   `yaml:"requireEncryption"`
}

func (c *EncryptionConfig) Validate() error {
	switch c.Provider {
	case "envvar":
		if c.KeyEnv == "" && c.KeyFileEnv == "" {
			return fmt.Errorf("encryption provider 'envvar' requires keyEnv or keyFileEnv")
		}
	case "k8s":
		if c.K8sSecretPath == "" {
			return fmt.Errorf("encryption provider 'k8s' requires k8sSecretPath")
		}
	case "":
		if c.RequireEncryption {
			return fmt.Errorf("encryption required but no provider configured")
		}
	default:
		return fmt.Errorf("unknown encryption provider: %s", c.Provider)
	}
	return nil
}

func (c *EncryptionConfig) NewKEKProvider() (crypto.KEKProvider, error) {
	switch c.Provider {
	case "envvar":
		return crypto.NewEnvVarProvider(c.KeyEnv, c.KeyFileEnv), nil
	case "k8s":
		return crypto.NewK8sSecretProvider(c.K8sSecretPath, c.K8sKeyName)
	default:
		return nil, fmt.Errorf("unknown provider: %s", c.Provider)
	}
}

func (c *InternalConfig) ValidateEncryption() error {
	return c.Encryption.Validate()
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

	if err := cfg.ValidateEncryption(); err != nil {
		return nil, fmt.Errorf("encryption config validation failed: %w", err)
	}

	return &cfg, nil
}
