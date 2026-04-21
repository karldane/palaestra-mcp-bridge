package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
)

// MCPSchemaValidator validates responses against MCP spec
type MCPSchemaValidator struct{}

func (v *MCPSchemaValidator) ValidateInitializeResponse(resp map[string]interface{}) error {
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing result")
	}
	if _, ok := result["protocolVersion"].(string); !ok {
		return fmt.Errorf("missing protocolVersion")
	}
	if _, ok := result["capabilities"].(map[string]interface{}); !ok {
		return fmt.Errorf("missing capabilities")
	}
	if _, ok := result["serverInfo"].(map[string]interface{}); !ok {
		return fmt.Errorf("missing serverInfo")
	}
	return nil
}

func (v *MCPSchemaValidator) ValidateToolsListResponse(resp map[string]interface{}) error {
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing result")
	}
	if tools, ok := result["tools"].([]interface{}); ok {
		for i, t := range tools {
			tool, ok := t.(map[string]interface{})
			if !ok {
				return fmt.Errorf("tool %d: not a map", i)
			}
			if _, ok := tool["name"].(string); !ok {
				return fmt.Errorf("tool %d: missing name", i)
			}
		}
	}
	return nil
}

func (v *MCPSchemaValidator) ValidateToolsCallResponse(resp map[string]interface{}) error {
	// Per MCP spec, tools/call response should have:
	// {"result": {"content": [{"type": "text", "text": "..."}]}}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing result")
	}
	// Our current format uses "tools" but spec says "content"
	if tools, ok := result["tools"].([]interface{}); ok {
		shared.Warnf("[MCPValidator] WARNING: tools/call uses 'tools' array, spec requires 'content' array")
		if len(tools) == 0 {
			return fmt.Errorf("empty tools array")
		}
	}
	return nil
}

var mcpValidator = &MCPSchemaValidator{}

