package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// handleToolsList aggregates tools from all enabled backends
func (s *MCPBridgeServer) handleToolsList(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	shared.Debugf("handleToolsList: userID=%s START", userID)
	backends, err := s.app.store.ListBackends()
	if err != nil {
		shared.Debugf("handleToolsList: ERROR listing backends: %v", err)
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}
	shared.Debugf("handleToolsList: found %d backends in DB", len(backends))
	for i, b := range backends {
		shared.Debugf("handleToolsList: backend[%d]: id=%s, enabled=%v, command=%s", i, b.ID, b.Enabled, b.Command)
	}
	if len(backends) == 0 {
		shared.Debugf("handleToolsList: no backends, falling back to default")
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}

	var allTools []map[string]interface{}

	// Build augmentation specs for justification injection
	var augmentSpecs []muxer.AugmentSpec
	if s.app.enforcer != nil && s.app.enforcer.Config().MinJustificationLength > 0 {
		augmentSpecs = []muxer.AugmentSpec{{
			Name:        "justification",
			Type:        "string",
			Description: fmt.Sprintf("Required: explain why this tool call is necessary. Minimum %d characters.", s.app.enforcer.Config().MinJustificationLength),
			MinLength:   s.app.enforcer.Config().MinJustificationLength,
			Required:    true,
		}}
	}

	// Load cached capabilities for all backends
	cachedCaps, _ := s.app.store.GetAllBackendCapabilities()

	// Load backend statuses for unavailable backends
	statusMap := make(map[string]string) // backendID -> status
	statuses, _ := s.app.store.ListBackendStatuses()
	for _, st := range statuses {
		statusMap[st.BackendID] = st.Status
	}

	for _, backend := range backends {
		if !backend.Enabled {
			shared.Debugf("handleToolsList: skipping disabled backend: %s", backend.ID)
			continue
		}

		shared.Debugf("handleToolsList: processing backend: %s for user: %s", backend.ID, userID)
		pool := s.app.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		// Check if backend is marked unavailable in DB
		backendStatus := statusMap[backend.ID]
		if backendStatus == "unavailable" {
			shared.Debugf("handleToolsList: backend %s is unavailable (DB status), attempting reconnect", backend.ID)
			// Try to reconnect immediately for user-initiated request
			pool.ForceReconnect()
		}

		// Check circuit breaker state
		if pool.IsUnavailable() {
			shared.Debugf("handleToolsList: backend %s circuit breaker open, using cached tools", backend.ID)
			if caps, ok := cachedCaps[backend.ID]; ok {
				prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
				var toolSlice []map[string]interface{}
				for _, tool := range caps.Tools {
					if name, ok := tool["name"].(string); ok && prefix != "" {
						tool["name"] = prefix + "_" + name
					}
					// Mark as unavailable with annotation
					if backend.ToolHints != "" {
						annotations, hasAnnotations := tool["annotations"].(map[string]interface{})
						if !hasAnnotations {
							annotations = make(map[string]interface{})
							tool["annotations"] = annotations
						}
						annotations["hints"] = backend.ToolHints
					}
					annotations, hasAnnotations := tool["annotations"].(map[string]interface{})
					if !hasAnnotations {
						annotations = make(map[string]interface{})
						tool["annotations"] = annotations
					}
					annotations["unavailable"] = true
					annotations["unavailable_reason"] = "Backend temporarily unavailable - will retry automatically"
					toolSlice = append(toolSlice, tool)
				}
				if len(augmentSpecs) > 0 && !backend.SkipJustification {
					muxer.AugmentToolList(toolSlice, backend.ID, augmentSpecs)
				}
				allTools = append(allTools, toolSlice...)
			}
			continue
		}

		// Try to use cached tools first (fast path)
		if caps, ok := cachedCaps[backend.ID]; ok && len(caps.Tools) > 0 {
			shared.Debugf("handleToolsList: using cached tools for backend %s (%d tools)", backend.ID, len(caps.Tools))
			prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
			var toolSlice []map[string]interface{}
			for _, tool := range caps.Tools {
				if name, ok := tool["name"].(string); ok && prefix != "" {
					tool["name"] = prefix + "_" + name
				}
				if backend.ToolHints != "" {
					annotations, hasAnnotations := tool["annotations"].(map[string]interface{})
					if !hasAnnotations {
						annotations = make(map[string]interface{})
						tool["annotations"] = annotations
					}
					annotations["hints"] = backend.ToolHints
				}
				toolSlice = append(toolSlice, tool)
			}
			if len(augmentSpecs) > 0 && !backend.SkipJustification {
				muxer.AugmentToolList(toolSlice, backend.ID, augmentSpecs)
			}
			allTools = append(allTools, toolSlice...)
			// Background refresh: try to update cache asynchronously
			go s.refreshBackendTools(userID, backend)
			continue
		}

		// No cache - need to spawn process
		shared.Debugf("handleToolsList: no cached tools for backend %s, spawning process", backend.ID)

		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			if strings.Contains(err.Error(), "max_pool_size reached") {
				shared.Debugf("handleToolsList: max pool size reached for backend %s: %v", backend.ID, err)
			} else if strings.Contains(err.Error(), "backend unavailable") {
				shared.Debugf("handleToolsList: backend %s is unavailable (circuit breaker open)", backend.ID)
			} else {
				shared.Debugf("handleToolsList: timeout waiting for warm process for backend %s", backend.ID)
			}
			// Add placeholder for unavailable backend
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_unavailable",
				"description": "Backend temporarily unavailable: " + err.Error(),
			})
			continue
		}

		shared.Debugf("handleToolsList: got warm process for backend %s", backend.ID)
		reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "tools/list",
			"id":      reqID,
		}
		reqBody, _ := json.Marshal(req)
		shared.Debugf("handleToolsList: sending tools/list to backend %s, reqID=%s", backend.ID, reqID)

		respCh := pool.RegisterRequest(reqID)
		reqBody = append(reqBody, '\n')
		proc.Stdin.Write(reqBody)

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			shared.Debugf("handleToolsList: received response from backend %s, ok=%v, len=%d", backend.ID, ok, len(response))
			if ok && len(response) > 0 {
				var result struct {
					Result struct {
						Tools []map[string]interface{} `json:"tools"`
					} `json:"result"`
					Error map[string]interface{} `json:"error"`
				}
				if err := json.Unmarshal(response, &result); err == nil {
					if result.Error != nil {
						shared.Debugf("handleToolsList: tools/list error from backend %s: %v", backend.ID, result.Error)
						// Mark backend as unavailable
						s.app.store.SetBackendUnavailable(backend.ID, fmt.Sprintf("%v", result.Error))
					} else {
						shared.Debugf("handleToolsList: backend %s returned %d tools", backend.ID, len(result.Result.Tools))
						if err := s.app.store.SetBackendCapabilities(backend.ID, result.Result.Tools); err != nil {
							shared.Debugf("handleToolsList: failed to cache capabilities for %s: %v", backend.ID, err)
						} else {
							shared.Debugf("handleToolsList: cached %d tools for backend %s", len(result.Result.Tools), backend.ID)
						}
						// Mark backend as available
						s.app.store.SetBackendAvailable(backend.ID)
						prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
						shared.Debugf("handleToolsList: prefix for backend %s: %q", backend.ID, prefix)
						var toolSlice []map[string]interface{}
						for _, tool := range result.Result.Tools {
							if name, ok := tool["name"].(string); ok && prefix != "" {
								tool["name"] = prefix + "_" + name
							}
							if backend.ToolHints != "" {
								annotations, hasAnnotations := tool["annotations"].(map[string]interface{})
								if !hasAnnotations {
									annotations = make(map[string]interface{})
									tool["annotations"] = annotations
								}
								annotations["hints"] = backend.ToolHints
							}
							toolSlice = append(toolSlice, tool)
						}
						if len(augmentSpecs) > 0 && !backend.SkipJustification {
							muxer.AugmentToolList(toolSlice, backend.ID, augmentSpecs)
						}
						allTools = append(allTools, toolSlice...)
					}
				} else {
					shared.Debugf("handleToolsList: JSON unmarshal error from backend %s: %v", backend.ID, err)
				}
			}
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			shared.Debugf("handleToolsList: TIMEOUT waiting for tools/list from backend %s, killing stuck process", backend.ID)
			proc.Kill()
			// Mark backend as unavailable
			s.app.store.SetBackendUnavailable(backend.ID, "timeout waiting for tools/list")
			continue
		}

		pool.Warm <- proc
	}

	// Add mcpbridge system tools
	allTools = append(allTools, shared.SystemToolsAsMap()...)

	respID := id
	if respID == nil || respID == "" {
		respID = 1
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      respID,
		"result": map[string]interface{}{
			"tools": allTools,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleToolsCall routes the call to the correct backend based on tool name prefix
func (s *MCPBridgeServer) handleToolsCall(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	// Extract tool name, arguments, and justification for enforcer check
	var toolName string
	var toolArgs map[string]interface{}
	var justification string
	var toolReq map[string]interface{}
	if err := json.Unmarshal(body, &toolReq); err == nil {
		if params, ok := toolReq["params"].(map[string]interface{}); ok {
			toolName, _ = params["name"].(string)
			toolArgs, _ = params["arguments"].(map[string]interface{})
			// Extract justification from arguments (standard MCP field), with fallback
			if toolArgs != nil {
				justification, _ = toolArgs["justification"].(string)
				delete(toolArgs, "justification") // strip before forwarding to backend
			}
			if justification == "" {
				justification, _ = params["justification"].(string) // backwards compat
			}
		}
	}

	// Get backend ID from tool name (needed for enforcer)
	backendID := ""
	if toolName != "" && !strings.HasPrefix(toolName, "mcpbridge_") {
		if bid, _, err := s.toolMuxer.FindBackendForTool(toolName); err == nil {
			backendID = bid
		}
	}

	// Resolve per-backend SkipJustification flag
	backendSkipJustification := false
	if backendID != "" {
		if b, err := s.app.store.GetBackend(backendID); err == nil {
			backendSkipJustification = b.SkipJustification
		}
	}

	// Enforcer check - policy enforcement
	if s.app.enforcer != nil && toolName != "" && !strings.HasPrefix(toolName, "mcpbridge_") {
		ctx := r.Context()
		shared.Infof("Enforcer: Evaluating tool call - user=%s tool=%s backend=%s", userID, toolName, backendID)
		decision, err := s.app.enforcer.HandleToolCall(ctx, userID, toolName, toolArgs, backendID, justification, enforcer.CallOptions{SkipJustification: backendSkipJustification})
		if err != nil && decision.Action == "" {
			shared.Errorf("Enforcer error: %v", err)
		} else {
			shared.Infof("Enforcer: Decision for %s - Action=%s", toolName, decision.Action)
			switch decision.Action {
			case enforcer.ActionDeny:
				shared.Debugf("Enforcer DENIED tool call: %s - %s", toolName, decision.Message)
				// Return 200 OK with JSON-RPC error in body (standard JSON-RPC pattern)
				// This ensures clients parse the actual error message instead of showing generic HTTP error
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]interface{}{
						"code":    -32001,
						"message": decision.Message,
						"data": map[string]interface{}{
							"policy_id": decision.PolicyID,
							"action":    "denied",
						},
					},
				})
				return

			case enforcer.ActionPendingApproval, enforcer.ActionPendingAdminApproval:
				shared.Debugf("Enforcer PENDING_APPROVAL for tool: %s", toolName)
				approvalID, err := s.app.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID:        userID,
					Tool:          toolName,
					Args:          toolArgs,
					BackendID:     backendID,
					Justification: justification,
					RequestBody:   string(body),
				}, decision.PolicyID, decision.Message, "admin")
				if err != nil {
					shared.Errorf("Failed to create approval request: %v", err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"error": map[string]interface{}{
							"code":    -32002,
							"message": "Failed to create approval request: " + err.Error(),
						},
					})
					return
				}
				// Return 403 Forbidden with approval details in body
				// MCP client shows HTTP error messages to LLM, so use error response
				respID := id
				if respID == nil {
					respID = 1
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Enforcer-Status", "pending_approval")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      respID,
					"error": map[string]interface{}{
						"code":           -32003,
						"message":        decision.Message,
						"approval_id":    approvalID,
						"policy_id":      decision.PolicyID,
						"tool":           toolName,
						"requires_human": true,
						"status_url":     "/web/admin/enforcer/api/approval-status?id=" + approvalID,
						"instructions":   "Use mcpbridge_check_approval_status tool with approval_id: " + approvalID + " to check if this request was approved.",
					},
				})
				return

			case enforcer.ActionPendingUserApproval:
				shared.Debugf("Enforcer PENDING_USER_APPROVAL for tool: %s", toolName)
				approvalID, err := s.app.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID:        userID,
					Tool:          toolName,
					Args:          toolArgs,
					BackendID:     backendID,
					Justification: justification,
					RequestBody:   string(body),
				}, decision.PolicyID, decision.Message, "user")
				if err != nil {
					shared.Errorf("Failed to create user approval request: %v", err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"error": map[string]interface{}{
							"code":    -32002,
							"message": "Failed to create approval request: " + err.Error(),
						},
					})
					return
				}
				// Return 202 Accepted with user approval details
				respID := id
				if respID == nil {
					respID = 1
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Enforcer-Status", "pending_user_approval")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      respID,
					"result": map[string]interface{}{
						"status":      "pending_user_approval",
						"approval_id": approvalID,
						"message":     decision.Message,
					},
				})
				return

			case enforcer.ActionWarn:
				shared.Debugf("Enforcer WARNING for tool: %s - %s", toolName, decision.Message)
				w.Header().Set("X-Enforcer-Warning", decision.Message)
				// Continue to execute the tool

			case enforcer.ActionAllow:
				shared.Debugf("Enforcer ALLOWED tool: %s", toolName)
				// Continue normally
			}
		}
	}

	modifiedBody, router, err := s.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		shared.Errorf("tools/call routing error: %v", err)
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}

	pool := router.Pool
	pool.TouchLastUsed()

	proc, err := pool.GetWarmWithRetry(poolmgr.DefaultWarmWaitTimeout)
	if err != nil {
		shared.Errorf("handleToolsCall: no warm process available: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes available"))
		return
	}

	var msg poolmgr.JSONRPCMessage
	if err := json.Unmarshal(modifiedBody, &msg); err != nil {
		pool.ReleaseWarm(proc)
		http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
		return
	}

	reqID := fmt.Sprintf("%v", msg.ID)
	if reqID == "" || reqID == "<nil>" {
		reqID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
		msg.ID = reqID
		modifiedBody, _ = json.Marshal(msg)
	}

	buf := new(bytes.Buffer)
	if err := json.Compact(buf, modifiedBody); err != nil {
		buf.Reset()
		buf.Write(modifiedBody)
	}

	respCh := pool.RegisterRequest(reqID)
	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())
	shared.Debugf("handleToolsCall: sent request to backend, waiting for response (timeout=60s)")

	select {
	case response, ok := <-respCh:
		pool.UnregisterRequest(reqID)
		if ok && len(response) > 0 {
			shared.Debugf("handleToolsCall: received response, len=%d", len(response))
			w.Header().Set("Content-Type", "application/json")
			w.Write(response)
		} else {
			shared.Debugf("handleToolsCall: empty or invalid response")
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"No response received"}}`))
		}
	case <-time.After(60 * time.Second):
		pool.UnregisterRequest(reqID)
		shared.Debugf("handleToolsCall: TIMEOUT after 60s, killing stuck process")
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 60s"}}`))
		// Don't return stuck process to pool - kill it and let pool refill
		proc.Kill()
		return
	}

	pool.ReleaseWarm(proc)
}

