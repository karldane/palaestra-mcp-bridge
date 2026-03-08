// Package web provides the browser-based administration and user interface
// for mcp-bridge. It uses cookie-based sessions (separate from the OAuth 2.1
// bearer-token flow used by MCP clients like opencode).
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/store"
)

const (
	sessionCookieName = "mcp_session"
	sessionTTL        = 24 * time.Hour
)

// Handler holds the shared dependencies for all web routes.
type Handler struct {
	Store     *store.Store
	Templates *template.Template

	// OnBackendChange is called after a backend is created, edited, or
	// deleted. The callback receives the backend ID so the caller can
	// refresh routing tables and tear down stale pools. It may be nil.
	OnBackendChange func(backendID string)

	// OnProbeBackend is called to probe/test a backend. The callback
	// receives the backend ID, looks up the command, spawns a temporary
	// process, attempts the MCP handshake, and returns JSON-encoded result
	// bytes. It may be nil (probe endpoint returns 501).
	OnProbeBackend func(backendID string) ([]byte, error)
}

// NewHandler creates a Handler by parsing templates from the given directory.
func NewHandler(st *store.Store, templateDir string) (*Handler, error) {
	pattern := filepath.Join(templateDir, "*.html")
	tmpl, err := template.ParseGlob(pattern)
	if err != nil {
		return nil, fmt.Errorf("parse templates %s: %w", pattern, err)
	}
	return &Handler{Store: st, Templates: tmpl}, nil
}

// Register mounts all web routes onto the given ServeMux.
func (h *Handler) Register(mux *http.ServeMux) {
	// Public (no session required)
	mux.HandleFunc("/web/login", h.LoginHandler)
	mux.HandleFunc("/web/logout", h.LogoutHandler)

	// Authenticated (any role)
	mux.Handle("/web/", h.requireAuth(http.HandlerFunc(h.DashboardHandler)))
	mux.Handle("/web/tokens", h.requireAuth(http.HandlerFunc(h.TokensHandler)))
	mux.Handle("/web/tokens/save", h.requireAuth(http.HandlerFunc(h.TokensSaveHandler)))
	mux.Handle("/web/tokens/delete", h.requireAuth(http.HandlerFunc(h.TokensDeleteHandler)))
	mux.Handle("/web/password", h.requireAuth(http.HandlerFunc(h.PasswordHandler)))

	// Admin only
	mux.Handle("/web/admin/users", h.requireAdmin(http.HandlerFunc(h.AdminUsersHandler)))
	mux.Handle("/web/admin/users/create", h.requireAdmin(http.HandlerFunc(h.AdminUsersCreateHandler)))
	mux.Handle("/web/admin/users/delete", h.requireAdmin(http.HandlerFunc(h.AdminUsersDeleteHandler)))
	mux.Handle("/web/admin/backends", h.requireAdmin(http.HandlerFunc(h.AdminBackendsHandler)))
	mux.Handle("/web/admin/backends/create", h.requireAdmin(http.HandlerFunc(h.AdminBackendsCreateHandler)))
	mux.Handle("/web/admin/backends/edit", h.requireAdmin(http.HandlerFunc(h.AdminBackendsEditHandler)))
	mux.Handle("/web/admin/backends/delete", h.requireAdmin(http.HandlerFunc(h.AdminBackendsDeleteHandler)))
	mux.Handle("/web/admin/backends/probe", h.requireAdmin(http.HandlerFunc(h.AdminBackendsProbeHandler)))
}

// ---------- Session / Auth middleware ----------

type contextKey string

const userContextKey contextKey = "web_user"

func (h *Handler) getSessionUser(r *http.Request) *store.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	sess, err := h.Store.GetWebSession(cookie.Value)
	if err != nil {
		return nil
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		h.Store.DeleteWebSession(sess.Token)
		return nil
	}
	user, err := h.Store.GetUser(sess.UserID)
	if err != nil {
		return nil
	}
	return user
}

func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := h.getSessionUser(r)
		if user == nil {
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}
		// Store user in request context
		ctx := r.Context()
		ctx = contextWithUser(ctx, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r)
		if user == nil || user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// ---------- Context helpers ----------

func contextWithUser(ctx interface {
	Value(key interface{}) interface{}
}, user *store.User) interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(key interface{}) interface{}
} {
	return &userContext{parent: ctx.(interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(key interface{}) interface{}
	}), user: user}
}

type userContext struct {
	parent interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(key interface{}) interface{}
	}
	user *store.User
}

func (c *userContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }
func (c *userContext) Done() <-chan struct{}       { return c.parent.Done() }
func (c *userContext) Err() error                  { return c.parent.Err() }
func (c *userContext) Value(key interface{}) interface{} {
	if key == userContextKey {
		return c.user
	}
	return c.parent.Value(key)
}

func userFromContext(r *http.Request) *store.User {
	if v := r.Context().Value(userContextKey); v != nil {
		if u, ok := v.(*store.User); ok {
			return u
		}
	}
	return nil
}

