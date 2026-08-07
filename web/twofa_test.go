package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/internal/twofa"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// newTwoFAHandler builds a test Handler with 2FA enabled for the TOTP method.
func newTwoFAHandler(t *testing.T, required bool) (*Handler, *store.Store) {
	t.Helper()
	h, st := testHandler(t)
	mgr, err := twofa.NewManager(st, []string{"totp"}, required)
	if err != nil {
		t.Fatalf("twofa.NewManager: %v", err)
	}
	h.TwoFA = mgr
	h.TwoFAMethods = []string{"totp"}
	return h, st
}

// enable2FA configures a fresh TOTP secret on the user and returns the
// plaintext secret for generating codes in tests. plaintextPassword must match
// the user's real login password (before bcrypt hashing) so the DEK used to
// wrap the secret equals the DEK derived during login.
func enable2FA(t *testing.T, h *Handler, u *store.User, plaintextPassword string) string {
	t.Helper()
	var salt string
	if u.PasswordSalt == "" {
		var err error
		salt, err = crypto.GenerateSalt()
		if err != nil {
			t.Fatalf("GenerateSalt: %v", err)
		}
		u.PasswordSalt = salt
		if err := h.Store.UpdateUser(u); err != nil {
			t.Fatalf("UpdateUser: %v", err)
		}
	}
	dek, err := crypto.DeriveUserDEK(plaintextPassword, u.PasswordSalt)
	if err != nil {
		t.Fatalf("DeriveUserDEK: %v", err)
	}
	res, err := h.TwoFA.Setup(u.Email, "totp")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	code, err := totp.GenerateCode(res.Secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if err := h.TwoFA.Enable(u.ID, "totp", []byte(res.Secret), code, dek); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return res.Secret
}

// doLogin posts to /web/login and returns the pending-cookie value (empty if
// none) and the response recorder.
func doLogin(h *Handler, mux *http.ServeMux, email, password string) (*httptest.ResponseRecorder, string) {
	form := url.Values{"email": {email}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var pending string
	for _, c := range w.Result().Cookies() {
		if c.Name == pendingCookieName {
			pending = c.Value
		}
	}
	return w, pending
}

func TestLogin_2FAChallenge_Redirects(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)

	w, pending := doLogin(h, mux, u.Email, "pass")
	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect to 2FA challenge, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/web/login/2fa" {
		t.Fatalf("expected Location /web/login/2fa, got %s", loc)
	}
	if pending == "" {
		t.Fatal("expected pending cookie to be set")
	}
	if _, ok := pendingLogins[pending]; !ok {
		t.Fatal("expected pending login to be registered")
	}
}

func TestLogin_2FAChallenge_WrongCode(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)

	_, pending := doLogin(h, mux, u.Email, "pass")
	chReq := authedRequest(http.MethodPost, "/web/login/2fa", url.Values{"code": {"000000"}}.Encode(), &http.Cookie{Name: pendingCookieName, Value: pending})
	chW := httptest.NewRecorder()
	mux.ServeHTTP(chW, chReq)
	if chW.Code != http.StatusOK {
		t.Fatalf("expected challenge re-render (200), got %d", chW.Code)
	}
	if !strings.Contains(chW.Body.String(), "Invalid code") {
		t.Fatalf("expected invalid-code error message, got: %s", chW.Body.String())
	}
	// Wrong code must not have consumed the pending login or created a session.
	if _, ok := pendingLogins[pending]; !ok {
		t.Fatal("expected pending login to survive a wrong code")
	}
	for _, c := range chW.Result().Cookies() {
		if c.Name == sessionCookieName {
			t.Fatal("expected no session cookie on wrong 2FA code")
		}
	}
}

func TestLogin_2FAFlow_Success(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	secret := enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)

	_, pending := doLogin(h, mux, u.Email, "pass")
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	chReq := authedRequest(http.MethodPost, "/web/login/2fa", url.Values{"code": {code}}.Encode(), &http.Cookie{Name: pendingCookieName, Value: pending})
	chW := httptest.NewRecorder()
	mux.ServeHTTP(chW, chReq)
	if chW.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after successful 2FA, got %d", chW.Code)
	}
	var session *http.Cookie
	for _, c := range chW.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("expected session cookie after successful 2FA")
	}
	if _, ok := pendingLogins[pending]; ok {
		t.Fatal("expected pending login to be removed after 2FA success")
	}
}

func TestLogin_Forced2FA_RedirectsToSetup(t *testing.T) {
	h, st := newTwoFAHandler(t, true)
	defer st.Close()
	u := seedRegularUser(t, st) // no 2FA configured

	mux := http.NewServeMux()
	h.Register(mux)

	w, _ := doLogin(h, mux, u.Email, "pass")
	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/web/setup-2fa" {
		t.Fatalf("expected Location /web/setup-2fa, got %s", loc)
	}
}

func TestLogin_No2FA_CompletesNormally(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)

	mux := http.NewServeMux()
	h.Register(mux)

	w, _ := doLogin(h, mux, u.Email, "pass")
	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/web/" {
		t.Fatalf("expected Location /web/, got %s", loc)
	}
	_ = u
}

