package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
