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
  jira:
    command: "npx -y @xuandev/atlassian-mcp"
    poolSize: 2
    toolPrefix: jira
    env:
      JIRA_HOST: "https://example.atlassian.net"
    secrets:
      - name: jira-token
        envKey: JIRA_API_TOKEN
        context: user
  confluence:
    command: "npx -y @xuandev/confluence-mcp"
    poolSize: 1
    toolPrefix: confluence
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

	jira := cfg.Backends["jira"]
	if jira.Command != "npx -y @xuandev/atlassian-mcp" {
		t.Errorf("expected jira command, got %s", jira.Command)
	}
	if jira.PoolSize != 2 {
		t.Errorf("expected jira pool size 2, got %d", jira.PoolSize)
	}
	if jira.ToolPrefix != "jira" {
		t.Errorf("expected jira tool prefix, got %s", jira.ToolPrefix)
	}
	if jira.Env["JIRA_HOST"] != "https://example.atlassian.net" {
		t.Errorf("expected JIRA_HOST env var, got %s", jira.Env["JIRA_HOST"])
	}
	if len(jira.Secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(jira.Secrets))
	}
	if jira.Secrets[0].Name != "jira-token" {
		t.Errorf("expected secret name jira-token, got %s", jira.Secrets[0].Name)
	}
	if jira.Secrets[0].EnvKey != "JIRA_API_TOKEN" {
		t.Errorf("expected secret envKey JIRA_API_TOKEN, got %s", jira.Secrets[0].EnvKey)
	}
	if jira.Secrets[0].Context != "user" {
		t.Errorf("expected secret context user, got %s", jira.Secrets[0].Context)
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
