package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
)

type EnforcerHandler struct {
	enforcer  *enforcer.Enforcer
	templates *template.Template
}

func NewEnforcerHandler(e *enforcer.Enforcer, t *template.Template) *EnforcerHandler {
	return &EnforcerHandler{enforcer: e, templates: t}
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
	data := map[string]interface{}{
		"Title":     "Approval Queue",
		"Approvals": []map[string]interface{}{},
		"Count":     0,
	}

	approvals, err := h.enforcer.ListPendingApprovals()
	if err == nil {
		views := make([]map[string]interface{}, len(approvals))
		for i, a := range approvals {
			var prettyArgs string
			if argsJSON, err := json.MarshalIndent(json.RawMessage(a.ToolArgs), "", "  "); err == nil {
				prettyArgs = string(argsJSON)
			} else {
				prettyArgs = a.ToolArgs
			}
			views[i] = map[string]interface{}{
				"ID":          a.ID,
				"ToolName":    a.ToolName,
				"UserID":      a.UserID,
				"UserEmail":   a.UserEmail,
				"ToolArgs":    prettyArgs,
				"PolicyID":    a.PolicyID,
				"Message":     a.ViolationMsg,
				"RequestedAt": a.RequestedAt,
				"ExpiresAt":   a.ExpiresAt,
			}
		}
		data["Approvals"] = views
		data["Count"] = len(approvals)
	}

	if err := h.templates.ExecuteTemplate(w, "admin_enforcer_queue.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *EnforcerHandler) ApproveRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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

	requestBody, err := h.enforcer.ApproveRequest(req.ApprovalID, "admin", req.Comments)
	if err != nil {
		http.Error(w, "Failed to approve: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"message":      "Request approved",
		"request_body": requestBody,
		"instructions": "The original request body has been approved. Use the 'request_body' field to replay the operation.",
	})
}

func (h *EnforcerHandler) DenyRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
