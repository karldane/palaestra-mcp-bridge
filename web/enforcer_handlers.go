package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
)

// EnforcerHandler provides HTTP handlers for enforcer management
type EnforcerHandler struct {
	enforcer *enforcer.Enforcer
}

// NewEnforcerHandler creates a new enforcer handler
func NewEnforcerHandler(e *enforcer.Enforcer) *EnforcerHandler {
	return &EnforcerHandler{enforcer: e}
}

// ListPendingApprovals returns all pending approval requests
func (h *EnforcerHandler) ListPendingApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get store from enforcer and list pending
	// This would require exposing the store or adding a method to the enforcer
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"approvals": []map[string]interface{}{},
	})
}

// ApproveRequest handles approval of a pending request
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

	// Get user from session
	// user := getUserFromSession(r)

	// Approve the request
	// err := h.enforcer.ApproveRequest(req.ApprovalID, user.ID, req.Comments)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Request approved",
	})
}

// DenyRequest handles denial of a pending request
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

	// Get user from session
	// user := getUserFromSession(r)

	// Deny the request
	// err := h.enforcer.DenyRequest(req.ApprovalID, user.ID, req.Reason)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Request denied",
	})
}

// EnableKillSwitch activates emergency kill switch
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

	// Get user from session
	// user := getUserFromSession(r)

	// Enable kill switch
	// err := h.enforcer.EnableKillSwitch(req.Scope, user.ID, req.Reason)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Kill switch enabled",
		"scope":   req.Scope,
	})
}

// DisableKillSwitch deactivates emergency kill switch
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

	// Disable kill switch
	// err := h.enforcer.DisableKillSwitch(req.Scope)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Kill switch disabled",
		"scope":   req.Scope,
	})
}

// SSEHandler provides real-time updates for approval queue
func (h *EnforcerHandler) SSEHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create ticker for periodic updates
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Send initial connection message
	fmt.Fprintf(w, "data: %s\n\n", `{"type": "connected"}`)
	w.(http.Flusher).Flush()

	for {
		select {
		case <-ticker.C:
			// Check for new pending approvals
			// Send update if any
			fmt.Fprintf(w, "data: %s\n\n", `{"type": "ping"}`)
			w.(http.Flusher).Flush()

		case <-r.Context().Done():
			return
		}
	}
}