// refreshBackendTools asynchronously refreshes cached tools for a backend
func (s *MCPBridgeServer) refreshBackendTools(userID string, backend *store.Backend) {
	go func() {
		shared.Debugf("refreshBackendTools: refreshing tools for backend %s", backend.ID)
		pool := s.app.getPoolForUser(userID, backend.ID)

		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			shared.Debugf("refreshBackendTools: failed to get warm process for %s: %v", backend.ID, err)
			return
		}

		reqID := fmt.Sprintf("refresh-%s-%d", backend.ID, time.Now().UnixNano())
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "tools/list",
			"id":      reqID,
		}
		reqBody, _ := json.Marshal(req)

		respCh := pool.RegisterRequest(reqID)
		reqBody = append(reqBody, '\n')
		proc.Stdin.Write(reqBody)

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			if ok && len(response) > 0 {
				var result struct {
					Result struct {
						Tools []map[string]interface{} `json:"tools"`
					} `json:"result"`
					Error map[string]interface{} `json:"error"`
				}
				if err := json.Unmarshal(response, &result); err == nil {
					if result.Error == nil && len(result.Result.Tools) > 0 {
						if err := s.app.store.SetBackendCapabilities(backend.ID, result.Result.Tools); err != nil {
							shared.Debugf("refreshBackendTools: failed to cache capabilities for %s: %v", backend.ID, err)
						} else {
							shared.Debugf("refreshBackendTools: refreshed %d tools for backend %s", len(result.Result.Tools), backend.ID)
						}
						s.app.store.SetBackendAvailable(backend.ID)
					} else if result.Error != nil {
						s.app.store.SetBackendUnavailable(backend.ID, fmt.Sprintf("%v", result.Error))
					}
				}
			}
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			shared.Debugf("refreshBackendTools: timeout for backend %s", backend.ID)
			proc.Kill()
			s.app.store.SetBackendUnavailable(backend.ID, "timeout during background refresh")
		}

		pool.Warm <- proc
	}()
}