// JSONRPCMessage mirrors the structure in poolmgr/process.go
type JSONRPCMessage struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// v2Handle is the main handler for the /mcp/v2 endpoint
func v2Handle(a *app, w http.ResponseWriter, r *http.Request, userID string) {
	shared.Debugf("[v2] v2Handle: userID=%s", userID)
	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	var msg JSONRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "Invalid JSON-RPC request", http.StatusBadRequest)
		return
	}
	shared.Debugf("[v2] method=%s id=%v", msg.Method, msg.ID)

	switch msg.Method {
	case "initialize":
		v2Initialize(a, w, r, userID, msg.ID)
	case "tools/list":
		v2toolsList(a, w, r, userID, msg.ID)
	case "tools/call":
		// tool handling
		// Parse params to get tool name and arguments
		// Params could be map[string]interface{} or json.RawMessage depending on how it was unmarshaled
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}

		switch p := msg.Params.(type) {
		case json.RawMessage:
			if err := json.Unmarshal(p, &params); err != nil {
				http.Error(w, "Invalid params in tools/call request", http.StatusBadRequest)
				return
			}
		case map[string]interface{}:
			if name, ok := p["name"].(string); ok {
				params.Name = name
			}
			if args, ok := p["arguments"].(map[string]interface{}); ok {
				params.Arguments = args
			}
		default:
			http.Error(w, "Invalid params type in tools/call request", http.StatusBadRequest)
			return
		}
		shared.Debugf("[v2] tools/call: name=%q args=%v", params.Name, params.Arguments)
		toolName := params.Name
		// Strip MCP_Bridge_v2_ prefix if present (some clients add this prefix)
		if strings.HasPrefix(toolName, "MCP_Bridge_v2_") {
			toolName = strings.TrimPrefix(toolName, "MCP_Bridge_v2_")
			shared.Debugf("[v2] stripped prefix, toolName=%s", toolName)
		}

		// Log the final toolName being processed for debugging
		shared.Debugf("[v2] tools/call: processing toolName=%q (original=%q)", toolName, params.Name)

		if toolName == "namespace_expand" || toolName == "MCP_Bridge_v2_namespace_expand" {
			v2namespaceExpand(a, w, r, userID, params.Arguments, msg.ID)
		} else if toolName == "tool_call" || toolName == "MCP_Bridge_v2_tool_call" {
			v2toolCall(a, w, r, userID, params.Arguments, msg.ID)
		} else if strings.HasSuffix(toolName, "_expand") {
			// Handle dynamic namespace_expand calls like "atlassian_expand", "appscan_asoc_expand"
			namespace := strings.TrimSuffix(toolName, "_expand")
			shared.Debugf("[v2] expand: name=%s namespace=%s args=%v", toolName, namespace, params.Arguments)
			// Build params with namespace
			expandParams := map[string]interface{}{"namespace": namespace}
			shared.Debugf("[v2] expand routing: namespace=%s args=%v", namespace, params.Arguments)
			v2namespaceExpand(a, w, r, userID, expandParams, msg.ID)
		} else if strings.HasSuffix(toolName, "_call") {
			// Handle dynamic tool_call calls like "atlassian_call", "appscan_asoc_call"
			namespace := strings.TrimSuffix(toolName, "_call")
			// Build params with namespace
			callParams := map[string]interface{}{
				"namespace":     namespace,
				"tool":          params.Arguments["tool"],
				"params":        params.Arguments["params"],
				"justification": params.Arguments["justification"],
			}
			shared.Debugf("[v2] atlassian_call routing: namespace=%s tool=%v params=%v", namespace, callParams["tool"], callParams["params"])
			v2toolCall(a, w, r, userID, callParams, msg.ID)
		} else {
			// Handle unknown tools/call methods
			shared.Debugf("[v2] tools/call: Method not found for toolName=%q", toolName)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "Method not found: " + toolName,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	default:
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// v2HandleWrapper wraps v2Handle to extract userID from context
func v2HandleWrapper(a *app) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shared.Debugf("[v2Wrapper] %s %s", r.Method, r.URL.Path)

		// Read request body
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		userID := auth.UserIDFromContext(r)
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Streamable HTTP: Only POST is required for request/response
		// GET returns 405 if client tries to open server→client stream (not needed)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Accept", "application/json")
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      nil,
				"error": map[string]interface{}{
					"code":    -32000,
					"message": "GET not supported for v2 endpoint - use POST for tool calls",
				},
			}
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(resp)
			return
		}

		v2Handle(a, w, r, userID)
	}
}

// v2Initialize handles the initialize method for v2
func v2Initialize(a *app, w http.ResponseWriter, r *http.Request, userID string, id interface{}) {
	// Generate a session ID per MCP spec
	sessionID := fmt.Sprintf("session-%d-%s", time.Now().UnixNano(), userID[:8])
	w.Header().Set("MCP-Session-Id", sessionID)

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": true,
				},
			},
			"serverInfo": map[string]interface{}{
				"name":    "mcp-bridge-v2",
				"version": "2.0.0",
			},
		},
	}
	shared.Debugf("[v2Initialize] sessionID=%s", sessionID)
	json.NewEncoder(w).Encode(response)
}