func TestTwoFASettings_NotEnabled(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	seedRegularUser(t, st)

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	req := authedRequest(http.MethodGet, "/web/2fa", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Not Enabled") {
		t.Fatalf("expected 'Not Enabled' state, got: %s", body)
	}
	if !strings.Contains(body, "/web/setup-2fa") {
		t.Fatalf("expected setup link on settings page")
	}
}

func TestTwoFASettings_Disable(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	secret := enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginWith2FA(t, h, mux, u.Email, "pass", secret)

	req := authedRequest(http.MethodPost, "/web/2fa", url.Values{"action": {"disable"}}.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	_, enabled, err := h.Store.GetUser2FA(u.ID)
	if err != nil {
		t.Fatalf("GetUser2FA: %v", err)
	}
	if enabled {
		t.Fatal("expected 2FA to be disabled")
	}
}

func TestTwoFAAdmin_Reset(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	seedAdmin(t, st)
	target := seedRegularUser(t, st)
	enable2FA(t, h, target, "pass")

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodPost, "/web/admin/users/2fa-reset", url.Values{"user_id": {target.ID}}.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	_, enabled, _ := h.Store.GetUser2FA(target.ID)
	if enabled {
		t.Fatal("expected target 2FA to be reset")
	}
}

// loginWith2FA performs a full login that includes the TOTP 2FA step and
// returns the resulting session cookie.
func loginWith2FA(t *testing.T, h *Handler, mux *http.ServeMux, email, password, secret string) *http.Cookie {
	t.Helper()
	_, pending := doLogin(h, mux, email, password)
	if pending == "" {
		t.Fatal("loginWith2FA: pending cookie not set")
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	chReq := authedRequest(http.MethodPost, "/web/login/2fa", url.Values{"code": {code}}.Encode(), &http.Cookie{Name: pendingCookieName, Value: pending})
	chW := httptest.NewRecorder()
	mux.ServeHTTP(chW, chReq)
	if chW.Code != http.StatusSeeOther {
		t.Fatalf("loginWith2FA: expected 303 after 2FA, got %d", chW.Code)
	}
	for _, c := range chW.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("loginWith2FA: no session cookie set")
	return nil
}

func TestLogin2FAGet_RendersChallenge(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)
	_, pending := doLogin(h, mux, u.Email, "pass")

	req := authedRequest(http.MethodGet, "/web/login/2fa", "", &http.Cookie{Name: pendingCookieName, Value: pending})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "verification code") {
		t.Fatalf("expected challenge page, got: %s", w.Body.String())
	}
}

func TestLogin2FAGet_NoPending_RedirectsToLogin(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/web/login/2fa", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/web/login" {
		t.Fatalf("expected redirect to /web/login, got %s", loc)
	}
}

func TestSetup2FA_Forced_CompletesLogin(t *testing.T) {
	h, st := newTwoFAHandler(t, true)
	defer st.Close()
	u := seedRegularUser(t, st)

	mux := http.NewServeMux()
	h.Register(mux)
	_, pending := doLogin(h, mux, u.Email, "pass")
	if pending == "" {
		t.Fatal("expected pending cookie")
	}

	// GET the setup page (forced enrollment for an email-only login).
	getReq := authedRequest(http.MethodGet, "/web/setup-2fa", "", &http.Cookie{Name: pendingCookieName, Value: pending})
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 for setup GET, got %d", getW.Code)
	}
	if !strings.Contains(getW.Body.String(), "Two-Factor Authentication") {
		t.Fatalf("expected setup page, got: %s", getW.Body.String())
	}
	var setupCookie string
	for _, c := range getW.Result().Cookies() {
		if c.Name == setupCookieName {
			setupCookie = c.Value
		}
	}
	if setupCookie == "" {
		t.Fatal("expected setup cookie")
	}
	ps, ok := pendingSetups[setupCookie]
	if !ok {
		t.Fatal("expected pendingSetup registered")
	}

	// Confirm with a valid code.
	code, err := totp.GenerateCode(ps.Secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	postReq := authedRequest(http.MethodPost, "/web/setup-2fa", url.Values{"code": {code}}.Encode(), &http.Cookie{Name: setupCookieName, Value: setupCookie})
	postW := httptest.NewRecorder()
	mux.ServeHTTP(postW, postReq)
	if postW.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 after setup confirm, got %d", postW.Code)
	}
	var session *http.Cookie
	for _, c := range postW.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("expected session cookie after forced enrollment")
	}
	method, enabled, err := h.Store.GetUser2FA(u.ID)
	if err != nil {
		t.Fatalf("GetUser2FA: %v", err)
	}
	if !enabled || method != "totp" {
		t.Fatalf("expected 2FA enabled with totp, enabled=%v method=%s", enabled, method)
	}
}

