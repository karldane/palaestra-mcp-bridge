package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// Handler provides all OAuth 2.1 endpoints and auth middleware.
// It is designed to be mounted on an existing ServeMux.
type Handler struct {
	Store    *store.Store
	Issuer   string        // e.g. "http://localhost:8080"
	CodeTTL  time.Duration // how long auth codes live (default 10m)
	TokenTTL time.Duration // how long access tokens live (default 1h)
}

// contextKey is an unexported type for context keys in this package.
type contextKey string

// UserIDKey is the context key for the authenticated user ID.
const UserIDKey contextKey = "user_id"

// DefaultCodeTTL is the default auth code lifetime.
const DefaultCodeTTL = 10 * time.Minute

// DefaultTokenTTL is the default access token lifetime.
const DefaultTokenTTL = 1 * time.Hour

// Register mounts all OAuth endpoints on the given ServeMux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/.well-known/oauth-authorization-server", h.MetadataHandler)
	mux.HandleFunc("/authorize", h.AuthorizeHandler)
	mux.HandleFunc("/token", h.TokenHandler)
	mux.HandleFunc("/register", h.RegisterClientHandler)
}

func (h *Handler) codeTTL() time.Duration {
	if h.CodeTTL > 0 {
		return h.CodeTTL
	}
	return DefaultCodeTTL
}

func (h *Handler) tokenTTL() time.Duration {
	if h.TokenTTL > 0 {
		return h.TokenTTL
	}
	return DefaultTokenTTL
}

// ---------- Middleware ----------

