// Package web provides the browser-based administration and user interface
// for mcp-bridge. It uses cookie-based sessions (separate from the OAuth 2.1
// bearer-token flow used by MCP clients like opencode).
package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
)

const (
	sessionCookieName = "mcp_session"
	sessionTTL        = 24 * time.Hour
)

// Handler holds the shared dependencies for all web routes.
type Handler struct {
	Store       *store.Store
	Templates   *template.Template
	PoolManager *poolmgr.PoolManager
	Enforcer    *enforcer.Enforcer

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
	// Create template with functions first, then parse
	tmpl := template.New("").Funcs(template.FuncMap{
		"js": func(s string) template.JS {
			return template.JS(html.EscapeString(s))
		},
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	})

	pattern := filepath.Join(templateDir, "*.html")
	tmpl, err := tmpl.ParseGlob(pattern)
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
	mux.Handle("/web/apikeys", h.requireAuth(http.HandlerFunc(h.APIKeysHandler)))
	mux.Handle("/web/apikeys/create", h.requireAuth(http.HandlerFunc(h.APIKeysCreateHandler)))
	mux.Handle("/web/apikeys/delete", h.requireAuth(http.HandlerFunc(h.APIKeysDeleteHandler)))

	// Admin only
	mux.Handle("/web/admin/users", h.requireAdmin(http.HandlerFunc(h.AdminUsersHandler)))
	mux.Handle("/web/admin/users/create", h.requireAdmin(http.HandlerFunc(h.AdminUsersCreateHandler)))
	mux.Handle("/web/admin/users/delete", h.requireAdmin(http.HandlerFunc(h.AdminUsersDeleteHandler)))
	mux.Handle("/web/admin/backends", h.requireAdmin(http.HandlerFunc(h.AdminBackendsHandler)))
	mux.Handle("/web/admin/backends/create", h.requireAdmin(http.HandlerFunc(h.AdminBackendsCreateHandler)))
	mux.Handle("/web/admin/backends/edit", h.requireAdmin(http.HandlerFunc(h.AdminBackendsEditHandler)))
	mux.Handle("/web/admin/backends/delete", h.requireAdmin(http.HandlerFunc(h.AdminBackendsDeleteHandler)))
	mux.Handle("/web/admin/backends/probe", h.requireAdmin(http.HandlerFunc(h.AdminBackendsProbeHandler)))
	mux.Handle("/web/admin/settings/global_hints", h.requireAdmin(http.HandlerFunc(h.AdminSettingsGlobalHintsHandler)))
	mux.Handle("/web/admin/oauth-clients", h.requireAdmin(http.HandlerFunc(h.AdminOAuthClientsHandler)))
	mux.Handle("/web/admin/oauth-clients/create", h.requireAdmin(http.HandlerFunc(h.AdminOAuthClientsCreateHandler)))
	mux.Handle("/web/admin/oauth-clients/delete", h.requireAdmin(http.HandlerFunc(h.AdminOAuthClientsDeleteHandler)))

	// Enforcer (admin only)
	if h.Enforcer != nil {
		enforcerHandler := NewEnforcerHandler(h.Enforcer, h.Templates)
		mux.Handle("/web/admin/enforcer/queue", h.requireAdmin(http.HandlerFunc(enforcerHandler.QueuePageHandler)))
		mux.Handle("/web/admin/enforcer/api/approvals", h.requireAdmin(http.HandlerFunc(enforcerHandler.ListPendingApprovals)))
		mux.Handle("/web/admin/enforcer/api/approve", h.requireAdmin(http.HandlerFunc(enforcerHandler.ApproveRequest)))
		mux.Handle("/web/admin/enforcer/api/deny", h.requireAdmin(http.HandlerFunc(enforcerHandler.DenyRequest)))
		mux.Handle("/web/admin/enforcer/api/kill-switch/enable", h.requireAdmin(http.HandlerFunc(enforcerHandler.EnableKillSwitch)))
		mux.Handle("/web/admin/enforcer/api/kill-switch/disable", h.requireAdmin(http.HandlerFunc(enforcerHandler.DisableKillSwitch)))
		mux.Handle("/web/admin/enforcer/events", h.requireAdmin(http.HandlerFunc(enforcerHandler.SSEHandler)))
	}
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
	Extra    map[string]interface{}
}

func (h *Handler) render(w http.ResponseWriter, tmplName string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// ---------- Helpers ----------

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

// isValidBackendID validates backend ID: alphanumeric, dashes, underscores, max 50 chars
func isValidBackendID(id string) bool {
	if len(id) > 50 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// isLocalhost returns true if the request is from localhost
func isLocalhost(r *http.Request) bool {
	host := r.Host
	return host == "localhost" || host == "localhost:8080" ||
		host == "127.0.0.1" || host == "127.0.0.1:8080" ||
		host == "[::1]" || host == "[::1]:8080"
}
