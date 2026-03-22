package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptionConfig_Validate_EnvVar(t *testing.T) {
	cfg := EncryptionConfig{
		Provider: "envvar",
		KeyEnv:   "MY_KEY",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid config with KeyEnv, got error: %v", err)
	}

	cfg2 := EncryptionConfig{
		Provider:   "envvar",
		KeyFileEnv: "MY_KEY_FILE",
	}
	if err := cfg2.Validate(); err != nil {
		t.Errorf("expected valid config with KeyFileEnv, got error: %v", err)
	}
}

func TestEncryptionConfig_Validate_K8s(t *testing.T) {
	cfg := EncryptionConfig{
		Provider:      "k8s",
		K8sSecretPath: "/var/run/secrets/encryption",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid k8s config, got error: %v", err)
	}
}

func TestEncryptionConfig_Validate_Missing(t *testing.T) {
	cfg := EncryptionConfig{
		Provider:          "envvar",
		RequireEncryption: true,
		KeyEnv:            "TEST_KEY",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid with KeyEnv and encryption required, got: %v", err)
	}

	cfg2 := EncryptionConfig{
		Provider:          "",
		RequireEncryption: true,
	}
	if err := cfg2.Validate(); err == nil {
		t.Error("expected error when encryption required but no provider configured")
	}

	cfg3 := EncryptionConfig{
		Provider: "k8s",
	}
	if err := cfg3.Validate(); err == nil {
		t.Error("expected error when k8s provider missing secret path")
	}

	cfg4 := EncryptionConfig{
		Provider: "unknown",
	}
	if err := cfg4.Validate(); err == nil {
		t.Error("expected error for unknown provider")
	}

	cfg5 := EncryptionConfig{
		Provider: "envvar",
		KeyEnv:   "TEST_KEY",
	}
	if err := cfg5.Validate(); err != nil {
		t.Errorf("expected valid config with KeyEnv, got: %v", err)
	}

	cfg6 := EncryptionConfig{
		Provider:   "envvar",
		KeyFileEnv: "TEST_KEY_FILE",
	}
	if err := cfg6.Validate(); err != nil {
		t.Errorf("expected valid config with KeyFileEnv, got: %v", err)
	}

	cfg7 := EncryptionConfig{
		Provider:          "envvar",
		KeyEnv:            "TEST_KEY",
		RequireEncryption: false,
	}
	if err := cfg7.Validate(); err != nil {
		t.Errorf("expected valid config with KeyEnv, got: %v", err)
	}
}

func TestEncryptionConfig_NewKEKProvider(t *testing.T) {
	t.Run("envvar with KeyEnv", func(t *testing.T) {
		cfg := EncryptionConfig{
			Provider: "envvar",
			KeyEnv:   "TEST_KEY",
		}
		provider, err := cfg.NewKEKProvider()
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if provider == nil {
			t.Fatal("expected provider, got nil")
		}
	})

	t.Run("envvar with KeyFileEnv", func(t *testing.T) {
		cfg := EncryptionConfig{
			Provider:   "envvar",
			KeyFileEnv: "TEST_KEY_FILE",
		}
		provider, err := cfg.NewKEKProvider()
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if provider == nil {
			t.Fatal("expected provider, got nil")
		}
	})

	t.Run("k8s", func(t *testing.T) {
		cfg := EncryptionConfig{
			Provider:      "k8s",
			K8sSecretPath: "/tmp/test-secrets",
			K8sKeyName:    "test.key",
		}
		provider, err := cfg.NewKEKProvider()
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if provider == nil {
			t.Fatal("expected provider, got nil")
		}
	})
}

func TestEncryptionConfig_NewKEKProvider_Unknown(t *testing.T) {
	cfg := EncryptionConfig{
		Provider: "unknown",
	}
	_, err := cfg.NewKEKProvider()
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestLoad_Encryption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`
server:
  port: "8080"
encryption:
  provider: "envvar"
  keyEnv: "MY_ENCRYPTION_KEY"
  requireEncryption: false
backends:
  test:
    command: "echo"
    poolSize: 1
`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Encryption.Provider != "envvar" {
		t.Errorf("expected provider envvar, got %s", cfg.Encryption.Provider)
	}
	if cfg.Encryption.KeyEnv != "MY_ENCRYPTION_KEY" {
		t.Errorf("expected keyEnv MY_ENCRYPTION_KEY, got %s", cfg.Encryption.KeyEnv)
	}
	if cfg.Encryption.RequireEncryption != false {
		t.Errorf("expected requireEncryption false, got %v", cfg.Encryption.RequireEncryption)
	}
}

