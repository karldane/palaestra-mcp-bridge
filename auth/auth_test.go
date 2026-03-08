package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// testSetup creates a fresh store + Handler backed by a temp SQLite DB.
func testSetup(t *testing.T) (*Handler, *store.Store, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "auth-test-*")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	h := &Handler{
		Store:    s,
		Issuer:   "http://localhost:8080",
		CodeTTL:  10 * time.Minute,
		TokenTTL: 1 * time.Hour,
	}
	return h, s, dir
}

func cleanup(s *store.Store, dir string) {
	s.Close()
	os.RemoveAll(dir)
}

// seedUser creates a test user and returns it.
func seedUser(t *testing.T, s *store.Store) *store.User {
	t.Helper()
	u := &store.User{
		ID:       "test-user-1",
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "password123",
	}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	return u
}

// seedClient creates a registered OAuth client and returns it.
func seedClient(t *testing.T, s *store.Store) *store.OAuthClient {
	t.Helper()
	c := &store.OAuthClient{
		ClientID:     "test-client-1",
		ClientSecret: "",
		RedirectURIs: `["http://localhost:9876/callback"]`,
		ClientName:   "test-app",
	}
	if err := s.CreateOAuthClient(c); err != nil {
		t.Fatal(err)
	}
	return c
}

// seedSession creates a valid OAuth session and returns it.
func seedSession(t *testing.T, s *store.Store) *store.OAuthSession {
	t.Helper()
	sess := &store.OAuthSession{
		AccessToken:  "valid-access-token",
		RefreshToken: "valid-refresh-token",
		UserID:       "test-user-1",
		ClientID:     "test-client-1",
		Scope:        "mcp",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
	}
	if err := s.CreateOAuthSession(sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

// computePKCEChallenge returns the S256 challenge for a given verifier.
func computePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---------- Metadata ----------

func TestMetadataHandler(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	h.MetadataHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var meta map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}

	if meta["issuer"] != "http://localhost:8080" {
		t.Errorf("issuer = %v", meta["issuer"])
	}
	if meta["authorization_endpoint"] != "http://localhost:8080/authorize" {
		t.Errorf("authorization_endpoint = %v", meta["authorization_endpoint"])
	}
	if meta["token_endpoint"] != "http://localhost:8080/token" {
		t.Errorf("token_endpoint = %v", meta["token_endpoint"])
	}
	if meta["registration_endpoint"] != "http://localhost:8080/register" {
		t.Errorf("registration_endpoint = %v", meta["registration_endpoint"])
	}
}

func TestMetadataHandler_MethodNotAllowed(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("POST", "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	h.MetadataHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ---------- Register Client ----------

func TestRegisterClientHandler(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	body := `{"redirect_uris":["http://localhost:9876/callback"],"client_name":"opencode"}`
	req := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.RegisterClientHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var resp clientRegistrationResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ClientID == "" {
		t.Error("ClientID is empty")
	}
	if resp.ClientName != "opencode" {
		t.Errorf("ClientName = %q, want opencode", resp.ClientName)
	}
	if len(resp.RedirectURIs) != 1 || resp.RedirectURIs[0] != "http://localhost:9876/callback" {
		t.Errorf("RedirectURIs = %v", resp.RedirectURIs)
	}

	// Verify stored in DB
	_, err := s.GetOAuthClient(resp.ClientID)
	if err != nil {
		t.Errorf("client not found in DB: %v", err)
	}
}

func TestRegisterClientHandler_NoRedirectURIs(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	body := `{"client_name":"test"}`
	req := httptest.NewRequest("POST", "/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.RegisterClientHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRegisterClientHandler_InvalidJSON(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("POST", "/register", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.RegisterClientHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRegisterClientHandler_MethodNotAllowed(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("GET", "/register", nil)
	w := httptest.NewRecorder()
	h.RegisterClientHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ---------- Authorize GET (login form) ----------

func TestAuthorizeGet_RendersForm(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedClient(t, s)

	req := httptest.NewRequest("GET", "/authorize?client_id=test-client-1&redirect_uri=http://localhost:9876/callback&state=xyz&code_challenge=abc&code_challenge_method=S256&scope=mcp", nil)
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Sign In") {
		t.Error("form does not contain 'Sign In'")
	}
	if !strings.Contains(body, "test-client-1") {
		t.Error("form does not contain client_id")
	}
	if !strings.Contains(body, "abc") {
		t.Error("form does not contain code_challenge")
	}
}

func TestAuthorizeGet_MissingClientID(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("GET", "/authorize?redirect_uri=http://localhost/cb", nil)
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAuthorizeGet_InvalidClient(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("GET", "/authorize?client_id=unknown&redirect_uri=http://localhost/cb", nil)
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------- Authorize POST (login + code issue) ----------

func TestAuthorizePost_Success(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)
	seedClient(t, s)

	form := url.Values{}
	form.Set("email", "alice@example.com")
	form.Set("password", "password123")
	form.Set("client_id", "test-client-1")
	form.Set("redirect_uri", "http://localhost:9876/callback")
	form.Set("state", "mystate")
	form.Set("code_challenge", "test-challenge")
	form.Set("scope", "mcp")

	req := httptest.NewRequest("POST", "/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("no Location header")
	}
	if !strings.HasPrefix(loc, "http://localhost:9876/callback?") {
		t.Errorf("Location = %q, want prefix http://localhost:9876/callback?", loc)
	}
	if !strings.Contains(loc, "code=") {
		t.Error("Location missing code param")
	}
	if !strings.Contains(loc, "state=mystate") {
		t.Error("Location missing state param")
	}
}

func TestAuthorizePost_BadPassword(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)
	seedClient(t, s)

	form := url.Values{}
	form.Set("email", "alice@example.com")
	form.Set("password", "wrongpassword")
	form.Set("client_id", "test-client-1")
	form.Set("redirect_uri", "http://localhost:9876/callback")

	req := httptest.NewRequest("POST", "/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	// Should re-render the form with an error, status 200
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Invalid email or password") {
		t.Error("form does not show error message")
	}
}

func TestAuthorizePost_UnknownEmail(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedClient(t, s)

	form := url.Values{}
	form.Set("email", "nobody@example.com")
	form.Set("password", "pw")
	form.Set("client_id", "test-client-1")
	form.Set("redirect_uri", "http://localhost:9876/callback")

	req := httptest.NewRequest("POST", "/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid email or password") {
		t.Error("form does not show error message")
	}
}

// ---------- Token Endpoint: authorization_code ----------

func TestTokenAuthorizationCode_Success(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	// Create a code directly in the store
	code := &store.OAuthCode{
		Code:          "test-code-abc",
		UserID:        "test-user-1",
		ClientID:      "test-client-1",
		RedirectURI:   "http://localhost:9876/callback",
		CodeChallenge: "",
		Scope:         "mcp",
		ExpiresAt:     time.Now().Add(10 * time.Minute).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "test-code-abc")
	form.Set("client_id", "test-client-1")
	form.Set("redirect_uri", "http://localhost:9876/callback")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["access_token"] == nil || resp["access_token"] == "" {
		t.Error("missing access_token")
	}
	if resp["refresh_token"] == nil || resp["refresh_token"] == "" {
		t.Error("missing refresh_token")
	}
	if resp["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", resp["token_type"])
	}
	if resp["scope"] != "mcp" {
		t.Errorf("scope = %v, want mcp", resp["scope"])
	}

	// Code should be consumed (one-time use)
	_, err := s.GetOAuthCode("test-code-abc")
	if err == nil {
		t.Error("code was not consumed after token exchange")
	}
}

func TestTokenAuthorizationCode_WithPKCE(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := computePKCEChallenge(verifier)

	code := &store.OAuthCode{
		Code:          "pkce-code",
		UserID:        "test-user-1",
		ClientID:      "test-client-1",
		RedirectURI:   "http://localhost:9876/callback",
		CodeChallenge: challenge,
		Scope:         "mcp",
		ExpiresAt:     time.Now().Add(10 * time.Minute).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "pkce-code")
	form.Set("code_verifier", verifier)
	form.Set("client_id", "test-client-1")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestTokenAuthorizationCode_BadPKCE(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	code := &store.OAuthCode{
		Code:          "pkce-code-2",
		UserID:        "test-user-1",
		ClientID:      "test-client-1",
		CodeChallenge: "correct-challenge-hash",
		ExpiresAt:     time.Now().Add(10 * time.Minute).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "pkce-code-2")
	form.Set("code_verifier", "wrong-verifier")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PKCE") {
		t.Error("error message should mention PKCE")
	}
}

func TestTokenAuthorizationCode_MissingVerifierWhenChallengeSet(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	code := &store.OAuthCode{
		Code:          "pkce-code-3",
		UserID:        "test-user-1",
		ClientID:      "c1",
		CodeChallenge: "some-challenge",
		ExpiresAt:     time.Now().Add(10 * time.Minute).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "pkce-code-3")
	// No code_verifier

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestTokenAuthorizationCode_ExpiredCode(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	code := &store.OAuthCode{
		Code:      "expired-code",
		UserID:    "test-user-1",
		ClientID:  "c1",
		ExpiresAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "expired-code")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Error("error should mention expired")
	}
}

func TestTokenAuthorizationCode_InvalidCode(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "nonexistent")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestTokenAuthorizationCode_ClientIDMismatch(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	code := &store.OAuthCode{
		Code:      "code-x",
		UserID:    "test-user-1",
		ClientID:  "client-a",
		ExpiresAt: time.Now().Add(10 * time.Minute).UTC(),
	}
	s.CreateOAuthCode(code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", "code-x")
	form.Set("client_id", "client-b") // mismatch

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------- Token Endpoint: refresh_token ----------

func TestTokenRefresh_Success(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)
	sess := seedSession(t, s)

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", sess.RefreshToken)

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	newAT := resp["access_token"].(string)
	newRT := resp["refresh_token"].(string)
	if newAT == "" {
		t.Error("missing new access_token")
	}
	if newRT == "" {
		t.Error("missing new refresh_token")
	}
	// Old session should be deleted
	_, err := s.GetOAuthSession(sess.AccessToken)
	if err == nil {
		t.Error("old session still exists after refresh")
	}
	// New session should exist
	_, err = s.GetOAuthSession(newAT)
	if err != nil {
		t.Errorf("new session not found: %v", err)
	}
}

func TestTokenRefresh_InvalidToken(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", "nonexistent-refresh")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestToken_UnsupportedGrantType(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unsupported_grant_type") {
		t.Error("error should be unsupported_grant_type")
	}
}

func TestToken_MethodNotAllowed(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	req := httptest.NewRequest("GET", "/token", nil)
	w := httptest.NewRecorder()
	h.TokenHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// ---------- Middleware ----------

func TestMiddleware_ValidToken(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)
	seedClient(t, s)
	sess := seedSession(t, s)

	var capturedUserID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = UserIDFromContext(r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := h.Middleware(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if capturedUserID != "test-user-1" {
		t.Errorf("userID = %q, want test-user-1", capturedUserID)
	}
}

func TestMiddleware_NoToken(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	handler := h.Middleware(inner)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	wwwAuth := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(wwwAuth, "oauth-authorization-server") {
		t.Errorf("WWW-Authenticate = %q, should contain resource_metadata URL", wwwAuth)
	}
}

func TestMiddleware_InvalidToken(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	handler := h.Middleware(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestMiddleware_ExpiredToken(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	sess := &store.OAuthSession{
		AccessToken:  "expired-access-token",
		RefreshToken: "r1",
		UserID:       "test-user-1",
		ClientID:     "c1",
		Scope:        "mcp",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).UTC(),
	}
	s.CreateOAuthSession(sess)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	handler := h.Middleware(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer expired-access-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}

	// Session should be cleaned up
	_, err := s.GetOAuthSession("expired-access-token")
	if err == nil {
		t.Error("expired session was not cleaned up")
	}
}

func TestMiddleware_CaseInsensitiveBearer(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)
	seedClient(t, s)
	sess := seedSession(t, s)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := h.Middleware(inner)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer "+sess.AccessToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (case insensitive bearer)", w.Code)
	}
}

// ---------- PKCE ----------

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := computePKCEChallenge(verifier)

	if !verifyPKCE(verifier, challenge) {
		t.Error("verifyPKCE returned false for valid verifier/challenge pair")
	}
	if verifyPKCE("wrong-verifier", challenge) {
		t.Error("verifyPKCE returned true for wrong verifier")
	}
	if verifyPKCE(verifier, "wrong-challenge") {
		t.Error("verifyPKCE returned true for wrong challenge")
	}
}

// ---------- Full flow integration test ----------

func TestFullOAuthFlow(t *testing.T) {
	h, s, dir := testSetup(t)
	defer cleanup(s, dir)
	seedUser(t, s)

	// Set up the HTTP server with all routes
	mux := http.NewServeMux()
	h.Register(mux)

	// Protected endpoint behind middleware
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := UserIDFromContext(r)
		fmt.Fprintf(w, `{"user_id":%q}`, uid)
	})
	mux.Handle("/protected", h.Middleware(inner))

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Override issuer to match test server
	h.Issuer = ts.URL

	client := ts.Client()

	// 1. Discover metadata
	resp, err := client.Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()

	regEndpoint := meta["registration_endpoint"].(string)
	authEndpoint := meta["authorization_endpoint"].(string)
	tokenEndpoint := meta["token_endpoint"].(string)

	// 2. Dynamic client registration
	regBody := `{"redirect_uris":["http://127.0.0.1:9999/callback"],"client_name":"integration-test"}`
	resp, err = client.Post(regEndpoint, "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var regResp clientRegistrationResponse
	json.NewDecoder(resp.Body).Decode(&regResp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d", resp.StatusCode)
	}
	clientID := regResp.ClientID

	// 3. PKCE
	verifier := "my-test-code-verifier-for-integration-testing"
	challenge := computePKCEChallenge(verifier)

	// 4. Authorize GET (check form renders)
	authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&state=teststate&code_challenge=%s&code_challenge_method=S256&scope=mcp",
		authEndpoint, clientID, url.QueryEscape("http://127.0.0.1:9999/callback"), challenge)
	resp, err = client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize GET status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 5. Authorize POST (login) — prevent auto-redirect to capture the Location
	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	form := url.Values{}
	form.Set("email", "alice@example.com")
	form.Set("password", "password123")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://127.0.0.1:9999/callback")
	form.Set("state", "teststate")
	form.Set("code_challenge", challenge)
	form.Set("scope", "mcp")

	resp, err = noRedirectClient.PostForm(authEndpoint, form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize POST status = %d, want 302", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	locURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	authCode := locURL.Query().Get("code")
	if authCode == "" {
		t.Fatal("no code in redirect")
	}
	if locURL.Query().Get("state") != "teststate" {
		t.Error("state mismatch in redirect")
	}

	// 6. Token exchange
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", authCode)
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("client_id", clientID)
	tokenForm.Set("redirect_uri", "http://127.0.0.1:9999/callback")

	resp, err = client.PostForm(tokenEndpoint, tokenForm)
	if err != nil {
		t.Fatal(err)
	}
	var tokenResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&tokenResp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d", resp.StatusCode)
	}
	accessToken := tokenResp["access_token"].(string)
	refreshTok := tokenResp["refresh_token"].(string)

	// 7. Access protected resource
	protReq, _ := http.NewRequest("GET", ts.URL+"/protected", nil)
	protReq.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err = client.Do(protReq)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("protected status = %d; body: %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "test-user-1") {
		t.Errorf("protected body = %s, want test-user-1", string(body))
	}

	// 8. Access without token → 401
	protReq2, _ := http.NewRequest("GET", ts.URL+"/protected", nil)
	resp, err = client.Do(protReq2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", resp.StatusCode)
	}

	// 9. Refresh token
	refreshForm := url.Values{}
	refreshForm.Set("grant_type", "refresh_token")
	refreshForm.Set("refresh_token", refreshTok)
	resp, err = client.PostForm(tokenEndpoint, refreshForm)
	if err != nil {
		t.Fatal(err)
	}
	var refreshResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&refreshResp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d", resp.StatusCode)
	}
	newAccessToken := refreshResp["access_token"].(string)
	if newAccessToken == accessToken {
		t.Error("new access token should differ from old one")
	}

	// Old token should no longer work
	protReq3, _ := http.NewRequest("GET", ts.URL+"/protected", nil)
	protReq3.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err = client.Do(protReq3)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401", resp.StatusCode)
	}

	// New token should work
	protReq4, _ := http.NewRequest("GET", ts.URL+"/protected", nil)
	protReq4.Header.Set("Authorization", "Bearer "+newAccessToken)
	resp, err = client.Do(protReq4)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new token status = %d, want 200", resp.StatusCode)
	}
}

// ---------- UserIDFromContext ----------

func TestUserIDFromContext_NoValue(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if uid := UserIDFromContext(req); uid != "" {
		t.Errorf("expected empty string, got %q", uid)
	}
}