// v2toolsList generates the initial tools/list response for v2
// For opencode compatibility, we return actual tool descriptors from each namespace
func v2toolsList(a *app, w http.ResponseWriter, r *http.Request, userID string, id interface{}) {
	shared.Debugf("v2toolsList: userID=%s START", userID)
	backends, err := a.store.ListBackends()
	if err != nil {
		shared.Errorf("v2toolsList: Error listing backends: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	shared.Debugf("v2toolsList: found %d backends in DB", len(backends))

	var toolsList []map[string]interface{}

	// Add tool entry for each backend namespace pointing to namespace_expand
	for _, backend := range backends {
		shared.Debugf("v2toolsList: processing backend: %s enabled=%v", backend.ID, backend.Enabled)
		if backend.Enabled {
			// Capitalize first letter of each part for display name
			parts := strings.Split(backend.ID, "_")
			for i, part := range parts {
				if len(part) > 0 {
					parts[i] = strings.ToUpper(string(part[0])) + part[1:]
				}
			}
			capitalizedID := strings.Join(parts, "_")

			// Add namespace_expand tool for this backend
			toolsList = append(toolsList, map[string]interface{}{
				"name":        fmt.Sprintf("%s_expand", backend.ID),
				"description": fmt.Sprintf("Expand %s namespace to get available tools", capitalizedID),
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			})

			// Add tool_call tool for this backend
			toolsList = append(toolsList, map[string]interface{}{
				"name":        fmt.Sprintf("%s_call", backend.ID),
				"description": fmt.Sprintf("Call a tool in the %s namespace", capitalizedID),
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool":          map[string]interface{}{"type": "string", "description": "Tool name in " + backend.ID},
						"params":        map[string]interface{}{"type": "object", "description": "Tool parameters"},
						"justification": map[string]interface{}{"type": "string", "description": "Reason for call"},
					},
					"required": []string{"tool", "justification"},
				},
			})
		}
	}
	shared.Debugf("v2toolsList: added %d tool entries for namespace routing", len(toolsList))

	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"tools": toolsList,
		},
	}
	shared.Debugf("v2toolsList: responding with %d tools", len(toolsList))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// v2namespaceExpand handles the namespace_expand tool call for v2
func v2namespaceExpand(a *app, w http.ResponseWriter, r *http.Request, userID string, params map[string]interface{}, id interface{}) {
	namespace, ok := params["namespace"].(string)
	if !ok || namespace == "" {
		http.Error(w, "Missing or invalid 'namespace' parameter", http.StatusBadRequest)
		return
	}

	var allTools []map[string]interface{}
	var finalErr error

	if namespace == "mcpbridge" {
		// Handle internal mcpbridge tools
		allTools = append(allTools, shared.SystemToolsAsMap()...)
	} else {
		// Fetch tools from the specified backend
		backend, err := a.store.GetBackend(namespace)
		if err != nil {
			shared.Errorf("v2namespaceExpand: Backend %s not found: %v", namespace, err)
			http.Error(w, fmt.Sprintf("Backend %s not found", namespace), http.StatusNotFound)
			return
		}
		if !backend.Enabled {
			shared.Errorf("v2namespaceExpand: Backend %s is disabled", namespace)
			http.Error(w, fmt.Sprintf("Backend %s is disabled", namespace), http.StatusServiceUnavailable)
			return
		}

		pool := a.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			finalErr = fmt.Errorf("timeout waiting for warm process for backend %s", backend.ID)
		} else {
			reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
			reqBody, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "tools/list",
				"id":      reqID,
			})

			respCh := pool.RegisterRequest(reqID)
			shared.Debugf("[v2namespaceExpand] REQ: sent tools/list to backend=%s reqID=%s", backend.ID, reqID)
			proc.Stdin.Write(append(reqBody, '\n'))

			select {
			case response, ok := <-respCh:
				pool.UnregisterRequest(reqID)
				shared.Debugf("[v2namespaceExpand] RSP: backend=%s got response len=%d ok=%v", backend.ID, len(response), ok)
				if ok && len(response) > 0 {
					var result struct {
						Result struct {
							Tools []map[string]interface{} `json:"tools"`
						} `json:"result"`
						Error map[string]interface{} `json:"error"`
					}
					if err := json.Unmarshal(response, &result); err == nil {
						if result.Error != nil {
							finalErr = fmt.Errorf("tools/list error from backend %s: %v", backend.ID, result.Error)
						} else {
							allTools = result.Result.Tools
							shared.Debugf("[v2namespaceExpand] OK: got %d tools from backend=%s", len(allTools), backend.ID)
						}
					} else {
						shared.Warnf("[MCPValidator] tools/list response invalid: %v", err)
						finalErr = fmt.Errorf("JSON unmarshal error from backend %s: %v", backend.ID, err)
					}
				} else {
					finalErr = fmt.Errorf("empty or invalid response from backend %s", backend.ID)
				}
			case <-time.After(30 * time.Second):
				pool.UnregisterRequest(reqID)
				proc.Kill()
				shared.Warnf("[v2namespaceExpand] TIMEOUT: backend=%s reqID=%s", backend.ID, reqID)
				finalErr = fmt.Errorf("timeout waiting for tools/list from backend %s", backend.ID)
			}
			pool.Warm <- proc
		}
	}

	if finalErr != nil {
		shared.Errorf("[v2namespaceExpand] ERROR: %v", finalErr)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"error": map[string]interface{}{
				"code":    -32003,
				"message": finalErr.Error(),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	shared.Debugf("[v2namespaceExpand] success: returning %d tools for namespace=%s", len(allTools), namespace)

	// Per MCP spec 2024-11-05, tools/call returns "content" array
	// But for namespace_expand (custom extension), we return tool descriptors
	// Wrap in both formats for compatibility: spec-compliant "content" + legacy "tools"

	// Create text representation of tools for spec-compliant response
	toolsText := fmt.Sprintf("Available tools in namespace '%s': %d tools", namespace, len(allTools))
	toolsText += fmt.Sprintf("\n(%d tool definitions)", len(allTools))
	if len(allTools) > 10 {
		toolsText += fmt.Sprintf("\n... and %d more", len(allTools)-10)
	}

	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": toolsText,
				},
			},
			// Also include raw tools for clients that expect that format
			"tools": allTools,
		},
	}

	// Validate against MCP spec
	if err := mcpValidator.ValidateToolsCallResponse(resp); err != nil {
		shared.Warnf("[MCPValidator] tools/call response warning: %v", err)
	}

	shared.Debugf("[v2namespaceExpand] ENCODING dual-format response with %d tools", len(allTools))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		shared.Errorf("[v2namespaceExpand] ENCODE ERROR: %v", err)
	}
	shared.Debugf("[v2namespaceExpand] DONE - response written with content+tools")
}

