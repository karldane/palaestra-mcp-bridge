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
	Auth       AuthConfig               `yaml:"auth"`
	SMTP       SMTPConfig               `yaml:"smtp"`
}

type ServerConfig struct {
	Port                string `yaml:"port"`
	LogLevel            string `yaml:"logLevel"`
	AuthCodeTTL         string `yaml:"authCodeTTL"`
	AccessTokenTTL      string `yaml:"accessTokenTTL"`
	PublicURL           string `yaml:"publicURL"`
	InviteExpiry        string `yaml:"inviteExpiry"`
	InviteAllowExisting bool   `yaml:"allowInviteExisting"`

	// Parsed durations (set after Load)
	AuthCodeTTLParsed    time.Duration `yaml:"-"`
	AccessTokenTTLParsed time.Duration `yaml:"-"`
	InviteExpiryParsed   time.Duration `yaml:"-"`
}

// AuthConfig controls how the web login works. Mode is one of "internal"
// (username/password managed by this service) or "sso" (external identity
// provider). Invitation flows only apply to internal mode.
type AuthConfig struct {
	Mode string `yaml:"mode"`
	// TwoFactor controls web-login 2FA. Required defaults to true; when set,
	// users must configure at least one 2FA method. Methods is the ordered
	// list of allowed 2FA methods with "totp" as the first supported method.
	TwoFactor TwoFactorConfig `yaml:"twoFactor"`
}

// TwoFactorConfig configures two-factor authentication on the "/web" login.
// Required is a *bool so that "unset" (nil, defaults to true) can be
// distinguished from an explicit false (which disables enforcement).
type TwoFactorConfig struct {
	Required *bool    `yaml:"required"`
	Methods  []string `yaml:"methods"`
}

// TwoFactorRequired reports whether 2FA enforcement is on. Defaults to true
// when unset.
func (c *TwoFactorConfig) TwoFactorRequired() bool {
	if c == nil || c.Required == nil {
		return true
	}
	return *c.Required
}

// SMTPConfig holds outbound email settings for invitations and notifications.
type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
	FromName string `yaml:"fromName"`
	UseTLS   bool   `yaml:"useTLS"`
	// Timeout bounds each SMTP operation (dial and the whole conversation).
	// Accepts Go duration strings like "10s". Zero means the mailer's default.
	Timeout string `yaml:"timeout"`

	// Parsed duration (set after Load).
	TimeoutParsed time.Duration `yaml:"-"`
}

// IsInternalAuth reports whether invitations are applicable in the current
// auth mode.
func (c *InternalConfig) IsInternalAuth() bool {
	return c.Auth.Mode == "" || strings.EqualFold(c.Auth.Mode, "internal")
}

type BackendConfig struct {
	Command           string            `yaml:"command"`
	PoolSize          int               `yaml:"poolSize"`
	MinPoolSize       int               `yaml:"minPoolSize"`
	MaxPoolSize       int               `yaml:"maxPoolSize"`
	Env               map[string]string `yaml:"env"`
	Secrets           []SecretRef       `yaml:"secrets"`
	ToolPrefix        string            `yaml:"toolPrefix"`
	SelfReporting     bool              `yaml:"selfReporting"`
	NoKeysRequired    bool              `yaml:"noKeysRequired"`
	SkipJustification bool              `yaml:"skipJustification"`
	IsSystem          bool              `yaml:"isSystem"`
	StdioFraming      string            `yaml:"stdioFraming"`
	Enabled           *bool             `yaml:"enabled"`
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

	// Auth mode defaults to internal.
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = "internal"
	}

	// Two-factor auth defaults: required by default, TOTP as the first method.
	if len(cfg.Auth.TwoFactor.Methods) == 0 {
		cfg.Auth.TwoFactor.Methods = []string{"totp"}
	}

	// SMTP defaults.
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = 587
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

	cfg.Server.InviteExpiryParsed = parseDuration(cfg.Server.InviteExpiry)
	if cfg.Server.InviteExpiryParsed == 0 {
		cfg.Server.InviteExpiryParsed = 7 * 24 * time.Hour
	}

	// Environment overrides. These take precedence over the config file so that
	// the same config.yaml can ship to multiple environments.
	if v := os.Getenv("PUBLIC_URL"); v != "" {
		cfg.Server.PublicURL = v
	}
	if v := os.Getenv("SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = n
		}
	}
	if v := os.Getenv("SMTP_USER"); v != "" {
		cfg.SMTP.User = v
	}
	if v := os.Getenv("SMTP_PASSWORD"); v != "" {
		cfg.SMTP.Password = v
	}
	if v := os.Getenv("SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
	if v := os.Getenv("SMTP_FROM_NAME"); v != "" {
		cfg.SMTP.FromName = v
	}
	if v := os.Getenv("INVITE_ALLOW_EXISTING"); v != "" {
		cfg.Server.InviteAllowExisting = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("SMTP_TIMEOUT"); v != "" {
		cfg.SMTP.Timeout = v
	}
	cfg.SMTP.TimeoutParsed = parseDuration(cfg.SMTP.Timeout)

	if err := cfg.ValidateEncryption(); err != nil {
		return nil, fmt.Errorf("encryption config validation failed: %w", err)
	}

	return &cfg, nil
}
