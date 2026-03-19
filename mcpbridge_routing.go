package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
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
	var firstError error

	for _, backend := range backends {
		if !backend.Enabled {
			shared.Debugf("handleToolsList: skipping disabled backend: %s", backend.ID)
			continue
		}

		shared.Debugf("handleToolsList: processing backend: %s for user: %s", backend.ID, userID)
		pool := s.app.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			if strings.Contains(err.Error(), "max_pool_size reached") {
				shared.Debugf("handleToolsList: max pool size reached for backend %s: %v", backend.ID, err)
			} else {
				shared.Debugf("handleToolsList: timeout waiting for warm process for backend %s", backend.ID)
			}
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_error",
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
		reqBody = append(reqBody, '\n')
		shared.Debugf("handleToolsList: sending tools/list to backend %s, reqID=%s", backend.ID, reqID)

		respCh := pool.RegisterRequest(reqID)
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
						if firstError == nil {
							firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
						}
					} else {
						shared.Debugf("handleToolsList: backend %s returned %d tools", backend.ID, len(result.Result.Tools))
						if err := s.app.store.SetBackendCapabilities(backend.ID, result.Result.Tools); err != nil {
							shared.Debugf("handleToolsList: failed to cache capabilities for %s: %v", backend.ID, err)
						} else {
							shared.Debugf("handleToolsList: cached %d tools for backend %s", len(result.Result.Tools), backend.ID)
						}
						prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
						shared.Debugf("handleToolsList: prefix for backend %s: %q", backend.ID, prefix)
						for _, tool := range result.Result.Tools {
							if name, ok := tool["name"].(string); ok && prefix != "" {
								tool["name"] = prefix + "_" + name
							}
							// Merge backend tool hints into annotations
							if backend.ToolHints != "" {
								annotations, hasAnnotations := tool["annotations"].(map[string]interface{})
								if !hasAnnotations {
									annotations = make(map[string]interface{})
									tool["annotations"] = annotations
								}
								annotations["hints"] = backend.ToolHints
							}
							allTools = append(allTools, tool)
						}
					}
				} else {
					shared.Debugf("handleToolsList: JSON unmarshal error from backend %s: %v", backend.ID, err)
				}
			}
		case <-time.After(10 * time.Second):
			pool.UnregisterRequest(reqID)
			shared.Debugf("handleToolsList: TIMEOUT waiting for tools/list from backend %s, killing stuck process", backend.ID)
			// Don't return stuck process to pool - kill it and let pool refill
			proc.Kill()
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
	// Extract tool name and arguments for enforcer check
	var toolName string
	var toolArgs map[string]interface{}
	var toolReq map[string]interface{}
	if err := json.Unmarshal(body, &toolReq); err == nil {
		if params, ok := toolReq["params"].(map[string]interface{}); ok {
			toolName, _ = params["name"].(string)
			toolArgs, _ = params["arguments"].(map[string]interface{})
		}
	}

	// Enforcer check - policy enforcement
	if s.app.enforcer != nil && toolName != "" && !strings.HasPrefix(toolName, "mcpbridge_") {
		ctx := r.Context()
		shared.Infof("Enforcer: Evaluating tool call - user=%s tool=%s", userID, toolName)
		decision, err := s.app.enforcer.HandleToolCall(ctx, userID, toolName, toolArgs, "")
		if err != nil {
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

			case enforcer.ActionPendingApproval:
				shared.Debugf("Enforcer PENDING_APPROVAL for tool: %s", toolName)
				approvalID, err := s.app.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID: userID,
					Tool:   toolName,
					Args:   toolArgs,
				}, decision.PolicyID, decision.Message)
				if err != nil {
					shared.Errorf("Failed to create approval request: %v", err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
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
				// Return 200 OK with pending_approval status in result
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]interface{}{
						"status":      "pending_approval",
						"approval_id": approvalID,
						"message":     decision.Message,
						"policy_id":   decision.PolicyID,
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

	select {
	case proc := <-pool.Warm:
		var msg poolmgr.JSONRPCMessage
		if err := json.Unmarshal(modifiedBody, &msg); err != nil {
			pool.Warm <- proc
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
		buf.WriteByte('\n')

		respCh := pool.RegisterRequest(reqID)
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

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}

// handleDefaultBackend routes to the default backend (legacy behavior)
func (s *MCPBridgeServer) handleDefaultBackend(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := s.app.defaultBackendID()
	pool := s.app.getPoolForUser(userID, backendID)
	pool.TouchLastUsed()

	select {
	case proc := <-pool.Warm:
		var msg poolmgr.JSONRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			pool.Warm <- proc
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
		buf.WriteByte('\n')

		respCh := pool.RegisterRequest(reqID)
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

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}