// Middleware returns an http.Handler that validates the Authorization: Bearer
// token and injects the user ID into the request context. If the token is
// missing or invalid, it returns 401 with a WWW-Authenticate header pointing
// to the OAuth resource metadata, per MCP spec.
// It supports both OAuth access tokens and API keys (prefixed with "mcp_").
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" {
			h.unauthorized(w)
			return
		}

		var userID string

		if strings.HasPrefix(token, "mcp_") {
			apiKey, err := h.Store.ValidateAPIKey(token)
			if err != nil || apiKey == nil {
				h.unauthorized(w)
				return
			}
			if apiKey.ExpiresAt != nil && time.Now().UTC().After(*apiKey.ExpiresAt) {
				h.unauthorized(w)
				return
			}
			h.Store.UpdateAPIKeyLastUsed(apiKey.ID)
			userID = apiKey.UserID
		} else {
			sess, err := h.Store.GetOAuthSession(token)
			if err != nil {
				h.unauthorized(w)
				return
			}
			if time.Now().UTC().After(sess.ExpiresAt) {
				h.Store.DeleteOAuthSession(token)
				h.unauthorized(w)
				return
			}
			userID = sess.UserID
		}

		ctx := r.Context()
		ctx = contextWithUserID(ctx, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-authorization-server"`, h.Issuer))
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"unauthorized"}`))
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return auth[len(prefix):]
}

// ---------- Metadata ----------

// MetadataHandler serves the OAuth 2.0 Authorization Server Metadata (RFC 8414).
// opencode discovers this at /.well-known/oauth-authorization-server.
func (h *Handler) MetadataHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("auth: metadata: %s %s", r.Method, r.URL.String())
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	meta := map[string]interface{}{
		"issuer":                                h.Issuer,
		"authorization_endpoint":                h.Issuer + "/authorize",
		"token_endpoint":                        h.Issuer + "/token",
		"registration_endpoint":                 h.Issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"mcp"},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// ---------- Dynamic Client Registration (RFC 7591) ----------

type clientRegistrationRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
}

type clientRegistrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// RegisterClientHandler handles POST /register for dynamic client registration.
func (h *Handler) RegisterClientHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("auth: register client: %s %s", r.Method, r.URL.String())
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req clientRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	if len(req.RedirectURIs) == 0 {
		http.Error(w, `{"error":"invalid_request","error_description":"redirect_uris required"}`, http.StatusBadRequest)
		return
	}

	urisJSON, _ := json.Marshal(req.RedirectURIs)

	client := &store.OAuthClient{
		ClientSecret: "", // public client — no secret
		RedirectURIs: string(urisJSON),
		ClientName:   req.ClientName,
	}
	if err := h.Store.CreateOAuthClient(client); err != nil {
		log.Printf("auth: register client: %v", err)
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}

	resp := clientRegistrationResponse{
		ClientID:                client.ClientID,
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		TokenEndpointAuthMethod: "none",
	}

	log.Printf("auth: registered client: id=%s name=%q uris=%v", resp.ClientID, resp.ClientName, resp.RedirectURIs)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Authorization Endpoint ----------

// AuthorizeHandler serves GET /authorize (shows login form) and
// POST /authorize (validates credentials, issues auth code, redirects).
func (h *Handler) AuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("auth: authorize: %s %s", r.Method, r.URL.String())
	switch r.Method {
	case http.MethodGet:
		h.authorizeGet(w, r)
	case http.MethodPost:
		h.authorizePost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) authorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	scope := q.Get("scope")

	log.Printf("auth: authorize GET: client_id=%q redirect_uri=%q", clientID, redirectURI)

	if clientID == "" || redirectURI == "" {
		log.Printf("auth: authorize GET: missing client_id or redirect_uri")
		http.Error(w, `{"error":"invalid_request","error_description":"client_id and redirect_uri required"}`, http.StatusBadRequest)
		return
	}

	// Verify client exists
	_, err := h.Store.GetOAuthClient(clientID)
	if err != nil {
		log.Printf("auth: authorize GET: client_id=%q not found in store: %v", clientID, err)
		http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
		return
	}

	// We only support S256
	if codeChallenge != "" && codeChallengeMethod != "S256" && codeChallengeMethod != "" {
		http.Error(w, `{"error":"invalid_request","error_description":"only S256 code_challenge_method supported"}`, http.StatusBadRequest)
		return
	}

	// Render the login form
	data := loginFormData{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: codeChallenge,
		Scope:         scope,
		Error:         "",
	}
	renderLoginForm(w, data)
}

func (h *Handler) authorizePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	codeChallenge := r.FormValue("code_challenge")
	scope := r.FormValue("scope")

	if clientID == "" || redirectURI == "" {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	// Authenticate user
	user, err := h.Store.GetUserByEmail(email)
	if err != nil || store.CheckPassword(user.Password, password) != nil {
		data := loginFormData{
			ClientID:      clientID,
			RedirectURI:   redirectURI,
			State:         state,
			CodeChallenge: codeChallenge,
			Scope:         scope,
			Error:         "Invalid email or password",
		}
		renderLoginForm(w, data)
		return
	}

	// Auto-upgrade plaintext password to bcrypt on successful login.
	if !store.IsBcrypt(user.Password) {
		if hash, hashErr := store.HashPassword(password); hashErr == nil {
			user.Password = hash
			h.Store.UpdateUser(user)
		}
	}

	// Issue auth code
	code := &store.OAuthCode{
		UserID:        user.ID,
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		Scope:         scope,
		ExpiresAt:     time.Now().Add(h.codeTTL()).UTC(),
	}
	if err := h.Store.CreateOAuthCode(code); err != nil {
		log.Printf("auth: create code: %v", err)
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}

	// Redirect back to client with the code.
	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}
	location := redirectURI + sep + "code=" + code.Code
	if state != "" {
		location += "&state=" + state
	}
	http.Redirect(w, r, location, http.StatusFound)
}

// ---------- Token Endpoint ----------

// TokenHandler handles POST /token for authorization_code and refresh_token grants.
func (h *Handler) TokenHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("auth: token: %s %s", r.Method, r.URL.String())
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		jsonError(w, "invalid_request", "could not parse form", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	switch grantType {
	case "authorization_code":
		h.tokenAuthorizationCode(w, r)
	case "refresh_token":
		h.tokenRefresh(w, r)
	default:
		jsonError(w, "unsupported_grant_type", "only authorization_code and refresh_token supported", http.StatusBadRequest)
	}
}

func (h *Handler) tokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	codeStr := r.FormValue("code")
	codeVerifier := r.FormValue("code_verifier")
	redirectURI := r.FormValue("redirect_uri")
	clientID := r.FormValue("client_id")

	log.Printf("auth: token exchange: client_id=%q code=%q redirect_uri=%q code_verifier_len=%d",
		clientID, codeStr, redirectURI, len(codeVerifier))

	if codeStr == "" {
		jsonError(w, "invalid_request", "code required", http.StatusBadRequest)
		return
	}

	code, err := h.Store.GetOAuthCode(codeStr)
	if err != nil {
		jsonError(w, "invalid_grant", "code not found", http.StatusBadRequest)
		return
	}

	// Validate expiry
	if time.Now().UTC().After(code.ExpiresAt) {
		h.Store.DeleteOAuthCode(codeStr)
		jsonError(w, "invalid_grant", "code expired", http.StatusBadRequest)
		return
	}

	// Validate client_id
	if clientID != "" && clientID != code.ClientID {
		jsonError(w, "invalid_grant", "client_id mismatch", http.StatusBadRequest)
		return
	}

	// Validate redirect_uri
	if redirectURI != "" && redirectURI != code.RedirectURI {
		jsonError(w, "invalid_grant", "redirect_uri mismatch", http.StatusBadRequest)
		return
	}

	// PKCE validation
	if code.CodeChallenge != "" {
		if codeVerifier == "" {
			jsonError(w, "invalid_grant", "code_verifier required", http.StatusBadRequest)
			return
		}
		if !verifyPKCE(codeVerifier, code.CodeChallenge) {
			jsonError(w, "invalid_grant", "PKCE verification failed", http.StatusBadRequest)
			return
		}
	}

	// Consume the code (one-time use)
	h.Store.DeleteOAuthCode(codeStr)

	// Issue tokens
	sess := &store.OAuthSession{
		UserID:    code.UserID,
		ClientID:  code.ClientID,
		Scope:     code.Scope,
		ExpiresAt: time.Now().Add(h.tokenTTL()).UTC(),
	}
	if err := h.Store.CreateOAuthSession(sess); err != nil {
		log.Printf("auth: create session: %v", err)
		jsonError(w, "server_error", "could not create session", http.StatusInternalServerError)
		return
	}

	writeTokenResponse(w, sess, h.tokenTTL())
}

func (h *Handler) tokenRefresh(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	if refreshToken == "" {
		jsonError(w, "invalid_request", "refresh_token required", http.StatusBadRequest)
		return
	}

	oldSess, err := h.Store.GetOAuthSessionByRefresh(refreshToken)
	if err != nil {
		jsonError(w, "invalid_grant", "refresh_token not found", http.StatusBadRequest)
		return
	}

	// Delete old session
	h.Store.DeleteOAuthSession(oldSess.AccessToken)

	// Issue new session
	newSess := &store.OAuthSession{
		UserID:    oldSess.UserID,
		ClientID:  oldSess.ClientID,
		Scope:     oldSess.Scope,
		ExpiresAt: time.Now().Add(h.tokenTTL()).UTC(),
	}
	if err := h.Store.CreateOAuthSession(newSess); err != nil {
		log.Printf("auth: create refresh session: %v", err)
		jsonError(w, "server_error", "could not create session", http.StatusInternalServerError)
		return
	}

	writeTokenResponse(w, newSess, h.tokenTTL())
}

// ---------- PKCE ----------

// verifyPKCE checks that SHA256(code_verifier) == code_challenge (S256 method).
func verifyPKCE(codeVerifier, codeChallenge string) bool {
	h := sha256.Sum256([]byte(codeVerifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return computed == codeChallenge
}

// ---------- Helpers ----------

func writeTokenResponse(w http.ResponseWriter, sess *store.OAuthSession, ttl time.Duration) {
	resp := map[string]interface{}{
		"access_token":  sess.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    int(ttl.Seconds()),
		"refresh_token": sess.RefreshToken,
		"scope":         sess.Scope,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}

func jsonError(w http.ResponseWriter, errCode, description string, status int) {
	resp := map[string]string{
		"error":             errCode,
		"error_description": description,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// ---------- Context helpers ----------

func contextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}

type userIDContext struct {
	parent interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(key interface{}) interface{}
	}
	userID string
}

func (c *userIDContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }
func (c *userIDContext) Done() <-chan struct{}       { return c.parent.Done() }
func (c *userIDContext) Err() error                  { return c.parent.Err() }
func (c *userIDContext) Value(key interface{}) interface{} {
	if key == UserIDKey {
		return c.userID
	}
	return c.parent.Value(key)
}

// UserIDFromContext extracts the authenticated user ID from the request context.
func UserIDFromContext(r *http.Request) string {
	v := r.Context().Value(UserIDKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ---------- Login form ----------

type loginFormData struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scope         string
	Error         string
}

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MCP Bridge — Sign In</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 400px; margin: 80px auto; padding: 0 20px; }
  h1 { font-size: 1.4em; }
  label { display: block; margin-top: 12px; font-weight: 600; font-size: 0.9em; }
  input[type=email], input[type=password] {
    width: 100%; padding: 8px; margin-top: 4px; box-sizing: border-box;
    border: 1px solid #ccc; border-radius: 4px; font-size: 1em;
  }
  button { margin-top: 16px; padding: 10px 20px; font-size: 1em; cursor: pointer;
    background: #0066cc; color: #fff; border: none; border-radius: 4px; width: 100%; }
  button:hover { background: #0052a3; }
  .error { color: #c00; margin-top: 12px; font-size: 0.9em; }
</style>
</head>
<body>
  <h1>MCP Bridge — Sign In</h1>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form method="POST" action="/authorize">
    <input type="hidden" name="client_id" value="{{.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
    <input type="hidden" name="state" value="{{.State}}">
    <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
    <input type="hidden" name="scope" value="{{.Scope}}">
    <label for="email">Email</label>
    <input type="email" id="email" name="email" required autofocus>
    <label for="password">Password</label>
    <input type="password" id="password" name="password" required>
    <button type="submit">Sign In</button>
  </form>
</body>
</html>`))

func renderLoginForm(w http.ResponseWriter, data loginFormData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginTmpl.Execute(w, data); err != nil {
		log.Printf("auth: render login form: %v", err)
	}
}
