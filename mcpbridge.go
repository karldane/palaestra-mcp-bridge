package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

type MCPBridgeServer struct {
	app       *app
	toolMuxer *muxer.ToolMuxer
}

func NewMCPBridgeServer(a *app, toolMuxer *muxer.ToolMuxer) *MCPBridgeServer {
	return &MCPBridgeServer{
		app:       a,
		toolMuxer: toolMuxer,
	}
}

func (s *MCPBridgeServer) Handler() http.Handler {
	// Create MCP server with our tools
	mcpServer := server.NewMCPServer("mcp-bridge", "1.0.0")

	// Add mcpbridge system tools
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_ping",
		Description: "Check bridge connectivity and get current timestamp",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("pong " + time.Now().UTC().Format(time.RFC3339)), nil
	})

	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_version",
		Description: "Get mcp-bridge version information",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("mcp-bridge version 1.0.0"), nil
	})

	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_list_backends",
		Description: "List configured backends (admin sees all, users see their token-enabled backends)",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backends, err := s.app.store.ListBackends()
		if err != nil {
			return mcp.NewToolResultText("Error: " + err.Error()), nil
		}

		var result string
		for _, b := range backends {
			status := "disabled"
			if b.Enabled {
				status = "enabled"
			}
			result += "- " + b.ID + ": " + status + "\n"
		}

		return mcp.NewToolResultText(result), nil
	})

	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_refresh_tools",
		Description: "Refresh and list tools from all enabled backends",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID := ctx.Value("user_id").(string)

		// Get tools from all backends
		backends, err := s.app.store.ListBackends()
		if err != nil {
			return mcp.NewToolResultText("Error listing backends: " + err.Error()), nil
		}

		var result string
		result += "mcpbridge system tools:\n"
		result += "- mcpbridge_ping\n"
		result += "- mcpbridge_version\n"
		result += "- mcpbridge_list_backends\n"
		result += "- mcpbridge_refresh_tools\n\n"

		result += "Backend tools:\n"
		for _, backend := range backends {
			if !backend.Enabled {
				continue
			}

			pool := s.app.getPoolForUser(userID, backend.ID)
			pool.TouchLastUsed()

			select {
			case proc := <-pool.Warm:
				reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
				req := map[string]interface{}{
					"jsonrpc": "2.0",
					"method":  "tools/list",
					"id":      reqID,
				}
				reqBody, _ := json.Marshal(req)
				reqBody = append(reqBody, '\n')

				respCh := pool.RegisterRequest(reqID)
				proc.Stdin.Write(reqBody)

				select {
				case response, ok := <-respCh:
					pool.UnregisterRequest(reqID)
					if ok && len(response) > 0 {
						var rpcResult struct {
							Result struct {
								Tools []map[string]interface{} `json:"tools"`
							} `json:"result"`
							Error map[string]interface{} `json:"error"`
						}
						if err := json.Unmarshal(response, &rpcResult); err == nil {
							if rpcResult.Error != nil {
								result += fmt.Sprintf("Backend %s error: %v\n", backend.ID, rpcResult.Error)
							} else {
								prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
								result += fmt.Sprintf("Backend %s (prefix: %s):\n", backend.ID, prefix)
								for _, tool := range rpcResult.Result.Tools {
									if name, ok := tool["name"].(string); ok {
										fullName := name
										if prefix != "" {
											fullName = prefix + "_" + name
										}
										result += fmt.Sprintf("- %s\n", fullName)
									}
								}
								result += "\n"
							}
						}
					}
				case <-time.After(10 * time.Second):
					pool.UnregisterRequest(reqID)
					result += fmt.Sprintf("Backend %s: timeout getting tools\n\n", backend.ID)
				}

				pool.Warm <- proc
			default:
				result += fmt.Sprintf("Backend %s: no warm process available\n\n", backend.ID)
			}
		}

		return mcp.NewToolResultText(result), nil
	})

	// Add mcpbridge_capabilities tool - returns bridge info for quick discovery
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_capabilities",
		Description: "Get bridge capabilities: available backends, user configuration status, and system tools. Use this for quick discovery of what's available.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID := ctx.Value("user_id").(string)

		// Get cached capabilities for all backends
		cachedCaps, err := s.app.store.GetAllBackendCapabilities()
		if err != nil {
			cachedCaps = make(map[string]*store.BackendCapabilities)
		}

		// Get all backends
		backends, err := s.app.store.ListBackends()
		if err != nil {
			return mcp.NewToolResultText("Error: " + err.Error()), nil
		}

		// Get user's tokens to determine which backends they have configured
		userTokens, err := s.app.store.GetUserTokens(userID, "")
		if err != nil {
			userTokens = []*store.UserToken{}
		}

		// Build user token map
		userBackendTokens := make(map[string]bool)
		for _, token := range userTokens {
			userBackendTokens[token.BackendID] = true
		}

		// Build namespace summary and tool counts
		var namespaceSummary []string
		var configuredBackends []string
		var totalTools int
		for _, backend := range backends {
			if userBackendTokens[backend.ID] {
				configuredBackends = append(configuredBackends, backend.ID)
				if caps, ok := cachedCaps[backend.ID]; ok {
					namespaceSummary = append(namespaceSummary, fmt.Sprintf("%s (%d tools)", backend.ID, caps.ToolCount))
					totalTools += caps.ToolCount
				}
			}
		}

		// Build output
		var result strings.Builder
		result.WriteString("=== MCP Bridge Capabilities ===\n\n")

		// Top-level summary
		if len(configuredBackends) > 0 {
			result.WriteString("Available integrations: ")
			result.WriteString(strings.Join(namespaceSummary, ", "))
			result.WriteString(fmt.Sprintf(", Bridge Admin (5 tools). Total: %d tools.\n\n", totalTools+5))
		} else {
			result.WriteString("No backends configured for this user. Bridge Admin (5 tools) is always available.\n\n")
		}

		result.WriteString("--- Backends ---\n")
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
				result.WriteString(fmt.Sprintf("- %s: %s (%d tools)\n", backend.ID, status, caps.ToolCount))
			} else {
				result.WriteString(fmt.Sprintf("- %s: %s\n", backend.ID, status))
			}
		}

		result.WriteString("\n--- System Tools (always available) ---\n")
		result.WriteString("- mcpbridge_ping: Check bridge connectivity\n")
		result.WriteString("- mcpbridge_version: Get version info\n")
		result.WriteString("- mcpbridge_list_backends: List backends\n")
		result.WriteString("- mcpbridge_refresh_tools: Refresh tools from backends\n")
		result.WriteString("- mcpbridge_capabilities: This tool\n")

		return mcp.NewToolResultText(result.String()), nil
	})

	// Add mcpbridge_pool_status tool - shows pool state for user's backends
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_pool_status",
		Description: "Get pool status for all user pools: warm process count, current size, min/max pool sizes. Shows resource usage per backend+user combination.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID := ctx.Value("user_id").(string)

		// Get pools for this user
		pools := s.app.poolManager.GetPoolsForUser(userID)

		var result strings.Builder
		result.WriteString("=== Pool Status ===\n\n")

		if len(pools) == 0 {
			result.WriteString("No pools found for this user.\n")
			return mcp.NewToolResultText(result.String()), nil
		}

		totalWarm := 0
		totalCurrent := 0

		for _, ps := range pools {
			totalWarm += ps.WarmCount
			totalCurrent += ps.CurrentSize

			result.WriteString(fmt.Sprintf("Pool: %s:%s\n", ps.BackendID, ps.UserID))
			result.WriteString(fmt.Sprintf("  Command: %s\n", ps.Command))
			result.WriteString(fmt.Sprintf("  Warm: %d, Current: %d\n", ps.WarmCount, ps.CurrentSize))
			result.WriteString(fmt.Sprintf("  Min: %d, Max: %d\n", ps.MinPoolSize, ps.MaxPoolSize))
			result.WriteString("\n")
		}

		result.WriteString(fmt.Sprintf("Total: %d pools, %d warm processes, %d current\n",
			len(pools), totalWarm, totalCurrent))

		return mcp.NewToolResultText(result.String()), nil
	})

	streamableHTTP := server.NewStreamableHTTPServer(mcpServer,
		server.WithStateLess(true),
		server.WithEndpointPath("/"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r)
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Intercept tools/list and tools/call requests for backend tools
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()

			var msg poolmgr.JSONRPCMessage
			if err := json.Unmarshal(body, &msg); err == nil {
				switch msg.Method {
				case "tools/list":
					s.handleToolsList(w, r, userID, body, msg.ID)
					return
				case "tools/call":
					// Check if it's a system tool (mcpbridge_*)
					var toolReq map[string]interface{}
					if err := json.Unmarshal(body, &toolReq); err == nil {
						if params, ok := toolReq["params"].(map[string]interface{}); ok {
							if name, ok := params["name"].(string); ok {
								if shared.IsSystemTool(name) {
									// System tool - let the SDK handle it
									// Need to recreate body since we already read it
									r.Body = io.NopCloser(bytes.NewReader(body))
									ctx := context.WithValue(r.Context(), "user_id", userID)
									streamableHTTP.ServeHTTP(w, r.WithContext(ctx))
									return
								}
							}
						}
					}
					// Backend tool - route to correct backend
					s.handleToolsCall(w, r, userID, body, msg.ID)
					return
				}
			}
			// If unmarshal fails or method doesn't match, recreate the request body
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		// Store userID in context for tool handlers
		ctx := context.WithValue(r.Context(), "user_id", userID)
		streamableHTTP.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleToolsList aggregates tools from all enabled backends
func (s *MCPBridgeServer) handleToolsList(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	fmt.Printf("[DEBUG handleToolsList] userID=%s START\n", userID)
	backends, err := s.app.store.ListBackends()
	if err != nil {
		fmt.Printf("[DEBUG handleToolsList] ERROR listing backends: %v\n", err)
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}
	fmt.Printf("[DEBUG handleToolsList] found %d backends in DB\n", len(backends))
	for i, b := range backends {
		fmt.Printf("[DEBUG handleToolsList] backend[%d]: id=%s, enabled=%v, command=%s\n", i, b.ID, b.Enabled, b.Command)
	}
	if len(backends) == 0 {
		// Fallback to default backend if no backends configured
		fmt.Printf("[DEBUG handleToolsList] no backends, falling back to default\n")
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}

	var allTools []map[string]interface{}
	var firstError error

	for _, backend := range backends {
		if !backend.Enabled {
			fmt.Printf("[DEBUG handleToolsList] skipping disabled backend: %s\n", backend.ID)
			continue
		}

		fmt.Printf("[DEBUG handleToolsList] processing backend: %s for user: %s\n", backend.ID, userID)
		pool := s.app.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		// Try to get a warm process (with max pool size check)
		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			if strings.Contains(err.Error(), "max_pool_size reached") {
				fmt.Printf("[DEBUG handleToolsList] max pool size reached for backend %s: %v\n", backend.ID, err)
			} else {
				fmt.Printf("[DEBUG handleToolsList] timeout waiting for warm process for backend %s\n", backend.ID)
			}
			// Add error info to results for this backend
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_error",
				"description": "Backend temporarily unavailable: " + err.Error(),
			})
			continue
		}

		fmt.Printf("[DEBUG handleToolsList] got warm process for backend %s\n", backend.ID)
		// Build tools/list request
		reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "tools/list",
			"id":      reqID,
		}
		reqBody, _ := json.Marshal(req)
		reqBody = append(reqBody, '\n')
		fmt.Printf("[DEBUG handleToolsList] sending tools/list to backend %s, reqID=%s\n", backend.ID, reqID)

		respCh := pool.RegisterRequest(reqID)
		proc.Stdin.Write(reqBody)

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			fmt.Printf("[DEBUG handleToolsList] received response from backend %s, ok=%v, len=%d\n", backend.ID, ok, len(response))
			if ok && len(response) > 0 {
				var result struct {
					Result struct {
						Tools []map[string]interface{} `json:"tools"`
					} `json:"result"`
					Error map[string]interface{} `json:"error"`
				}
				if err := json.Unmarshal(response, &result); err == nil {
					if result.Error != nil {
						fmt.Printf("[DEBUG handleToolsList] tools/list error from backend %s: %v\n", backend.ID, result.Error)
						if firstError == nil {
							firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
						}
					} else {
						fmt.Printf("[DEBUG handleToolsList] backend %s returned %d tools\n", backend.ID, len(result.Result.Tools))
						// Cache the capabilities for future use
						if err := s.app.store.SetBackendCapabilities(backend.ID, result.Result.Tools); err != nil {
							fmt.Printf("[DEBUG handleToolsList] failed to cache capabilities for %s: %v\n", backend.ID, err)
						} else {
							fmt.Printf("[DEBUG handleToolsList] cached %d tools for backend %s\n", len(result.Result.Tools), backend.ID)
						}
						// Add prefix to tool names if configured
						prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
						fmt.Printf("[DEBUG handleToolsList] prefix for backend %s: %q\n", backend.ID, prefix)
						for _, tool := range result.Result.Tools {
							if name, ok := tool["name"].(string); ok && prefix != "" {
								tool["name"] = prefix + "_" + name
							}
							allTools = append(allTools, tool)
						}
					}
				} else {
					fmt.Printf("[DEBUG handleToolsList] JSON unmarshal error from backend %s: %v\n", backend.ID, err)
				}
			}
		case <-time.After(10 * time.Second):
			pool.UnregisterRequest(reqID)
			fmt.Printf("[DEBUG handleToolsList] TIMEOUT waiting for tools/list from backend %s\n", backend.ID)
		}

		pool.Warm <- proc
	}

	// Add mcpbridge system tools
	allTools = append(allTools, []map[string]interface{}{
		{
			"name":        "mcpbridge_ping",
			"description": "Check bridge connectivity and get current timestamp",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_version",
			"description": "Get mcp-bridge version information",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_list_backends",
			"description": "List configured backends (admin sees all, users see their token-enabled backends)",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_refresh_tools",
			"description": "Refresh and list tools from all enabled backends",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_capabilities",
			"description": "Get bridge capabilities: available backends, user configuration status, and system tools. Use this for quick discovery.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}...)

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
}

// handleToolsCall routes the call to the correct backend based on tool name prefix
func (s *MCPBridgeServer) handleToolsCall(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	// Use muxer to route to correct backend
	modifiedBody, router, err := s.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		fmt.Printf("tools/call routing error: %v\n", err)
		// Fallback to default backend
		s.handleDefaultBackend(w, r, userID, body, id)
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