// ---------- Template rendering ----------

type pageData struct {
	User     *store.User
	Title    string
	Error    string
	Success  string
	Data     interface{}
	Backends []*store.Backend
}

func (h *Handler) render(w http.ResponseWriter, tmplName string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, tmplName, data); err != nil {
		log.Printf("web: render %s: %v", tmplName, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// ---------- Login / Logout ----------

func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.render(w, "login.html", pageData{Title: "Sign In"})
	case http.MethodPost:
		h.loginPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) loginPost(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	user, err := h.Store.GetUserByEmail(email)
	if err != nil || store.CheckPassword(user.Password, password) != nil {
		h.render(w, "login.html", pageData{
			Title: "Sign In",
			Error: "Invalid email or password",
		})
		return
	}

	// Auto-upgrade plaintext password to bcrypt on successful login.
	if !store.IsBcrypt(user.Password) {
		if hash, hashErr := store.HashPassword(password); hashErr == nil {
			user.Password = hash
			h.Store.UpdateUser(user)
		}
	}

	// Create web session
	sess := &store.WebSession{
		UserID:    user.ID,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	}
	if err := h.Store.CreateWebSession(sess); err != nil {
		log.Printf("web: create session: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/web",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	http.Redirect(w, r, "/web/", http.StatusSeeOther)
}

func (h *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.Store.DeleteWebSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/web",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

// ---------- Dashboard ----------

func (h *Handler) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	backends, _ := h.Store.ListBackends()
	tokens, _ := h.Store.GetAllUserTokens(user.ID)

	// Build a map of backendID -> list of configured env keys
	type backendStatus struct {
		Backend        *store.Backend
		ConfiguredKeys []string
	}
	var statuses []backendStatus
	for _, b := range backends {
		if !b.Enabled {
			continue
		}
		var keys []string
		for _, t := range tokens {
			if t.BackendID == b.ID {
				keys = append(keys, t.EnvKey)
			}
		}
		statuses = append(statuses, backendStatus{Backend: b, ConfiguredKeys: keys})
	}

	h.render(w, "dashboard.html", pageData{
		User:  user,
		Title: "Dashboard",
		Data:  statuses,
	})
}

// ---------- User: Manage Tokens ----------

func (h *Handler) TokensHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	backendID := r.URL.Query().Get("backend")

	backends, _ := h.Store.ListBackends()

	// If no backend selected, show the first enabled one
	if backendID == "" {
		for _, b := range backends {
			if b.Enabled {
				backendID = b.ID
				break
			}
		}
	}

	var backend *store.Backend
	for _, b := range backends {
		if b.ID == backendID {
			backend = b
			break
		}
	}

	var tokens []*store.UserToken
	if backendID != "" {
		tokens, _ = h.Store.GetUserTokens(user.ID, backendID)
	}

	type tokensData struct {
		Backends        []*store.Backend
		SelectedID      string
		SelectedBackend *store.Backend
		Tokens          []*store.UserToken
	}

	h.render(w, "tokens.html", pageData{
		User:    user,
		Title:   "My Tokens",
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Data: tokensData{
			Backends:        backends,
			SelectedID:      backendID,
			SelectedBackend: backend,
			Tokens:          tokens,
		},
	})
}

func (h *Handler) TokensSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	backendID := r.FormValue("backend_id")
	envKey := strings.TrimSpace(r.FormValue("env_key"))
	value := r.FormValue("value")

	if backendID == "" || envKey == "" || value == "" {
		http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=All+fields+required", http.StatusSeeOther)
		return
	}

	token := &store.UserToken{
		UserID:    user.ID,
		BackendID: backendID,
		EnvKey:    envKey,
		Value:     value,
	}
	if err := h.Store.SetUserToken(token); err != nil {
		log.Printf("web: set token: %v", err)
		http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=Failed+to+save+token", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+saved", http.StatusSeeOther)
}

func (h *Handler) TokensDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	backendID := r.FormValue("backend_id")
	envKey := r.FormValue("env_key")

	if err := h.Store.DeleteUserToken(user.ID, backendID, envKey); err != nil {
		log.Printf("web: delete token: %v", err)
	}

	http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+deleted", http.StatusSeeOther)
}

// ---------- User: Change Password ----------

