package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

type EnforcerHandler struct {
	enforcer  *enforcer.Enforcer
	templates *template.Template
	store     *store.Store
}

func NewEnforcerHandler(e *enforcer.Enforcer, t *template.Template, s *store.Store) *EnforcerHandler {
	return &EnforcerHandler{enforcer: e, templates: t, store: s}
}

func (h *EnforcerHandler) requireEnforcer(w http.ResponseWriter, r *http.Request) bool {
	if h.enforcer == nil {
		http.Error(w, "Enforcer is not enabled", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *EnforcerHandler) GetApprovalStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	approvalID := r.URL.Query().Get("id")
	if approvalID == "" {
		http.Error(w, "approval_id is required", http.StatusBadRequest)
		return
	}

	approval, err := h.enforcer.GetApprovalRequest(approvalID)
	if err != nil {
		http.Error(w, "Approval not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":            approval.ID,
		"status":        approval.Status,
		"tool_name":     approval.ToolName,
		"user_id":       approval.UserID,
		"user_email":    approval.UserEmail,
		"policy_id":     approval.PolicyID,
		"message":       approval.ViolationMsg,
		"requested_at":  approval.RequestedAt,
		"expires_at":    approval.ExpiresAt,
		"approved_at":   approval.ApprovedAt,
		"approved_by":   approval.ApprovedBy,
		"comments":      approval.Comments,
		"denial_reason": approval.DenialReason,
		"request_body":  approval.RequestBody,
	})
}

func (h *EnforcerHandler) ListPendingApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	approvals, err := h.enforcer.ListPendingApprovals()
	if err != nil {
		http.Error(w, "Failed to list approvals: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type ApprovalView struct {
		ID          string    `json:"id"`
		ToolName    string    `json:"tool_name"`
		UserID      string    `json:"user_id"`
		UserEmail   string    `json:"user_email"`
		ToolArgs    string    `json:"tool_args"`
		PolicyID    string    `json:"policy_id"`
		Message     string    `json:"message"`
		RequestedAt time.Time `json:"requested_at"`
		ExpiresAt   time.Time `json:"expires_at"`
	}

	views := make([]ApprovalView, len(approvals))
	for i, a := range approvals {
		views[i] = ApprovalView{
			ID:          a.ID,
			ToolName:    a.ToolName,
			UserID:      a.UserID,
			UserEmail:   a.UserEmail,
			ToolArgs:    a.ToolArgs,
			PolicyID:    a.PolicyID,
			Message:     a.ViolationMsg,
			RequestedAt: a.RequestedAt,
			ExpiresAt:   a.ExpiresAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"approvals": views,
	})
}

func (h *EnforcerHandler) QueuePageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	fmt.Printf("DEBUG QueuePageHandler: user=%v\n", user)

	data := map[string]interface{}{
		"Title":          "Approval Queue",
		"User":           user,
		"Approvals":      []map[string]interface{}{},
		"PendingCount":   0,
		"CompletedCount": 0,
		"DeniedCount":    0,
	}

	approvals, err := h.enforcer.ListAllApprovals()
	if err == nil {
		views := make([]map[string]interface{}, 0, len(approvals))
		pendingCount := 0
		completedCount := 0
		deniedCount := 0

		for _, a := range approvals {
			var prettyArgs string
			if argsJSON, err := json.MarshalIndent(json.RawMessage(a.ToolArgs), "", "  "); err == nil {
				prettyArgs = string(argsJSON)
			} else {
				prettyArgs = a.ToolArgs
			}

			view := map[string]interface{}{
				"ID":          a.ID,
				"Status":      a.Status,
				"StatusClass": getStatusClass(a.Status),
				"ToolName":    a.ToolName,
				"UserID":      a.UserID,
				"UserEmail":   a.UserEmail,
				"ToolArgs":    prettyArgs,
				"PolicyID":    a.PolicyID,
				"Message":     a.ViolationMsg,
				"RequestedAt": a.RequestedAt,
				"ExpiresAt":   a.ExpiresAt,
			}

			if a.ApprovedBy.Valid {
				view["ApprovedBy"] = a.ApprovedBy.String
			}
			if a.ApprovedAt.Valid {
				view["ApprovedAt"] = a.ApprovedAt.Time
			}
			if a.ResponseStatus > 0 {
				view["ResponseStatus"] = a.ResponseStatus
			}
			if a.ResponseBody != "" {
				view["ResponseBody"] = a.ResponseBody
			}
			if a.ErrorMsg != "" {
				view["ErrorMsg"] = a.ErrorMsg
			}
			if a.DenialReason != "" {
				view["DenialReason"] = a.DenialReason
			}
			if a.Comments != "" {
				view["Comments"] = a.Comments
			}

			switch a.Status {
			case "PENDING":
				pendingCount++
			case "COMPLETED", "EXECUTING":
				completedCount++
			case "DENIED", "EXPIRED", "FAILED":
				deniedCount++
			}

			views = append(views, view)
		}
		data["Approvals"] = views
		data["PendingCount"] = pendingCount
		data["CompletedCount"] = completedCount
		data["DeniedCount"] = deniedCount
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_queue.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *EnforcerHandler) DenyRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	var req struct {
		ApprovalID string `json:"approval_id"`
		Reason     string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.ApprovalID == "" {
		http.Error(w, "approval_id is required", http.StatusBadRequest)
		return
	}

	if err := h.enforcer.DenyRequest(req.ApprovalID, "admin", req.Reason); err != nil {
		http.Error(w, "Failed to deny: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Request denied",
	})
}

func (h *EnforcerHandler) EnableKillSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	var req struct {
		Scope  string `json:"scope"`
		Reason string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Scope == "" {
		req.Scope = "global"
	}

	if err := h.enforcer.EnableKillSwitch(req.Scope, "admin", req.Reason); err != nil {
		http.Error(w, "Failed to enable kill switch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Kill switch enabled",
		"scope":   req.Scope,
	})
}

func (h *EnforcerHandler) DisableKillSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	var req struct {
		Scope string `json:"scope"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Scope == "" {
		req.Scope = "global"
	}

	if err := h.enforcer.DisableKillSwitch(req.Scope); err != nil {
		http.Error(w, "Failed to disable kill switch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Kill switch disabled",
		"scope":   req.Scope,
	})
}

func (h *EnforcerHandler) SSEHandler(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnforcer(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	fmt.Fprintf(w, "data: %s\n\n", `{"type": "connected"}`)
	w.(http.Flusher).Flush()

	for {
		select {
		case <-ticker.C:
			approvals, err := h.enforcer.ListPendingApprovals()
			if err == nil && len(approvals) > 0 {
				data, _ := json.Marshal(map[string]interface{}{
					"type":        "pending_count",
					"count":       len(approvals),
					"approval_id": approvals[0].ID,
				})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
			} else {
				fmt.Fprintf(w, "data: %s\n\n", `{"type": "ping"}`)
				w.(http.Flusher).Flush()
			}

		case <-r.Context().Done():
			return
		}
	}
}

func getStatusClass(status string) string {
	switch status {
	case "PENDING":
		return "pending"
	case "EXECUTING":
		return "executing"
	case "COMPLETED":
		return "completed"
	case "FAILED":
		return "failed"
	case "DENIED":
		return "denied"
	case "EXPIRED":
		return "expired"
	default:
		return "pending"
	}
}

func (h *EnforcerHandler) ApproveRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.requireEnforcer(w, r) {
		return
	}

	var req struct {
		ApprovalID string `json:"approval_id"`
		Comments   string `json:"comments"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.ApprovalID == "" {
		http.Error(w, "approval_id is required", http.StatusBadRequest)
		return
	}

	// ExecuteApprovedRequest approves, executes the original tool call, and stores
	// the result — all in one step. This is the correct path; ApproveRequest (the
	// old call) only marked the DB record approved and never ran the tool.
	updatedReq, err := h.enforcer.ExecuteApprovedRequest(req.ApprovalID, "admin", req.Comments)
	if err != nil {
		http.Error(w, "Failed to approve and execute: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":         true,
		"message":         "Request approved and executed",
		"status":          updatedReq.Status,
		"response_status": updatedReq.ResponseStatus,
		"response_body":   updatedReq.ResponseBody,
		"error_msg":       updatedReq.ErrorMsg,
	})
}

// PoliciesPageHandler displays the list of policies
func (h *EnforcerHandler) PoliciesPageHandler(w http.ResponseWriter, r *http.Request) {
	if !h.requireEnforcer(w, r) {
		return
	}
	user := userFromContext(r)
	policies, err := h.enforcer.ListPolicies()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"User": user,
		"Data": policies,
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_policies.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PoliciesNewPageHandler displays the new policy form
func (h *EnforcerHandler) PoliciesNewPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	data := map[string]interface{}{
		"User": user,
	}
	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_policies_new.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PoliciesCreateHandler creates a new policy
func (h *EnforcerHandler) PoliciesCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	priority := 100
	if p := r.FormValue("priority"); p != "" {
		fmt.Sscanf(p, "%d", &priority)
	}

	policy := enforcer.PolicyRow{
		ID:          r.FormValue("id"),
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		Scope:       r.FormValue("scope"),
		Expression:  r.FormValue("expression"),
		Action:      r.FormValue("action"),
		Severity:    r.FormValue("severity"),
		Message:     r.FormValue("message"),
		Enabled:     r.FormValue("enabled") == "on",
		Priority:    priority,
	}

	if err := h.enforcer.AddPolicy(policy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/web/admin/enforcer/policies", http.StatusSeeOther)
}

// PoliciesDeleteHandler deletes a policy
func (h *EnforcerHandler) PoliciesDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	policyID := r.URL.Query().Get("id")
	if policyID == "" {
		http.Error(w, "policy id required", http.StatusBadRequest)
		return
	}

	if err := h.enforcer.DeletePolicy(policyID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/web/admin/enforcer/policies", http.StatusSeeOther)
}

// PoliciesEditPageHandler displays the edit policy form
func (h *EnforcerHandler) PoliciesEditPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	policyID := r.URL.Query().Get("id")
	if policyID == "" {
		http.Error(w, "policy id required", http.StatusBadRequest)
		return
	}

	policy, err := h.enforcer.GetPolicy(policyID)
	if err != nil {
		http.Error(w, "policy not found: "+err.Error(), http.StatusNotFound)
		return
	}

	data := map[string]interface{}{
		"User":   user,
		"Policy": policy,
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_policies_edit.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PoliciesUpdateHandler updates a policy
func (h *EnforcerHandler) PoliciesUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	priority := 100
	if p := r.FormValue("priority"); p != "" {
		fmt.Sscanf(p, "%d", &priority)
	}

	policy := enforcer.PolicyRow{
		ID:          r.FormValue("id"),
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		Scope:       r.FormValue("scope"),
		Expression:  r.FormValue("expression"),
		Action:      r.FormValue("action"),
		Severity:    r.FormValue("severity"),
		Message:     r.FormValue("message"),
		Enabled:     r.FormValue("enabled") == "on",
		Priority:    priority,
	}

	if err := h.enforcer.UpdatePolicy(policy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/web/admin/enforcer/policies", http.StatusSeeOther)
}

// ListPolicies returns all policies as JSON
func (h *EnforcerHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.enforcer.ListPolicies()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(policies)
}

// BackendProfileSummaryView is a view model for backend profile summaries.
type BackendProfileSummaryView struct {
	BackendID       string
	ToolCount       int
	HighRiskCount   int
	MediumRiskCount int
	LowRiskCount    int
}

// ToolProfilesPageHandler shows backends with tool profiles.
func (h *EnforcerHandler) ToolProfilesPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	summaries, err := h.store.ListBackendProfileSummaries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get backends list for display
	backends, err := h.store.ListBackends()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build map of backends that have tool profiles
	profileBackendSet := make(map[string]bool)
	for _, s := range summaries {
		profileBackendSet[s.BackendID] = true
	}

	// Get override count per backend
	enforcerStore := store.NewEnforcerStore(h.store.DB())
	overrides, _ := enforcerStore.ListOverrides()
	overrideCountByBackend := make(map[string]int)
	for _, o := range overrides {
		overrideCountByBackend[o.BackendID]++
	}

	var views []map[string]interface{}
	for _, b := range backends {
		if !b.Enabled || b.IsSystem {
			continue
		}
		hasProfiles := profileBackendSet[b.ID]
		summary := store.BackendProfileSummary{}
		for _, s := range summaries {
			if s.BackendID == b.ID {
				summary = s
				break
			}
		}

		view := map[string]interface{}{
			"BackendID":       b.ID,
			"HasProfiles":     hasProfiles,
			"ToolCount":       summary.ToolCount,
			"HighRiskCount":   summary.HighRiskCount,
			"MediumRiskCount": summary.MediumRiskCount,
			"LowRiskCount":    summary.LowRiskCount,
			"OverrideCount":   overrideCountByBackend[b.ID],
		}
		views = append(views, view)
	}

	data := map[string]interface{}{
		"User":     user,
		"Backends": views,
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_profiles.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// BackendToolProfilesHandler shows tool profiles for a specific backend.
func (h *EnforcerHandler) BackendToolProfilesHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	backendID := r.URL.Query().Get("backend_id")

	if backendID == "" {
		http.Redirect(w, r, "/web/admin/enforcer/profiles", http.StatusSeeOther)
		return
	}

	profiles, err := h.store.GetToolProfilesByBackend(backendID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	enforcerStore := store.NewEnforcerStore(h.store.DB())
	overrides, _ := enforcerStore.ListOverrides()

	overrideMap := make(map[string]*enforcer.EnforcerOverrideRow)
	for i := range overrides {
		if overrides[i].BackendID == backendID {
			overrideMap[overrides[i].ToolName] = &overrides[i]
		}
	}

	var views []map[string]interface{}
	for _, p := range profiles {
		override := overrideMap[p.ToolName]
		var effectiveRisk string
		if override != nil {
			effectiveRisk = override.RiskLevel
		} else {
			effectiveRisk = p.RiskLevel
		}

		view := map[string]interface{}{
			"ToolName":      p.ToolName,
			"RiskLevel":     p.RiskLevel,
			"ImpactScope":   p.ImpactScope,
			"ResourceCost":  p.ResourceCost,
			"RequiresHITL":  p.RequiresHITL,
			"PIIExposure":   p.PIIExposure,
			"Idempotent":    p.Idempotent,
			"HasOverride":   override != nil,
			"EffectiveRisk": effectiveRisk,
			"Override":      override,
			"ScannedAt":     p.ScannedAt,
		}
		views = append(views, view)
	}

	data := map[string]interface{}{
		"User":         user,
		"BackendID":    backendID,
		"Profiles":     views,
		"ProfileCount": len(views),
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_profiles_backend.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// OverridesPageHandler shows all tool overrides and allows creating/editing them.
func (h *EnforcerHandler) OverridesPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	enforcerStore := store.NewEnforcerStore(h.store.DB())
	overrides, err := enforcerStore.ListOverrides()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"User":      user,
		"Overrides": overrides,
		"Prefill": map[string]string{
			"tool_name":  r.URL.Query().Get("tool_name"),
			"backend_id": r.URL.Query().Get("backend_id"),
		},
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_overrides.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// OverrideCreateHandler creates or updates a tool override.
func (h *EnforcerHandler) OverrideCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hitl := r.FormValue("requires_hitl") == "on"
	pii := r.FormValue("pii_exposure") == "on"

	override := enforcer.EnforcerOverrideRow{
		ID:           r.FormValue("id"),
		ToolName:     r.FormValue("tool_name"),
		BackendID:    r.FormValue("backend_id"),
		RiskLevel:    r.FormValue("risk_level"),
		ImpactScope:  r.FormValue("impact_scope"),
		ResourceCost: 5,
		RequiresHITL: hitl,
		PIIExposure:  pii,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if override.ID == "" {
		override.ID = fmt.Sprintf("override-%s-%s", override.BackendID, override.ToolName)
	}

	enforcerStore := store.NewEnforcerStore(h.store.DB())
	if err := enforcerStore.UpsertOverride(override); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if h.enforcer != nil {
		profile := enforcer.SafetyProfile{
			ToolName:     override.ToolName,
			BackendID:    override.BackendID,
			Risk:         enforcer.RiskLevel(override.RiskLevel),
			Impact:       enforcer.ImpactScope(override.ImpactScope),
			Cost:         override.ResourceCost,
			RequiresHITL: override.RequiresHITL,
			PIIExposure:  override.PIIExposure,
			Source:       "override",
		}
		h.enforcer.RegisterOverride(override.ToolName, override.BackendID, profile)
	}

	http.Redirect(w, r, "/web/admin/enforcer/profiles", http.StatusSeeOther)
}

// OverrideDeleteHandler deletes a tool override.
func (h *EnforcerHandler) OverrideDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	toolName := r.URL.Query().Get("tool_name")
	backendID := r.URL.Query().Get("backend_id")
	if toolName == "" || backendID == "" {
		http.Error(w, "tool_name and backend_id are required", http.StatusBadRequest)
		return
	}

	enforcerStore := store.NewEnforcerStore(h.store.DB())
	if err := enforcerStore.DeleteOverride(toolName, backendID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if h.enforcer != nil {
		h.enforcer.RemoveOverride(toolName, backendID)
	}

	http.Redirect(w, r, "/web/admin/enforcer/profiles", http.StatusSeeOther)
}
