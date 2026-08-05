package mailer

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// startTestSmtpServer starts a minimal in-process SMTP server on a random port
// that speaks just enough SMTP to exercise the mailer's Send path. It records
// the recipients and message body it receives.
func startTestSmtpServer(t *testing.T) (addr string, getMsg func() string, getRcpts func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var (
		mu      chan struct{} = make(chan struct{}, 1)
		msgBody string
		rcpts   []string
	)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				w := bufio.NewWriter(c)
				say := func(s string) {
					fmt.Fprintf(w, "%s\r\n", s)
					w.Flush()
				}
				say("220 test ESMTP ready")
				inData := false
				var buf strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if inData {
						if line == "." {
							inData = false
							say("250 2.6.0 Queued")
							mu <- struct{}{}
							msgBody = buf.String()
							<-mu
							continue
						}
						buf.WriteString(line + "\n")
						continue
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						say("250-test.example.com Hello")
						say("250 OK")
					case strings.HasPrefix(line, "MAIL FROM:"):
						say("250 2.1.0 OK")
					case strings.HasPrefix(line, "RCPT TO:"):
						rcpts = append(rcpts, line)
						say("250 2.1.5 OK")
					case line == "DATA":
						say("354 End data with <CR><LF>.<CR><LF>")
						inData = true
					case line == "QUIT":
						say("221 Bye")
						return
					case strings.HasPrefix(line, "AUTH"):
						say("503 5.5.1 Authentication not available")
					default:
						say("250 OK")
					}
				}
			}(conn)
		}
	}()

	getMsg = func() string {
		mu <- struct{}{}
		defer func() { <-mu }()
		return msgBody
	}
	getRcpts = func() []string {
		mu <- struct{}{}
		defer func() { <-mu }()
		return rcpts
	}
	return ln.Addr().String(), getMsg, getRcpts
}

func TestSend_AnonymousRelay(t *testing.T) {
	addr, getMsg, getRcpts := startTestSmtpServer(t)
	h, p := splitAddr(t, addr)
	s := NewSmtpSender(SmtpConfig{Host: h, Port: p, From: "noreply@tuskerdirect.com"})
	if err := s.Send([]string{"karl@tuskerdirect.com"}, "Subject line", "Hello body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	rcpts := getRcpts()
	if len(rcpts) != 1 || !strings.Contains(rcpts[0], "karl@tuskerdirect.com") {
		t.Errorf("unexpected recipients: %v", rcpts)
	}
	msg := getMsg()
	for _, want := range []string{"From: noreply@tuskerdirect.com", "To: karl@tuskerdirect.com", "Subject: Subject line", "Hello body"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

func TestSend_NoHostIsNoop(t *testing.T) {
	s := NewSmtpSender(SmtpConfig{Host: ""})
	if err := s.Send([]string{"a@test.com"}, "s", "b"); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestSend_EmptyRecipientsIsNoop(t *testing.T) {
	s := NewSmtpSender(SmtpConfig{Host: "smtp.example.com", Port: 25})
	if err := s.Send(nil, "s", "b"); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

func TestSend_WithAuthUser(t *testing.T) {
	addr, _, _ := startTestSmtpServer(t)
	h, p := splitAddr(t, addr)
	s := NewSmtpSender(SmtpConfig{Host: h, Port: p, User: "u", Password: "pw", From: "a@test.com"})
	// The fake server returns 503 for AUTH, so Send should error out.
	if err := s.Send([]string{"b@test.com"}, "s", "b"); err == nil {
		t.Error("expected error when AUTH fails")
	}
}

func TestSend_UnreachableServer(t *testing.T) {
	s := NewSmtpSender(SmtpConfig{Host: "127.0.0.1", Port: 1, From: "a@test.com"})
	if err := s.Send([]string{"b@test.com"}, "s", "b"); err == nil {
		t.Error("expected error for unreachable server")
	}
}

// startStalledSmtpServer accepts a connection but never responds, so the
// client's dial succeeds but every SMTP command would hang without a timeout.
func startStalledSmtpServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				// Read forever, never reply. The client's deadline will fire.
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestSend_StalledServerTimesOut(t *testing.T) {
	addr := startStalledSmtpServer(t)
	h, p := splitAddr(t, addr)
	s := NewSmtpSender(SmtpConfig{Host: h, Port: p, From: "a@test.com", Timeout: 250 * time.Millisecond})

	start := time.Now()
	err := s.Send([]string{"b@test.com"}, "s", "b")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from stalled server")
	}
	// Allow generous headroom for slow CI machines, but ensure it does not hang.
	if elapsed > 3*time.Second {
		t.Errorf("Send took %v, expected to respect timeout", elapsed)
	}
}

func TestSend_DialTimeoutFailsFast(t *testing.T) {
	// Port 1 on 127.0.0.1 refuses instantly; the point is the dialer honors a
	// short timeout instead of blocking forever.
	s := NewSmtpSender(SmtpConfig{Host: "127.0.0.1", Port: 1, From: "a@test.com", Timeout: 250 * time.Millisecond})
	start := time.Now()
	err := s.Send([]string{"b@test.com"}, "s", "b")
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("dial took %v, expected to respect timeout", elapsed)
	}
}

// splitAddr splits "host:port" from the test server's listener address.
func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return host, port
}
