// Package mailer sends transactional emails (e.g. user invitations) over SMTP
// using only the standard library. It exposes a Sender interface so callers can
// inject a fake in tests.
package mailer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SmtpConfig holds SMTP connection details. Host may be empty, in which case
// NewSmtpSender returns a sender whose Send is a no-op (email disabled).
type SmtpConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	FromName string
	UseTLS   bool
}

// Sender is the interface used by web handlers to deliver email.
type Sender interface {
	Send(to []string, subject, body string) error
}

// BuildAddr returns the host:port dial address for the SMTP server.
func (c SmtpConfig) BuildAddr() string {
	return net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))
}

type smtpSender struct {
	cfg SmtpConfig
}

// NewSmtpSender creates a Sender from an SmtpConfig. If Host is empty the
// returned sender silently drops messages (useful when SMTP is unconfigured).
func NewSmtpSender(cfg SmtpConfig) Sender {
	return &smtpSender{cfg: cfg}
}

func (s *smtpSender) Send(to []string, subject, body string) error {
	if s.cfg.Host == "" {
		return nil
	}
	if len(to) == 0 {
		return nil
	}

	from := s.cfg.From
	if from == "" {
		from = "mcp-bridge@localhost"
	}

	addr := s.cfg.BuildAddr()
	msg := buildMessage(s.cfg, from, to, subject, body)

	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)
	}

	if s.cfg.Port == 465 || s.cfg.UseTLS {
		// Explicit TLS: dial, wrap, then send via SMTP.
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		client, err := smtp.NewClient(conn, s.cfg.Host)
		if err != nil {
			conn.Close()
			return err
		}
		return deliver(client, auth, from, to, msg)
	}

	// Plain SMTP with STARTTLS when available.
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			client.Close()
			// Anonymous relay doesn't require TLS; fall back to plain.
			client, err = smtp.Dial(addr)
			if err != nil {
				return err
			}
		}
	}
	return deliver(client, auth, from, to, msg)
}

func deliver(client *smtp.Client, auth smtp.Auth, from string, to []string, msg string) error {
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// BuildInviteEmail renders the full RFC 5322 message for an invitation. The
// invite URL must already contain the one-time token. expiryDays of 0 falls
// back to the default 7-day message wording. from is the address used in both
// the envelope and the From header.
func BuildInviteEmail(from, toEmail, name, inviteURL string, expiry time.Duration) (string, error) {
	recipient := toEmail
	if name != "" {
		recipient = fmt.Sprintf("%s <%s>", name, toEmail)
	}
	if _, err := mail.ParseAddress(recipient); err != nil {
		return "", err
	}

	expiryDays := int(expiry.Hours() / 24)
	if expiryDays < 1 {
		expiryDays = 7
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", recipient))
	b.WriteString(fmt.Sprintf("Subject: Invitation to mcp-bridge\r\n"))
	b.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b, `<p>Hello%s,</p>`, greetingName(name))
	b.WriteString(`<p>You have been invited to join mcp-bridge. To set up your account, click the link below:</p>`)
	fmt.Fprintf(&b, `<p><a href="%s">Accept invitation</a></p>`, inviteURL)
	fmt.Fprintf(&b, `<p>This invitation will expire in %d days.</p>`, expiryDays)
	b.WriteString(`<p>If you did not expect this invitation, you can safely ignore this email.</p>`)
	return b.String(), nil
}

func greetingName(name string) string {
	if name == "" {
		return ""
	}
	return " " + name
}

func buildMessage(cfg SmtpConfig, from string, to []string, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}

// ParseRecipients splits a form-supplied recipients field into individual
// validated addresses. It accepts bare emails, "Name <email>" display names,
// and comma- or newline-separated lists. The display name is dropped; only
// addresses are returned.
func ParseRecipients(input string) ([]string, error) {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		addr, err := mail.ParseAddress(f)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient %q: %w", f, err)
		}
		out = append(out, addr.Address)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no recipients provided")
	}
	return out, nil
}
