package config

import "testing"

func TestLoad_DockerConfigFile(t *testing.T) {
	cfg, err := Load("../config.yaml.docker")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Mode != "internal" {
		t.Errorf("expected internal mode, got %s", cfg.Auth.Mode)
	}
	if cfg.Server.PublicURL != "https://mcp-bridge.staging6.tuskeraws.com" {
		t.Errorf("unexpected publicURL %s", cfg.Server.PublicURL)
	}
	if cfg.SMTP.Host != "mail.tuskerdirect.com" || cfg.SMTP.Port != 25 {
		t.Errorf("unexpected smtp %s:%d", cfg.SMTP.Host, cfg.SMTP.Port)
	}
	if cfg.Server.InviteExpiryParsed.Hours() != 168 {
		t.Errorf("unexpected inviteExpiry %v", cfg.Server.InviteExpiryParsed)
	}
	if !cfg.Server.InviteAllowExisting {
		t.Error("expected allowInviteExisting true in config.yaml.docker")
	}
}