// handleDefaultBackend routes to the default backend (legacy behavior)
func (s *MCPBridgeServer) handleDefaultBackend(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := s.app.defaultBackendID()
	pool := s.app.getPoolForUser(userID, backendID)
	pool.TouchLastUsed()

	proc, err := pool.GetWarmWithRetry(poolmgr.DefaultWarmWaitTimeout)
	if err != nil {
		shared.Errorf("handleDefaultBackend: no warm process available: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes available"))
		return
	}

	var msg poolmgr.JSONRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		pool.ReleaseWarm(proc)
		http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
		return
	}

	reqID := fmt.Sprintf("%v", msg.ID)
	if reqID == "" || reqID == "<nil>" {
		reqID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
		msg.ID = reqID
		body, _ = json.Marshal(msg)
	}

	buf := new(bytes.Buffer)
	if err := json.Compact(buf, body); err != nil {
		buf.Reset()
		buf.Write(body)
	}

	respCh := pool.RegisterRequest(reqID)
	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())

	select {
	case response, ok := <-respCh:
		pool.UnregisterRequest(reqID)
		if ok && len(response) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Write(response)
		} else {
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"No response received"}}`))
		}
	case <-time.After(60 * time.Second):
		pool.UnregisterRequest(reqID)
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 60s"}}`))
		// Don't return stuck process to pool - kill it and let pool refill
		proc.Kill()
		return
	}

	pool.ReleaseWarm(proc)
}
