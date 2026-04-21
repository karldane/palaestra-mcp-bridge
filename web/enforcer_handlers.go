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

	approvals, err := h.enforcer.ListAdminPendingApprovals()
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

	data := map[string]interface{}{
		"Title":          "Approval Queue",
		"User":           user,
		"Approvals":      []map[string]interface{}{},
		"PendingCount":   0,
		"CompletedCount": 0,
		"DeniedCount":    0,
	}

	approvals, err := h.enforcer.ListAdminPendingApprovals()
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
				"ID":            a.ID,
				"Status":        a.Status,
				"StatusClass":   getStatusClass(a.Status),
				"ToolName":      a.ToolName,
				"UserID":        a.UserID,
				"UserEmail":     a.UserEmail,
				"ToolArgs":      prettyArgs,
				"PolicyID":      a.PolicyID,
				"Message":       a.ViolationMsg,
				"RequestedAt":   a.RequestedAt,
				"ExpiresAt":     a.ExpiresAt,
				"QueueType":     a.QueueType,
				"Justification": a.Justification,
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

// UserPoliciesPageHandler displays a read-only view of policies for regular users
func (h *EnforcerHandler) UserPoliciesPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	data := map[string]interface{}{
		"User": user,
		"Data": []interface{}{},
	}

	if h.enforcer != nil {
		policies, err := h.enforcer.ListPolicies()
		if err == nil {
			data["Data"] = policies
		}
	}

	if err := h.templates.ExecuteTemplate(w, "user_enforcer_policies.html", data); err != nil {
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
		Locked:      r.FormValue("locked") == "on",
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
		Locked:      r.FormValue("locked") == "on",
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

// UserQueuePageHandler displays the user's approval queue
func (h *EnforcerHandler) UserQueuePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	data := map[string]interface{}{
		"Title":          "My Approval Requests",
		"User":           user,
		"Approvals":      []map[string]interface{}{},
		"AllApprovals":   []map[string]interface{}{},
		"PendingCount":   0,
		"CompletedCount": 0,
		"DeniedCount":    0,
	}

	// Pending tab: only PENDING records (used for approve/cancel actions)
	approvals, err := h.enforcer.ListUserPendingApprovals()
	if err == nil {
		views := make([]map[string]interface{}, 0, len(approvals))
		pendingCount := 0

		for _, a := range approvals {
			var prettyArgs string
			if argsJSON, err := json.MarshalIndent(json.RawMessage(a.ToolArgs), "", "  "); err == nil {
				prettyArgs = string(argsJSON)
			} else {
				prettyArgs = a.ToolArgs
			}

			view := map[string]interface{}{
				"ID":            a.ID,
				"Status":        a.Status,
				"StatusClass":   getStatusClass(a.Status),
				"ToolName":      a.ToolName,
				"UserID":        a.UserID,
				"UserEmail":     a.UserEmail,
				"ToolArgs":      prettyArgs,
				"PolicyID":      a.PolicyID,
				"Message":       a.ViolationMsg,
				"RequestedAt":   a.RequestedAt,
				"ExpiresAt":     a.ExpiresAt,
				"Justification": a.Justification,
			}
			if a.ApprovedBy.Valid {
				view["ApprovedBy"] = a.ApprovedBy.String
			}
			pendingCount++
			views = append(views, view)
		}
		data["Approvals"] = views
		data["PendingCount"] = pendingCount
	}

	// All Requests tab: all statuses for this user, most recent first
	allApprovals, err := h.enforcer.ListUserAllApprovals(user.ID)
	if err == nil {
		allViews := make([]map[string]interface{}, 0, len(allApprovals))
		completedCount := 0
		deniedCount := 0

		for _, a := range allApprovals {
			var prettyArgs string
			if argsJSON, err := json.MarshalIndent(json.RawMessage(a.ToolArgs), "", "  "); err == nil {
				prettyArgs = string(argsJSON)
			} else {
				prettyArgs = a.ToolArgs
			}

			view := map[string]interface{}{
				"ID":            a.ID,
				"Status":        a.Status,
				"StatusClass":   getStatusClass(a.Status),
				"ToolName":      a.ToolName,
				"UserID":        a.UserID,
				"UserEmail":     a.UserEmail,
				"ToolArgs":      prettyArgs,
				"PolicyID":      a.PolicyID,
				"Message":       a.ViolationMsg,
				"RequestedAt":   a.RequestedAt,
				"ExpiresAt":     a.ExpiresAt,
				"Justification": a.Justification,
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
			case "COMPLETED", "EXECUTING":
				completedCount++
			case "DENIED", "EXPIRED", "FAILED":
				deniedCount++
			}

			allViews = append(allViews, view)
		}
		data["AllApprovals"] = allViews
		data["CompletedCount"] = completedCount
		data["DeniedCount"] = deniedCount
	}

	if err := h.templates.ExecuteTemplate(w, "user_enforcer_queue.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserApproveRequest approves a user's own pending request
func (h *EnforcerHandler) UserApproveRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
	// the result — all in one step.
	updatedReq, err := h.enforcer.ExecuteApprovedRequest(req.ApprovalID, user.ID, req.Comments)
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

// UserDenyRequest denies a user's own pending request
func (h *EnforcerHandler) UserDenyRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

	if err := h.enforcer.DenyRequest(req.ApprovalID, user.ID, req.Reason); err != nil {
		http.Error(w, "Failed to deny: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Request denied",
	})
}

// UserOverridesPageHandler displays the user's tool overrides
func (h *EnforcerHandler) UserOverridesPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	enforcerStore := store.NewEnforcerStore(h.store.DB())
	overrides, err := enforcerStore.ListOverrides()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter overrides to only show those that require user approval (not admin)
	var userOverrides []enforcer.EnforcerOverrideRow
	for _, override := range overrides {
		// Only show overrides for tools that require user-level HITL
		if h.enforcer != nil {
			resolver := h.enforcer.GetResolver()
			profile, err := resolver.Resolve(override.ToolName, override.BackendID)
			if err == nil && profile.RequiresHITL {
				userOverrides = append(userOverrides, override)
			}
		} else {
			// If enforcer is not available, show all overrides
			userOverrides = append(userOverrides, override)
		}
	}

	data := map[string]interface{}{
		"User":      user,
		"Overrides": userOverrides,
		"Prefill": map[string]string{
			"tool_name":  r.URL.Query().Get("tool_name"),
			"backend_id": r.URL.Query().Get("backend_id"),
		},
	}

	if err := h.templates.ExecuteTemplate(w, "user_enforcer_overrides.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// UserOverrideCreateHandler creates or updates a user's tool override
func (h *EnforcerHandler) UserOverrideCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

	http.Redirect(w, r, "/web/user/enforcer/overrides", http.StatusSeeOther)
}

// UserOverrideDeleteHandler deletes a user's tool override
func (h *EnforcerHandler) UserOverrideDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	toolName := r.URL.Query().Get("tool_name")
	backendID := r.URL.Query().Get("backend_id")
	if toolName == "" || backendID == "" {
		http.Error(w, "tool_name and backend_id are required", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/web/user/enforcer/overrides", http.StatusSeeOther)
}

// UserSSEHandler streams real-time queue updates for the authenticated user.
// It sends a "pending_count" event whenever the user's queue changes, and
// a "ping" keepalive every 5 seconds so clients can detect disconnects.
func (h *EnforcerHandler) UserSSEHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	fmt.Fprintf(w, "data: %s\n\n", `{"type":"connected"}`)
	flusher.Flush()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastCount int = -1

	for {
		select {
		case <-ticker.C:
			if h.enforcer == nil {
				fmt.Fprintf(w, "data: %s\n\n", `{"type":"ping"}`)
				flusher.Flush()
				continue
			}

			approvals, err := h.enforcer.ListUserPendingApprovals()
			if err != nil {
				fmt.Fprintf(w, "data: %s\n\n", `{"type":"ping"}`)
				flusher.Flush()
				continue
			}

			// Filter to this user's pending approvals
			var userApprovals []enforcer.ApprovalRequestRow
			for _, a := range approvals {
				if a.UserID == user.ID {
					userApprovals = append(userApprovals, a)
				}
			}

			count := len(userApprovals)
			if count != lastCount {
				lastCount = count
				payload := map[string]interface{}{
					"type":  "pending_count",
					"count": count,
				}
				if count > 0 {
					payload["approval_id"] = userApprovals[0].ID
				}
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "data: %s\n\n", data)
			} else {
				fmt.Fprintf(w, "data: %s\n\n", `{"type":"ping"}`)
			}
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// UserQueuesPageHandler shows a read-only admin view of all user-tier pending
// approval requests, grouped by user. Admins can see who has items waiting
// but cannot approve/deny from this page — those must go through the user
// themselves or via the main admin queue if an admin-tier escalation occurs.
func (h *EnforcerHandler) UserQueuesPageHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	data := map[string]interface{}{
		"User":   user,
		"ByUser": []map[string]interface{}{},
		"Total":  0,
	}

	if h.enforcer == nil {
		if err := h.templates.ExecuteTemplate(w, "admin_enforcer_user_queues.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	approvals, err := h.enforcer.ListUserPendingApprovals()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Group by UserID
	byUser := map[string]map[string]interface{}{}
	order := []string{}

	for _, a := range approvals {
		if a.Status != "PENDING" {
			continue
		}
		uid := a.UserID
		if uid == "" {
			uid = "unknown"
		}
		if _, exists := byUser[uid]; !exists {
			byUser[uid] = map[string]interface{}{
				"UserID":    uid,
				"UserEmail": a.UserEmail,
				"Items":     []map[string]interface{}{},
			}
			order = append(order, uid)
		}

		var prettyArgs string
		if b, err2 := json.MarshalIndent(json.RawMessage(a.ToolArgs), "", "  "); err2 == nil {
			prettyArgs = string(b)
		} else {
			prettyArgs = a.ToolArgs
		}

		item := map[string]interface{}{
			"ID":            a.ID,
			"ToolName":      a.ToolName,
			"ToolArgs":      prettyArgs,
			"Justification": a.Justification,
			"PolicyID":      a.PolicyID,
			"Message":       a.ViolationMsg,
			"RequestedAt":   a.RequestedAt,
			"ExpiresAt":     a.ExpiresAt,
		}
		byUser[uid]["Items"] = append(byUser[uid]["Items"].([]map[string]interface{}), item)
	}

	rows := make([]map[string]interface{}, 0, len(order))
	for _, uid := range order {
		rows = append(rows, byUser[uid])
	}

	data["ByUser"] = rows
	data["Total"] = len(approvals)

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_user_queues.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
