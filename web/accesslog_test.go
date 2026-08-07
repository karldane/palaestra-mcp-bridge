package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/shared"
)

// captureStdout runs f while redirecting os.Stdout to a buffer and returns
// everything printed. The caller is responsible for the global logger level.
func captureStdout(f func()) string {
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	f()

	os.Stdout = orig
	_ = w.Close()
	<-done
	return buf.String()
}

// TestSetup2FA_DebugLogs_EnableFailure ensures that when debugging is enabled,
// a rejected confirmation code logs both the access line and the Enable-failed
// diagnostic, without ever exposing the secret or submitted code.
func TestSetup2FA_DebugLogs_EnableFailure(t *testing.T) {
	shared.SetLogLevel("debug")
	defer shared.SetLogLevel("info")

	h, _ := newTwoFAHandler(t, true)
	u := seedRegularUser(t, h.Store)

	mux := http.NewServeMux()
	h.Register(mux)

	_, pending := doLogin(h, mux, u.Email, "pass")
	if pending == "" {
		t.Fatal("expected pending cookie")
	}
	getReq := authedRequest(http.MethodGet, "/web/setup-2fa", "", &http.Cookie{Name: pendingCookieName, Value: pending})
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	var setupCookie string
	for _, c := range getW.Result().Cookies() {
		if c.Name == setupCookieName {
			setupCookie = c.Value
		}
	}
	if setupCookie == "" {
		t.Fatal("expected setup cookie")
	}

	postReq := authedRequest(http.MethodPost, "/web/setup-2fa", url.Values{"code": {"000000"}}.Encode(), &http.Cookie{Name: setupCookieName, Value: setupCookie})

	logOut := captureStdout(func() {
		mux.ServeHTTP(httptest.NewRecorder(), postReq)
	})

	if !strings.Contains(logOut, "web setup-2fa POST:") {
		t.Fatalf("expected setup-2fa debug envelope in logs, got:\n%s", logOut)
	}
	if !strings.Contains(logOut, "Enable failed") {
		t.Fatalf("expected Enable-failed debug line for wrong code, got:\n%s", logOut)
	}
	if strings.Contains(logOut, "000000") {
		t.Fatalf("debug log must not contain the submitted code, got:\n%s", logOut)
	}
}

// TestAccessLog_RestrictedWhenNotDebug verifies that without debug logging an
// access line is suppressed (the global default info).
func TestAccessLog_RestrictedWhenNotDebug(t *testing.T) {
	shared.SetLogLevel("info")
	h, _ := newTwoFAHandler(t, false)

	sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	logOut := captureStdout(func() {
		h.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(sw, httptest.NewRequest(http.MethodGet, "/web/dashboard", nil))
	})
	if strings.Contains(logOut, "web ") {
		t.Fatalf("expected no access log at info level, got:\n%s", logOut)
	}
}
