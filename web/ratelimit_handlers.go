// Package web provides HTTP handlers for the admin interface.
//
// ADDING NEW WEB PAGES CHECKLIST:
// 1. Follow the same pattern as enforcer_handlers.go
// 2. Handler funcs use map[string]interface{} directly (NOT pageData struct)
// 3. Call h.templates.ExecuteTemplate directly
// 4. Get user from userFromContext(r)
// 5. Register routes in web.go Register() function
// 6. Add nav link in templates/_base.html
// 7. Create template file in templates/ directory
// 8. Test with minimal data first - verify page renders before adding business logic
// 9. Remove any debug logging added during troubleshooting

package web

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// RateLimitHandler handles rate limit admin UI operations
// Uses map[string]interface{} pattern (see enforcer_handlers.go for reference)
type RateLimitHandler struct {
	enforcer  *enforcer.Enforcer
	templates *template.Template
	store     *store.Store
}

func NewRateLimitHandler(e *enforcer.Enforcer, t *template.Template, s *store.Store) *RateLimitHandler {
	return &RateLimitHandler{enforcer: e, templates: t, store: s}
}

func (h *RateLimitHandler) requireEnforcer(w http.ResponseWriter, r *http.Request) bool {
	if h.enforcer == nil {
		http.Error(w, "Enforcer is not enabled", http.StatusServiceUnavailable)
		return false
	}
	return true
}

type BucketDisplay struct {
	UserID         string
	BackendID      string
	BucketType     string
	Capacity       int
	CurrentLevel   int
	RefillRate     int
	PercentageUsed int
}

type BackendConfigDisplay struct {
	BackendID    string
	RiskCapacity int
	RiskRefill   int
	ResCapacity  int
	ResRefill    int
}

func (h *RateLimitHandler) ListRateLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	user := userFromContext(r)

	buckets := h.enforcer.GetAllRateLimitStates()

	var displays []BucketDisplay
	for _, b := range buckets {
		pct := 0
		if b.Capacity > 0 {
			pct = ((b.Capacity - b.CurrentLevel) * 100) / b.Capacity
		}
		displays = append(displays, BucketDisplay{
			UserID:         b.UserID,
			BackendID:      b.BackendID,
			BucketType:     b.BucketType,
			Capacity:       b.Capacity,
			CurrentLevel:   b.CurrentLevel,
			RefillRate:     b.RefillRate,
			PercentageUsed: pct,
		})
	}

	configs := h.enforcer.GetRateLimitConfigs()

	data := map[string]interface{}{
		"User":    user,
		"Title":   "Rate Limits",
		"Data":    displays,
		"Configs": configs,
		"Error":   r.URL.Query().Get("error"),
		"Success": r.URL.Query().Get("success"),
	}

	if err := h.templates.ExecuteTemplate(w, "admin_ratelimits.html", data); err != nil {
		log.Printf("web: render ratelimits: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *RateLimitHandler) UpdateRateLimitConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	backendID := r.FormValue("backend_id")
	riskCapacity := parseInt(r.FormValue("risk_capacity"))
	riskRefill := parseInt(r.FormValue("risk_refill"))
	resourceCapacity := parseInt(r.FormValue("resource_capacity"))
	resourceRefill := parseInt(r.FormValue("resource_refill"))

	if backendID == "" {
		http.Redirect(w, r, "/web/admin/ratelimits?error=Backend+ID+required", http.StatusSeeOther)
		return
	}

	if riskCapacity <= 0 {
		riskCapacity = 100
	}
	if riskRefill <= 0 {
		riskRefill = 20
	}
	if resourceCapacity <= 0 {
		resourceCapacity = 200
	}
	if resourceRefill <= 0 {
		resourceRefill = 40
	}

	h.enforcer.SetRateLimitConfig(backendID, riskCapacity, riskRefill, resourceCapacity, resourceRefill)

	http.Redirect(w, r, "/web/admin/ratelimits?success=Rate+limit+config+updated+for+"+backendID, http.StatusSeeOther)
}

func (h *RateLimitHandler) ResetUserBuckets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	userID := r.FormValue("user_id")
	backendID := r.FormValue("backend_id")

	if userID == "" {
		http.Redirect(w, r, "/web/admin/ratelimits?error=User+ID+required", http.StatusSeeOther)
		return
	}

	if err := h.enforcer.ResetUserRateLimit(userID, backendID); err != nil {
		log.Printf("web: reset rate limits: %v", err)
		http.Redirect(w, r, "/web/admin/ratelimits?error=Failed+to+reset+rate+limits", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/admin/ratelimits?success=Rate+limits+reset+for+user", http.StatusSeeOther)
}

func (h *RateLimitHandler) GetRateLimitsAPI(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnforcer(w, r) {
		return
	}

	userID := r.URL.Query().Get("user_id")
	backendID := r.URL.Query().Get("backend_id")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	status := h.enforcer.GetRateLimitStatus(userID, backendID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
