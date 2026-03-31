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
	"github.com/mcp-bridge/mcp-bridge/store"
)

// handleInitialize handles the initialize method
func handleInitialize(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "mcp-bridge",
				"version": "1.0.0",
			},
		},
	}
	json.NewEncoder(w).Encode(response)
}

// handleToolsList aggregates tools from all enabled backends
func handleToolsList(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	shared.Debugf("handleToolsList called for userID: %s", userID)
	shared.Debugf("handleToolsList called for userID: %s", userID)
	backends, err := a.store.ListBackends()
	if err != nil {
		shared.Errorf("Error listing backends: %v", err)
		// Fallback to default backend on error
		handleDefaultBackend(a, w, r, userID, body, id)
		return
	}
	shared.Debugf("Found %d backends", len(backends))
	if len(backends) == 0 {
		// No backends configured, return only system tools
		allTools := shared.SystemToolsAsMap()

		// Build aggregated response
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
		return
	}

	var allTools []map[string]interface{}
	var firstError error

	for _, backend := range backends {
		if !backend.Enabled {
			shared.Debugf("Skipping disabled backend: %s", backend.ID)
			continue
		}
		shared.Debugf("Processing backend: %s (tool_prefix: %s, pool_size: %d)", backend.ID, backend.ToolPrefix, backend.PoolSize)

		pool := a.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		// Skip backends that are known to be unavailable (circuit breaker open)
		if pool.IsUnavailable() {
			shared.Debugf("Backend %s is unavailable (circuit breaker open), skipping", backend.ID)
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_unavailable",
				"description": "Backend unavailable - check configuration",
			})
			continue
		}

		select {
		case proc := <-pool.Warm:
			// Build tools/list request
			reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
			req := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "tools/list",
				"id":      reqID,
			}
			reqBody, _ := json.Marshal(req)
			reqBody = append(reqBody, '\n')
			shared.Debugf("Sending tools/list request to backend %s: %s", backend.ID, string(reqBody))

			respCh := pool.RegisterRequest(reqID)
			proc.Stdin.Write(reqBody)

			select {
			case response, ok := <-respCh:
				pool.UnregisterRequest(reqID)
				if ok && len(response) > 0 {
					shared.Debugf("Received response from backend %s", backend.ID)
					var result struct {
						Result struct {
							Tools []map[string]interface{} `json:"tools"`
						} `json:"result"`
						Error map[string]interface{} `json:"error"`
					}
					if err := json.Unmarshal(response, &result); err == nil {
						if result.Error != nil {
							shared.Errorf("tools/list error from backend %s: %v", backend.ID, result.Error)
							if firstError == nil {
								firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
							}
						} else {
							shared.Infof("tools/list success from backend %s, got %d tools", backend.ID, len(result.Result.Tools))
							// Add prefix to tool names if configured
							prefix := a.toolMuxer.GetPrefixForBackend(backend.ID)
							shared.Debugf("Tool prefix for backend %s: %s", backend.ID, prefix)
							for _, tool := range result.Result.Tools {
								if name, ok := tool["name"].(string); ok && prefix != "" {
									shared.Debugf("Adding prefix %s to tool %s", prefix, name)
									tool["name"] = prefix + "_" + name
								}
								allTools = append(allTools, tool)
							}
						}
					} else {
						shared.Errorf("Error unmarshaling response from backend %s: %v", backend.ID, err)
					}
				} else {
					shared.Debugf("Empty or invalid response from backend %s", backend.ID)
				}
			case <-time.After(10 * time.Second):
				pool.UnregisterRequest(reqID)
				shared.Errorf("tools/list timeout from backend %s, killing stuck process", backend.ID)
				// Don't return stuck process to pool - kill it and let pool refill
				proc.Kill()
				continue
			}

			pool.Warm <- proc
		default:
			shared.Errorf("No warm process for backend %s", backend.ID)
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_starting",
				"description": "Backend starting up or temporarily unavailable",
			})
		}
	}

	// Add system tools
	systemTools := shared.SystemToolsAsMap()
	shared.Debugf("Adding %d system tools", len(systemTools))
	allTools = append(allTools, systemTools...)

	// Build aggregated response
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
	shared.Infof("Returning %d total tools", len(allTools))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleToolsCall routes the call to the correct backend based on tool name prefix
