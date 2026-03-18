package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
	log.Printf("CUSTOM DEBUG: handleToolsList called for userID: %s", userID)
	log.Printf("handleToolsList called for userID: %s", userID)
	backends, err := a.store.ListBackends()
	if err != nil {
		log.Printf("Error listing backends: %v", err)
		// Fallback to default backend on error
		handleDefaultBackend(a, w, r, userID, body, id)
		return
	}
	log.Printf("Found %d backends", len(backends))
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
			log.Printf("Skipping disabled backend: %s", backend.ID)
			continue
		}
		log.Printf("Processing backend: %s (tool_prefix: %s, pool_size: %d)", backend.ID, backend.ToolPrefix, backend.PoolSize)

		pool := a.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

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
			log.Printf("Sending tools/list request to backend %s: %s", backend.ID, string(reqBody))

			respCh := pool.RegisterRequest(reqID)
			proc.Stdin.Write(reqBody)

			select {
			case response, ok := <-respCh:
				pool.UnregisterRequest(reqID)
				if ok && len(response) > 0 {
					log.Printf("Received response from backend %s: %s", backend.ID, string(response))
					var result struct {
						Result struct {
							Tools []map[string]interface{} `json:"tools"`
						} `json:"result"`
						Error map[string]interface{} `json:"error"`
					}
					if err := json.Unmarshal(response, &result); err == nil {
						if result.Error != nil {
							log.Printf("tools/list error from backend %s: %v", backend.ID, result.Error)
							if firstError == nil {
								firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
							}
						} else {
							log.Printf("tools/list success from backend %s, got %d tools", backend.ID, len(result.Result.Tools))
							// Add prefix to tool names if configured
							prefix := a.toolMuxer.GetPrefixForBackend(backend.ID)
							log.Printf("Tool prefix for backend %s: %s", backend.ID, prefix)
							for _, tool := range result.Result.Tools {
								if name, ok := tool["name"].(string); ok && prefix != "" {
									log.Printf("Adding prefix %s to tool %s", prefix, name)
									tool["name"] = prefix + "_" + name
								}
								allTools = append(allTools, tool)
							}
						}
					} else {
						log.Printf("Error unmarshaling response from backend %s: %v", backend.ID, err)
					}
				} else {
					log.Printf("Empty or invalid response from backend %s", backend.ID)
				}
			case <-time.After(10 * time.Second):
				pool.UnregisterRequest(reqID)
				log.Printf("tools/list timeout from backend %s", backend.ID)
			}

			pool.Warm <- proc
		default:
			log.Printf("No warm process for backend %s", backend.ID)
		}
	}

	// Add system tools
	systemTools := shared.SystemToolsAsMap()
	log.Printf("Adding %d system tools", len(systemTools))
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
	log.Printf("Returning %d total tools", len(allTools))

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
								status := "disabled"
								if b.Enabled {
									status = "enabled"
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

	// Use muxer to route to correct backend
	modifiedBody, router, err := a.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		log.Printf("tools/call routing error: %v", err)
		// Fallback to default backend
		handleDefaultBackend(a, w, r, userID, body, id)
		return
	}

	pool := router.Pool
	pool.TouchLastUsed()

	select {
	case proc := <-pool.Warm:
		// Ensure we have a valid ID
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
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 30s"}}`))
		}

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}

// handleDefaultBackend routes to the default backend (legacy behavior)
func handleDefaultBackend(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := a.defaultBackendID()
	pool := a.getPoolForUser(userID, backendID)
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
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 30s"}}`))
		}

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}