func TestSetup2FA_Forced_WrongCode(t *testing.T) {
	h, st := newTwoFAHandler(t, true)
	defer st.Close()
	u := seedRegularUser(t, st)

	mux := http.NewServeMux()
	h.Register(mux)
	_, pending := doLogin(h, mux, u.Email, "pass")
	getReq := authedRequest(http.MethodGet, "/web/setup-2fa", "", &http.Cookie{Name: pendingCookieName, Value: pending})
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	var setupCookie string
	for _, c := range getW.Result().Cookies() {
		if c.Name == setupCookieName {
			setupCookie = c.Value
		}
	}

	postReq := authedRequest(http.MethodPost, "/web/setup-2fa", url.Values{"code": {"000000"}}.Encode(), &http.Cookie{Name: setupCookieName, Value: setupCookie})
	postW := httptest.NewRecorder()
	mux.ServeHTTP(postW, postReq)
	if postW.Code != http.StatusOK {
		t.Fatalf("expected 200 re-render, got %d", postW.Code)
	}
	if !strings.Contains(postW.Body.String(), "Invalid code") {
		t.Fatalf("expected invalid code error, got: %s", postW.Body.String())
	}
	_, enabled, _ := h.Store.GetUser2FA(u.ID)
	if enabled {
		t.Fatal("expected 2FA NOT enabled after wrong code")
	}
}

func TestSetup2FA_SelfService_FromSettings(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	// GET the setup page from an authenticated session (no pending login).
	getReq := authedRequest(http.MethodGet, "/web/setup-2fa", "", cookie)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 for setup GET, got %d", getW.Code)
	}
	if !strings.Contains(getW.Body.String(), "Two-Factor Authentication") {
		t.Fatalf("expected setup page, got: %s", getW.Body.String())
	}
	// The QR code must render as a data URI, not be sanitized to #ZgotmplZ
	// by html/template (which does not trust data: URLs in img[src]).
	if strings.Contains(getW.Body.String(), "#ZgotmplZ") {
		t.Fatalf("QR data URI was sanitized by template: %s", getW.Body.String())
	}
	if !strings.Contains(getW.Body.String(), "data:image/png;base64,") {
		t.Fatalf("expected data-URI QR image on setup page: %s", getW.Body.String())
	}
	var setupCookie string
	for _, c := range getW.Result().Cookies() {
		if c.Name == setupCookieName {
			setupCookie = c.Value
		}
	}
	if setupCookie == "" {
		t.Fatal("expected setup cookie")
	}
	ps, ok := pendingSetups[setupCookie]
	if !ok {
		t.Fatal("expected pendingSetup registered")
	}
	if ps.Forced {
		t.Fatal("expected non-forced enrollment for self-service setup")
	}

	// Confirm with a valid code -> should redirect to settings, not log in.
	code, err := totp.GenerateCode(ps.Secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	postReq := authedRequest(http.MethodPost, "/web/setup-2fa", url.Values{"code": {code}}.Encode(), &http.Cookie{Name: setupCookieName, Value: setupCookie})
	postW := httptest.NewRecorder()
	mux.ServeHTTP(postW, postReq)
	if postW.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 after self-service setup confirm, got %d", postW.Code)
	}
	if loc := postW.Header().Get("Location"); !strings.Contains(loc, "/web/2fa") {
		t.Fatalf("expected redirect to settings, got %s", loc)
	}
	method, enabled, _ := h.Store.GetUser2FA(u.ID)
	if !enabled || method != "totp" {
		t.Fatalf("expected 2FA enabled with totp, enabled=%v method=%s", enabled, method)
	}
}

func TestPasswordChange_ReEncrypts2FA(t *testing.T) {
	h, st := newTwoFAHandler(t, false)
	defer st.Close()
	u := seedRegularUser(t, st)
	secret := enable2FA(t, h, u, "pass")

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginWith2FA(t, h, mux, u.Email, "pass", secret)

	// Change the password. This re-encrypts tokens AND the 2FA secret.
	form := url.Values{
		"current_password": {"pass"},
		"new_password":     {"newpass"},
		"confirm_password": {"newpass"},
	}
	req := authedRequest(http.MethodPost, "/web/password", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for password change, got %d", w.Code)
	}

	// The old password must no longer work, and the new password must pass
	// through the 2FA challenge using the same TOTP secret.
	oldW, _ := doLogin(h, mux, u.Email, "pass")
	if oldW.Code == http.StatusSeeOther && oldW.Header().Get("Location") == "/web/" {
		t.Fatal("old password still logs in after password change")
	}

	// Login with new password + same 2FA secret.
	newW, pending := doLogin(h, mux, u.Email, "newpass")
	if pending == "" {
		t.Fatalf("expected pending cookie after login with new password, got status %d", newW.Code)
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	chReq := authedRequest(http.MethodPost, "/web/login/2fa", url.Values{"code": {code}}.Encode(), &http.Cookie{Name: pendingCookieName, Value: pending})
	chW := httptest.NewRecorder()
	mux.ServeHTTP(chW, chReq)
	if chW.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 after 2FA with re-encrypted secret, got %d", chW.Code)
	}
	var session *http.Cookie
	for _, c := range chW.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("expected session after login with new password and 2FA")
	}
}
