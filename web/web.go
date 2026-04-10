// Package web provides the browser-based administration and user interface
// for mcp-bridge. It uses cookie-based sessions (separate from the OAuth 2.1
// bearer-token flow used by MCP clients like opencode).
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
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
		"rawjson": func(s string) template.JS {
			return template.JS(s)
		},
		"join": func(sep string, elems []string) string {
			return strings.Join(elems, sep)
		},
		"formatBytes": func(bytes uint64) string {
			if bytes == 0 {
				return "0 B"
			}
			mb := float64(bytes) / 1024 / 1024
			if mb > 1024 {
				return fmt.Sprintf("%.1f GB", mb/1024)
			}
			return fmt.Sprintf("%.0f MB", mb)
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
//
// HOW TO ADD A NEW WEB ROUTE:
//  1. Create the handler function in the appropriate *_handlers.go file
//     (e.g., admin.go for admin routes, auth.go for auth routes)
//  2. Add the route here in Register(), following the pattern:
//     mux.Handle("/path", h.requireAuth(http.HandlerFunc(h.YourHandler)))
//     - use h.requireAdmin() for admin-only routes
//     - use h.requireAuth() for authenticated user routes
//     - use mux.HandleFunc() for public routes (no auth)
//  3. If the route needs a separate POST action (e.g., /edit, /create),
//     create a separate handler function and register it
//  4. RESTART THE SERVER - changes require a server restart to take effect
//
// Route pattern: /web/admin/{resource} for GET, /web/admin/{resource}/action for POST actions
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
	mux.Handle("/web/admin/users/edit", h.requireAdmin(http.HandlerFunc(h.AdminUsersEditHandler)))
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

	// Enforcer admin routes (always registered; handlers guard against nil enforcer)
	enforcerHandler := NewEnforcerHandler(h.Enforcer, h.Templates, h.Store)
	mux.Handle("/web/admin/enforcer/queue", h.requireAdmin(http.HandlerFunc(enforcerHandler.QueuePageHandler)))
	mux.Handle("/web/admin/enforcer/policies", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesPageHandler)))
	mux.Handle("/web/admin/enforcer/policies/new", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesNewPageHandler)))
	mux.Handle("/web/admin/enforcer/policies/create", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesCreateHandler)))
	mux.Handle("/web/admin/enforcer/policies/edit", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesEditPageHandler)))
	mux.Handle("/web/admin/enforcer/policies/update", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesUpdateHandler)))
	mux.Handle("/web/admin/enforcer/policies/delete", h.requireAdmin(http.HandlerFunc(enforcerHandler.PoliciesDeleteHandler)))
	mux.Handle("/web/admin/enforcer/api/policies", h.requireAdmin(http.HandlerFunc(enforcerHandler.ListPolicies)))
	mux.Handle("/web/admin/enforcer/api/approvals", h.requireAdmin(http.HandlerFunc(enforcerHandler.ListPendingApprovals)))
	mux.Handle("/web/admin/enforcer/api/approval-status", h.requireAdmin(http.HandlerFunc(enforcerHandler.GetApprovalStatus)))
	mux.Handle("/web/admin/enforcer/api/approve", h.requireAdmin(http.HandlerFunc(enforcerHandler.ApproveRequest)))
	mux.Handle("/web/admin/enforcer/api/deny", h.requireAdmin(http.HandlerFunc(enforcerHandler.DenyRequest)))
	mux.Handle("/web/admin/enforcer/kill-switch/enable", h.requireAdmin(http.HandlerFunc(enforcerHandler.EnableKillSwitch)))
	mux.Handle("/web/admin/enforcer/kill-switch/disable", h.requireAdmin(http.HandlerFunc(enforcerHandler.DisableKillSwitch)))
	mux.Handle("/web/admin/enforcer/events", h.requireAdmin(http.HandlerFunc(enforcerHandler.SSEHandler)))
	mux.Handle("/web/admin/enforcer/profiles", h.requireAdmin(http.HandlerFunc(enforcerHandler.ToolProfilesPageHandler)))
	mux.Handle("/web/admin/enforcer/profiles/backend", h.requireAdmin(http.HandlerFunc(enforcerHandler.BackendToolProfilesHandler)))
	mux.Handle("/web/admin/enforcer/profiles/overrides", h.requireAdmin(http.HandlerFunc(enforcerHandler.OverridesPageHandler)))
	mux.Handle("/web/admin/enforcer/profiles/override/create", h.requireAdmin(http.HandlerFunc(enforcerHandler.OverrideCreateHandler)))
	mux.Handle("/web/admin/enforcer/profiles/override/delete", h.requireAdmin(http.HandlerFunc(enforcerHandler.OverrideDeleteHandler)))

	// Enforcer user routes (any authenticated user)
	mux.Handle("/web/user/enforcer/queue", h.requireAuth(http.HandlerFunc(enforcerHandler.UserQueuePageHandler)))
	mux.Handle("/web/user/enforcer/policies", h.requireAuth(http.HandlerFunc(enforcerHandler.UserPoliciesPageHandler)))
	mux.Handle("/web/user/enforcer/overrides", h.requireAuth(http.HandlerFunc(enforcerHandler.UserOverridesPageHandler)))
	mux.Handle("/web/user/enforcer/overrides/create", h.requireAuth(http.HandlerFunc(enforcerHandler.UserOverrideCreateHandler)))
	mux.Handle("/web/user/enforcer/overrides/delete", h.requireAuth(http.HandlerFunc(enforcerHandler.UserOverrideDeleteHandler)))
	mux.Handle("/web/user/enforcer/api/approve", h.requireAuth(http.HandlerFunc(enforcerHandler.UserApproveRequest)))
	mux.Handle("/web/user/enforcer/api/deny", h.requireAuth(http.HandlerFunc(enforcerHandler.UserDenyRequest)))
	mux.Handle("/web/user/enforcer/events", h.requireAuth(http.HandlerFunc(enforcerHandler.UserSSEHandler)))

	// Admin cross-queue view (read-only summary of user-tier queues)
	mux.Handle("/web/admin/enforcer/user-queues", h.requireAdmin(http.HandlerFunc(enforcerHandler.UserQueuesPageHandler)))

	// Rate limit admin routes
	rateLimitHandler := NewRateLimitHandler(h.Enforcer, h.Templates, h.Store)
	mux.Handle("/web/admin/ratelimits", h.requireAdmin(http.HandlerFunc(rateLimitHandler.ListRateLimits)))
	mux.Handle("/web/admin/ratelimits/config", h.requireAdmin(http.HandlerFunc(rateLimitHandler.UpdateRateLimitConfig)))
	mux.Handle("/web/admin/ratelimits/reset", h.requireAdmin(http.HandlerFunc(rateLimitHandler.ResetUserBuckets)))
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

// WithAdminUser returns an HTTP handler that injects the admin user into context
// for the given handler. This is used for testing/insecure mode.
func (h *Handler) WithAdminUser(admin *store.User, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userContextKey, admin)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
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
		log.Printf("template error: %v", err)
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