func handleToolsCall(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	// Check if it's a system tool (mcpbridge_*)
	var toolReq map[string]interface{}
	if err := json.Unmarshal(body, &toolReq); err == nil {
		if params, ok := toolReq["params"].(map[string]interface{}); ok {
			if name, ok := params["name"].(string); ok {
				if strings.HasPrefix(name, "mcpbridge_") {
					// System tools are handled directly
					var result string
					switch name {
					case "mcpbridge_ping":
						result = "pong " + time.Now().UTC().Format(time.RFC3339)
					case "mcpbridge_version":
						result = "mcp-bridge version 1.0.0"
					case "mcpbridge_list_backends":
						backends, err := a.store.ListBackends()
						if err != nil {
							result = "Error: " + err.Error()
						} else {
							for _, b := range backends {
								var status string
								if b.IsSystem {
									status = "system (always available)"
								} else if b.Enabled {
									status = "enabled"
								} else {
									status = "disabled"
								}
								result += "- " + b.ID + ": " + status + "\n"
							}
						}
					case "mcpbridge_refresh_tools":
						result = "Refreshed tools from all enabled backends"
					case "mcpbridge_capabilities":
						// Get cached capabilities
						cachedCaps, err := a.store.GetAllBackendCapabilities()
						if err != nil {
							cachedCaps = make(map[string]*store.BackendCapabilities)
						}
						backends, err := a.store.ListBackends()
						if err != nil {
							result = "Error: " + err.Error()
						} else {
							// Get user tokens to determine which backends are configured
							userTokens, err := a.store.GetAllUserTokens(userID)
							if err != nil {
								userTokens = []*store.UserToken{}
							}
							userBackendTokens := make(map[string]bool)
							for _, token := range userTokens {
								userBackendTokens[token.BackendID] = true
							}

							var namespaceSummary []string
							var totalTools int
							for _, backend := range backends {
								if userBackendTokens[backend.ID] {
									if caps, ok := cachedCaps[backend.ID]; ok {
										namespaceSummary = append(namespaceSummary, fmt.Sprintf("%s (%d tools)", backend.ID, caps.ToolCount))
										totalTools += caps.ToolCount
									}
								}
							}
							result = "=== MCP Bridge Capabilities ===\n\n"
							if len(namespaceSummary) > 0 {
								result += "Available integrations: "
								result += strings.Join(namespaceSummary, ", ")
								result += fmt.Sprintf(", Bridge Admin (5 tools). Total: %d tools.\n\n", totalTools+5)
							} else {
								result += "No backends configured for this user. Bridge Admin (5 tools) is always available.\n\n"
							}
							result += "--- Backends ---\n"
							for _, backend := range backends {
								var status string
								if backend.IsSystem {
									status = "system (always available)"
								} else if userBackendTokens[backend.ID] {
									status = "configured"
								} else {
									status = "available (not configured)"
								}
								if caps, ok := cachedCaps[backend.ID]; ok {
									result += fmt.Sprintf("- %s: %s (%d tools)\n", backend.ID, status, caps.ToolCount)
								} else {
									result += fmt.Sprintf("- %s: %s\n", backend.ID, status)
								}
							}
							result += "\n--- System Tools (always available) ---\n"
							result += "- mcpbridge_ping: Check bridge connectivity\n"
							result += "- mcpbridge_version: Get version info\n"
							result += "- mcpbridge_list_backends: List backends\n"
							result += "- mcpbridge_refresh_tools: Refresh tools from backends\n"
							result += "- mcpbridge_capabilities: This tool\n"
						}
					default:
						result = "Unknown system tool: " + name
					}
					response := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"result": map[string]interface{}{
							"content": []map[string]interface{}{
								{
									"type": "text",
									"text": result,
								},
							},
							"status": "ok",
						},
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
					return
				}
			}
		}
	}

	// Extract tool name and arguments for enforcer check
	var toolName string
	var toolArgs map[string]interface{}
	if params, ok := toolReq["params"].(map[string]interface{}); ok {
		toolName, _ = params["name"].(string)
		toolArgs, _ = params["arguments"].(map[string]interface{})
	}

	// Get backend ID from tool name (needed for enforcer)
	backendID := ""
	if toolName != "" && !strings.HasPrefix(toolName, "mcpbridge_") {
		if bid, _, err := a.toolMuxer.FindBackendForTool(toolName); err == nil {
			backendID = bid
		}
	}

	// Enforcer check - policy enforcement
	if a.enforcer != nil && toolName != "" && !strings.HasPrefix(toolName, "mcpbridge_") {
		ctx := r.Context()
		shared.Infof("Enforcer: Evaluating tool call - user=%s tool=%s backend=%s", userID, toolName, backendID)
		decision, err := a.enforcer.HandleToolCall(ctx, userID, toolName, toolArgs, backendID)
		if err != nil {
			shared.Errorf("Enforcer error: %v", err)
			// Continue anyway - fail open is safer than blocking everything
		} else {
			shared.Infof("Enforcer: Decision for %s - Action=%s", toolName, decision.Action)
			switch decision.Action {
			case enforcer.ActionDeny:
				shared.Debugf("Enforcer DENIED tool call: %s - %s", toolName, decision.Message)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]interface{}{
						"code":    -32001,
						"message": "Policy violation: " + decision.Message,
					},
				})
				return

			case enforcer.ActionPendingApproval:
				shared.Debugf("Enforcer PENDING_APPROVAL for tool: %s", toolName)
				// Create approval request
				approvalID, err := a.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID:    userID,
					Tool:      toolName,
					Args:      toolArgs,
					BackendID: backendID,
				}, decision.PolicyID, decision.Message)
				if err != nil {
					shared.Errorf("Failed to create approval request: %v", err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"error": map[string]interface{}{
							"code":    -32002,
							"message": "Failed to create approval request",
						},
					})
					return
				}

				// Return 202 Accepted with approval ID
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]interface{}{
						"status":      "pending_approval",
						"approval_id": approvalID,
						"message":     "This operation requires human approval. Please wait for an administrator to approve.",
					},
				})
				return

			case enforcer.ActionWarn:
				shared.Debugf("Enforcer WARNING for tool: %s - %s", toolName, decision.Message)
				// Add warning header and continue
				w.Header().Set("X-Enforcer-Warning", decision.Message)
				// Continue to execute the tool

			case enforcer.ActionAllow:
				shared.Debugf("Enforcer ALLOWED tool: %s", toolName)
				// Continue normally
			}
		}
	}

	// Use muxer to route to correct backend
	modifiedBody, router, err := a.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		shared.Errorf("tools/call routing error: %v", err)
		// Fallback to default backend
		handleDefaultBackend(a, w, r, userID, body, id)
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

	// Ensure we have a valid ID
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

	// Compact and add newline
	buf := new(bytes.Buffer)
	if err := json.Compact(buf, modifiedBody); err != nil {
		buf.Reset()
		buf.Write(modifiedBody)
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

	pool.ReleaseWarm(proc)
}

// handleDefaultBackend routes to the default backend (legacy behavior)
func handleDefaultBackend(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := a.defaultBackendID()
	pool := a.getPoolForUser(userID, backendID)
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

	pool.ReleaseWarm(proc)
}
