package mailer

import (
	"strings"
	"testing"
	"time"
)

func TestBuildInviteEmail(t *testing.T) {
	msg, err := BuildInviteEmail("noreply@tuskerdirect.com", "karl.dane@tuskerdirect.com", "Karl Dane", "https://mcp.example.com/web/invite?token=abc123", time.Duration(7*24)*time.Hour)
	if err != nil {
		t.Fatalf("BuildInviteEmail: %v", err)
	}
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
	for _, want := range []string{
		"From: noreply@tuskerdirect.com", "To:", "Subject:", "Karl Dane",
		"https://mcp.example.com/web/invite?token=abc123",
		"7 days",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q", want)
		}
	}
}

func TestBuildInviteEmail_DefaultExpiry(t *testing.T) {
	msg, err := BuildInviteEmail("noreply@tuskerdirect.com", "a@test.com", "", "https://x.example/web/invite?token=t", 0)
	if err != nil {
		t.Fatalf("BuildInviteEmail: %v", err)
	}
	if !strings.Contains(msg, "7 days") {
		t.Error("expected default 7 day expiry in message")
	}
}

func TestSmtpConfig_BuildAddr(t *testing.T) {
	cfg := SmtpConfig{Host: "smtp.example.com", Port: 587}
	if got := cfg.BuildAddr(); got != "smtp.example.com:587" {
		t.Errorf("BuildAddr = %q, want smtp.example.com:587", got)
	}
}

func TestNewSmtpSender_NoServer(t *testing.T) {
	cfg := SmtpConfig{Host: "", Port: 587, From: "noop@example.com"}
	s := NewSmtpSender(cfg)
	if s == nil {
		t.Fatal("expected sender even with no SMTP host")
	}
}

type fakeSender struct {
	calls int
}

func (f *fakeSender) Send(_ []string, _, _ string) error {
	f.calls++
	return nil
}

func TestParseRecipients(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{"bare", "a@test.com", 1, false},
		{"display name", "A B <a@test.com>", 1, false},
		{"comma separated", "a@test.com, b@test.com", 2, false},
		{"newline separated", "a@test.com\nb@test.com", 2, false},
		{"invalid", "not-an-email", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRecipients(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRecipients: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("got %d recipients, want %d", len(got), tt.wantLen)
			}
		})
	}
}