func (h *Handler) PasswordHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	switch r.Method {
	case http.MethodGet:
		h.render(w, "password.html", pageData{User: user, Title: "Change Password"})
	case http.MethodPost:
		current := r.FormValue("current_password")
		newPw := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		if store.CheckPassword(user.Password, current) != nil {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Current password is incorrect"})
			return
		}
		if newPw == "" {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "New password cannot be empty"})
			return
		}
		if newPw != confirm {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "New passwords do not match"})
			return
		}

		// Hash the new password (UpdateUser will auto-hash if plaintext,
		// but we set it explicitly for clarity).
		user.Password = newPw
		if err := h.Store.UpdateUser(user); err != nil {
			log.Printf("web: update password: %v", err)
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Failed to update password"})
			return
		}
		h.render(w, "password.html", pageData{User: user, Title: "Change Password", Success: "Password updated"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------- Admin: Users ----------

func (h *Handler) AdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	users, err := h.Store.ListUsers()
	if err != nil {
		log.Printf("web: list users: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_users.html", pageData{
		User:    user,
		Title:   "Manage Users",
		Data:    users,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
	})
}

func (h *Handler) AdminUsersCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	role := r.FormValue("role")

	if email == "" || password == "" {
		http.Redirect(w, r, "/web/admin/users?error=Email+and+password+required", http.StatusSeeOther)
		return
	}
	if role != "admin" && role != "user" {
		role = "user"
	}

	u := &store.User{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     role,
	}
	if err := h.Store.CreateUser(u); err != nil {
		log.Printf("web: create user: %v", err)
		http.Redirect(w, r, "/web/admin/users?error=Failed+to+create+user+(email+may+already+exist)", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/users?success=User+created", http.StatusSeeOther)
}

func (h *Handler) AdminUsersDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	currentUser := userFromContext(r)
	userID := r.FormValue("user_id")

	if userID == currentUser.ID {
		http.Redirect(w, r, "/web/admin/users?error=Cannot+delete+yourself", http.StatusSeeOther)
		return
	}

	if err := h.Store.DeleteUser(userID); err != nil {
		log.Printf("web: delete user: %v", err)
		http.Redirect(w, r, "/web/admin/users?error=Failed+to+delete+user", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/users?success=User+deleted", http.StatusSeeOther)
}

// ---------- Admin: Backends ----------

func (h *Handler) AdminBackendsHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	backends, err := h.Store.ListBackends()
	if err != nil {
		log.Printf("web: list backends: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_backends.html", pageData{
		User:    user,
		Title:   "Manage Backends",
		Data:    backends,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
	})
}

func (h *Handler) AdminBackendsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimSpace(r.FormValue("id"))
	command := strings.TrimSpace(r.FormValue("command"))
	poolSizeStr := r.FormValue("pool_size")
	toolPrefix := strings.TrimSpace(r.FormValue("tool_prefix"))
	env := strings.TrimSpace(r.FormValue("env"))
	enabled := r.FormValue("enabled") == "on"

	if id == "" || command == "" {
		http.Redirect(w, r, "/web/admin/backends?error=ID+and+command+required", http.StatusSeeOther)
		return
	}

	poolSize := 1
	if n := parseInt(poolSizeStr); n > 0 {
		poolSize = n
	}
	if env == "" {
		env = "{}"
	}

	b := &store.Backend{
		ID:         id,
		Command:    command,
		PoolSize:   poolSize,
		ToolPrefix: toolPrefix,
		Env:        env,
		Enabled:    enabled,
	}
	if err := h.Store.CreateBackend(b); err != nil {
		log.Printf("web: create backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+create+backend+(ID+may+already+exist)", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+created", http.StatusSeeOther)
}

func (h *Handler) AdminBackendsEditHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.FormValue("id")
	command := strings.TrimSpace(r.FormValue("command"))
	poolSizeStr := r.FormValue("pool_size")
	toolPrefix := strings.TrimSpace(r.FormValue("tool_prefix"))
	env := strings.TrimSpace(r.FormValue("env"))
	enabled := r.FormValue("enabled") == "on"

	poolSize := 1
	if n := parseInt(poolSizeStr); n > 0 {
		poolSize = n
	}
	if env == "" {
		env = "{}"
	}

	b := &store.Backend{
		ID:         id,
		Command:    command,
		PoolSize:   poolSize,
		ToolPrefix: toolPrefix,
		Env:        env,
		Enabled:    enabled,
	}
	if err := h.Store.UpdateBackend(b); err != nil {
		log.Printf("web: update backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+update+backend", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+updated", http.StatusSeeOther)
}

func (h *Handler) AdminBackendsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.FormValue("id")
	if err := h.Store.DeleteBackend(id); err != nil {
		log.Printf("web: delete backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+delete+backend", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+deleted", http.StatusSeeOther)
}

// ---------- Admin: Probe Backend ----------

func (h *Handler) AdminBackendsProbeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.OnProbeBackend == nil {
		http.Error(w, "Probe not configured", http.StatusNotImplemented)
		return
	}

	backendID := r.FormValue("id")
	if backendID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Missing backend id",
		})
		return
	}

	// Verify the backend exists.
	if _, err := h.Store.GetBackend(backendID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Backend not found: " + backendID,
		})
		return
	}

	result, err := h.OnProbeBackend(backendID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

// ---------- Helpers ----------

// notifyBackendChange invokes the OnBackendChange callback if set.
func (h *Handler) notifyBackendChange(backendID string) {
	if h.OnBackendChange != nil {
		h.OnBackendChange(backendID)
	}
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return 0
		}
	}
	return n
}