// v2toolCall handles the tool_call verb for v2
func v2toolCall(a *app, w http.ResponseWriter, r *http.Request, userID string, params map[string]interface{}, id interface{}) {
	namespace, ok := params["namespace"].(string)
	if !ok || namespace == "" {
		http.Error(w, "Missing or invalid 'namespace' parameter", http.StatusBadRequest)
		return
	}
	toolName, ok := params["tool"].(string)
	if !ok || toolName == "" {
		http.Error(w, "Missing or invalid 'tool' parameter", http.StatusBadRequest)
		return
	}
	toolParams, ok := params["params"].(map[string]interface{})
	if !ok {
		toolParams = make(map[string]interface{})
	}
	justification, ok := params["justification"].(string)
	if !ok || justification == "" {
		http.Error(w, "Missing or invalid 'justification' parameter", http.StatusBadRequest)
		return
	}

	// Validate namespace is a valid backend
	backend, err := a.store.GetBackend(namespace)
	if err != nil || !backend.Enabled {
		http.Error(w, fmt.Sprintf("Backend %s not found or disabled", namespace), http.StatusNotFound)
		return
	}

	// Enforcer check
	if a.enforcer != nil && !strings.HasPrefix(toolName, "mcpbridge_") {
		ctx := r.Context()
		shared.Infof("Enforcer: Evaluating tool call - user=%s tool=%s backend=%s", userID, toolName, namespace)
		decision, err := a.enforcer.HandleToolCall(ctx, userID, toolName, toolParams, namespace, justification, enforcer.CallOptions{SkipJustification: backend.SkipJustification})
		if err != nil && decision.Action == "" {
			shared.Errorf("Enforcer error: %v", err)
		} else {
			shared.Infof("Enforcer: Decision for %s - Action=%s", toolName, decision.Action)
			switch decision.Action {
			case enforcer.ActionDeny:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]interface{}{
						"code":    -32001,
						"message": decision.Message,
					},
				})
				return
			case enforcer.ActionPendingApproval, enforcer.ActionPendingAdminApproval:
				approvalID, err := a.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID:        userID,
					Tool:          toolName,
					Args:          toolParams,
					BackendID:     namespace,
					Justification: justification,
				}, decision.PolicyID, decision.Message, "admin")
				if err != nil {
					http.Error(w, "Failed to create approval request", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Enforcer-Status", "pending_approval")
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]interface{}{
						"code":        -32001,
						"message":     decision.Message,
						"approval_id": approvalID,
					},
				})
				return
			case enforcer.ActionPendingUserApproval:
				approvalID, err := a.enforcer.RequestApproval(ctx, enforcer.DecisionContext{
					UserID:        userID,
					Tool:          toolName,
					Args:          toolParams,
					BackendID:     namespace,
					Justification: justification,
				}, decision.PolicyID, decision.Message, "user")
				if err != nil {
					http.Error(w, "Failed to create approval request", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Enforcer-Status", "pending_user_approval")
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]interface{}{
						"code":        -32001,
						"message":     decision.Message,
						"approval_id": approvalID,
					},
				})
				return
			case enforcer.ActionWarn:
				w.Header().Set("X-Enforcer-Warning", decision.Message)
			case enforcer.ActionAllow:
			}
		}
	}

	// Route to backend
	shared.Debugf("[v2toolCall] START: namespace=%s tool=%s", namespace, toolName)
	pool := a.getPoolForUser(userID, namespace)
	if pool == nil {
		shared.Errorf("[v2toolCall] ERROR: failed to get pool for %s", namespace)
		http.Error(w, "Failed to create pool for backend", http.StatusInternalServerError)
		return
	}
	shared.Debugf("[v2toolCall] got pool, getting warm process")
	pool.TouchLastUsed()

	proc, err := pool.GetWarmWithRetry(poolmgr.DefaultWarmWaitTimeout)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32003,"message":"Backend %s unavailable: %v"}}`, namespace, err)))
		return
	}

	// Build JSON-RPC request
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": toolParams,
		},
		"id": id,
	}
	modifiedBody, err := json.Marshal(msg)
	if err != nil {
		pool.ReleaseWarm(proc)
		http.Error(w, "Invalid JSON-RPC request", http.StatusBadRequest)
		return
	}

	buf := new(bytes.Buffer)
	if err := json.Compact(buf, modifiedBody); err != nil {
		buf.Reset()
		buf.Write(modifiedBody)
	}

	reqID := fmt.Sprintf("v2-%d", time.Now().UnixNano())
	msg["id"] = reqID
	modifiedBody, _ = json.Marshal(msg)
	buf.Reset()
	if json.Compact(buf, modifiedBody); err != nil {
		buf.Reset()
		buf.Write(modifiedBody)
	}

	respCh := pool.RegisterRequest(reqID)
	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())

	shared.Debugf("[v2toolCall] waiting for response...")
	select {
	case response, ok := <-respCh:
		shared.Debugf("[v2toolCall] GOT response len=%d ok=%v", len(response), ok)
		pool.UnregisterRequest(reqID)
		if ok && len(response) > 0 {
			// Replace the backend's internal request ID with the client's original ID
			var respMap map[string]interface{}
			if err := json.Unmarshal(response, &respMap); err == nil {
				respMap["id"] = id
				if rewritten, err := json.Marshal(respMap); err == nil {
					response = rewritten
				}
			}
			// Set Content-Length and Connection: close so client (e.g. httpx aread()) gets EOF
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(response)))
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusOK)
			w.Write(response)
			shared.Debugf("[v2toolCall] DONE - response written, closing body")
		} else {
			pool.ReleaseWarm(proc)
			http.Error(w, "Empty response from backend", http.StatusInternalServerError)
		}
	case <-time.After(60 * time.Second):
		pool.UnregisterRequest(reqID)
		proc.Kill()
		pool.ReleaseWarm(proc)
		http.Error(w, "Timeout waiting for backend", http.StatusGatewayTimeout)
	}
}