func TestLoad_EncryptionK8s(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`
server:
  port: "8080"
encryption:
  provider: "k8s"
  k8sSecretPath: "/var/run/secrets/encryption"
  k8sKeyName: "master.key"
backends:
  test:
    command: "echo"
    poolSize: 1
`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Encryption.Provider != "k8s" {
		t.Errorf("expected provider k8s, got %s", cfg.Encryption.Provider)
	}
	if cfg.Encryption.K8sSecretPath != "/var/run/secrets/encryption" {
		t.Errorf("expected k8sSecretPath, got %s", cfg.Encryption.K8sSecretPath)
	}
	if cfg.Encryption.K8sKeyName != "master.key" {
		t.Errorf("expected k8sKeyName master.key, got %s", cfg.Encryption.K8sKeyName)
	}
}

func TestLoad_EncryptionRequireFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`
server:
  port: "8080"
encryption:
  requireEncryption: true
backends:
  test:
    command: "echo"
    poolSize: 1
`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error when encryption required but no provider")
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`
server:
  port: "9090"
  logLevel: debug
backends:
  filesystem:
    command: "npx -y @modelcontextprotocol/server-filesystem /tmp"
    poolSize: 2
    toolPrefix: fs
    env: {}
    secrets:
      - name: fs-token
        envKey: FS_API_KEY
        context: user
  fetch:
    command: "npx -y @modelcontextprotocol/server-fetch"
    poolSize: 1
    toolPrefix: fetch
`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.Port != "9090" {
		t.Errorf("expected port 9090, got %s", cfg.Server.Port)
	}

	if cfg.Server.LogLevel != "debug" {
		t.Errorf("expected logLevel debug, got %s", cfg.Server.LogLevel)
	}

	if len(cfg.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(cfg.Backends))
	}

	fs := cfg.Backends["filesystem"]
	if fs.Command != "npx -y @modelcontextprotocol/server-filesystem /tmp" {
		t.Errorf("expected filesystem command, got %s", fs.Command)
	}
	if fs.PoolSize != 2 {
		t.Errorf("expected filesystem pool size 2, got %d", fs.PoolSize)
	}
	if fs.ToolPrefix != "fs" {
		t.Errorf("expected filesystem tool prefix, got %s", fs.ToolPrefix)
	}
	if len(fs.Secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(fs.Secrets))
	}
	if fs.Secrets[0].Name != "fs-token" {
		t.Errorf("expected secret name fs-token, got %s", fs.Secrets[0].Name)
	}
	if fs.Secrets[0].EnvKey != "FS_API_KEY" {
		t.Errorf("expected secret envKey FS_API_KEY, got %s", fs.Secrets[0].EnvKey)
	}
	if fs.Secrets[0].Context != "user" {
		t.Errorf("expected secret context user, got %s", fs.Secrets[0].Context)
	}
}

func TestLoad_DefaultValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`
backends:
  echo:
    command: "cat"
    poolSize: 1
`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.Port != "8080" {
		t.Errorf("expected default port 8080, got %s", cfg.Server.Port)
	}
	if cfg.Server.LogLevel != "info" {
		t.Errorf("expected default logLevel info, got %s", cfg.Server.LogLevel)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(`{{{invalid yaml`)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoad_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := []byte(``)

	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("failed to load empty config: %v", err)
	}

	if cfg.Server.Port != "8080" {
		t.Errorf("expected default port 8080, got %s", cfg.Server.Port)
	}
	if cfg.Server.LogLevel != "info" {
		t.Errorf("expected default logLevel info, got %s", cfg.Server.LogLevel)
	}
	if cfg.Backends != nil && len(cfg.Backends) != 0 {
		t.Errorf("expected no backends, got %d", len(cfg.Backends))
	}
}
