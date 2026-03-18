package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// registerSystemTools adds mcpbridge system tools to the MCP server
func (s *MCPBridgeServer) registerSystemTools(mcpServer *server.MCPServer) {
	// README tool - must be first!
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_0_README",
		Description: "🚨 START HERE! 🚨 CRITICAL: Read this BEFORE using any other tools! Contains essential usage guidance, hints for all backends, and company-specific information. Failure to read this first may result in incorrect queries and wasted API calls.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleReadmeTool)

	// Ping tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_ping",
		Description: "Check bridge connectivity and get current timestamp",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handlePingTool)

	// Version tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_version",
		Description: "Get mcp-bridge version information",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleVersionTool)

	// List backends tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_list_backends",
		Description: "List configured backends (admin sees all, users see their token-enabled backends)",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleListBackendsTool)

	// Refresh tools tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_refresh_tools",
		Description: "Refresh and list tools from all enabled backends",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleRefreshToolsTool)

	// Capabilities tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_capabilities",
		Description: "Get bridge capabilities: available backends, user configuration status, and system tools. Use this for quick discovery of what's available.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleCapabilitiesTool)

	// Pool status tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_pool_status",
		Description: "Get pool status for all user pools: warm process count, current size, min/max pool sizes. Shows resource usage per backend+user combination.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handlePoolStatusTool)
}

func (s *MCPBridgeServer) handlePingTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("pong " + time.Now().UTC().Format(time.RFC3339)), nil
}

func (s *MCPBridgeServer) handleVersionTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("mcp-bridge version 1.0.0"), nil
}

func (s *MCPBridgeServer) handleListBackendsTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
}

func (s *MCPBridgeServer) handleRefreshToolsTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := ctx.Value("user_id").(string)

	backends, err := s.app.store.ListBackends()
	if err != nil {
		return mcp.NewToolResultText("Error listing backends: " + err.Error()), nil
	}

	var result strings.Builder
	result.WriteString("mcpbridge system tools:\n")
	result.WriteString("- mcpbridge_ping\n")
	result.WriteString("- mcpbridge_version\n")
	result.WriteString("- mcpbridge_list_backends\n")
	result.WriteString("- mcpbridge_refresh_tools\n\n")

	result.WriteString("Backend tools:\n")
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
							result.WriteString(fmt.Sprintf("Backend %s error: %v\n", backend.ID, rpcResult.Error))
						} else {
							prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
							result.WriteString(fmt.Sprintf("Backend %s (prefix: %s):\n", backend.ID, prefix))
							for _, tool := range rpcResult.Result.Tools {
								if name, ok := tool["name"].(string); ok {
									fullName := name
									if prefix != "" {
										fullName = prefix + "_" + name
									}
									result.WriteString(fmt.Sprintf("- %s\n", fullName))
								}
							}
							result.WriteString("\n")
						}
					}
				}
			case <-time.After(10 * time.Second):
				pool.UnregisterRequest(reqID)
				result.WriteString(fmt.Sprintf("Backend %s: timeout getting tools\n\n", backend.ID))
			}

			pool.Warm <- proc
		default:
			result.WriteString(fmt.Sprintf("Backend %s: no warm process available\n\n", backend.ID))
		}
	}

	return mcp.NewToolResultText(result.String()), nil
}

func (s *MCPBridgeServer) handleCapabilitiesTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := ctx.Value("user_id").(string)

	cachedCaps, err := s.app.store.GetAllBackendCapabilities()
	if err != nil {
		cachedCaps = make(map[string]*store.BackendCapabilities)
	}

	backends, err := s.app.store.ListBackends()
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}

	userTokens, err := s.app.store.GetAllUserTokens(userID)
	if err != nil {
		userTokens = []*store.UserToken{}
	}

	userBackendTokens := make(map[string]bool)
	for _, token := range userTokens {
		userBackendTokens[token.BackendID] = true
	}

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

	var result strings.Builder
	result.WriteString("=== MCP Bridge Capabilities ===\n\n")

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
}

func (s *MCPBridgeServer) handlePoolStatusTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := ctx.Value("user_id").(string)

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
}

func (s *MCPBridgeServer) handleReadmeTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := ctx.Value("user_id").(string)

	var result strings.Builder
	result.WriteString("╔════════════════════════════════════════════════════════════════════════╗\n")
	result.WriteString("║           🚀 MCP BRIDGE - START HERE - READ ME FIRST 🚀               ║\n")
	result.WriteString("╚════════════════════════════════════════════════════════════════════════╝\n\n")

	// Get global settings
	globalHints, _ := s.app.store.GetSetting("global_hints")
	if globalHints != "" {
		result.WriteString("📋 GLOBAL INFORMATION\n")
		result.WriteString("═══════════════════════\n")
		result.WriteString(globalHints)
		result.WriteString("\n\n")
	}

	// Get enabled backends
	backends, err := s.app.store.ListBackends()
	if err != nil {
		return mcp.NewToolResultText("Error listing backends: " + err.Error()), nil
	}

	userTokens, _ := s.app.store.GetAllUserTokens(userID)
	userBackendTokens := make(map[string]bool)
	for _, token := range userTokens {
		userBackendTokens[token.BackendID] = true
	}

	for _, backend := range backends {
		if !backend.Enabled || !userBackendTokens[backend.ID] {
			continue
		}

		result.WriteString(fmt.Sprintf("\n📦 BACKEND: %s\n", strings.ToUpper(backend.ID)))
		result.WriteString(strings.Repeat("═", 40+len(backend.ID)))
		result.WriteString("\n\n")

		// Per-backend tool hints
		if backend.ToolHints != "" {
			result.WriteString("📝 Usage Hints:\n")
			result.WriteString(backend.ToolHints)
			result.WriteString("\n\n")
		}

		// Backend's own instructions from initialize (get from pool - fresh)
		pool := s.app.getPoolForUser(userID, backend.ID)
		if pool != nil {
			instructions := pool.GetInstructions()
			if instructions != "" {
				result.WriteString("📖 Backend Instructions:\n")
				result.WriteString(instructions)
				result.WriteString("\n\n")
			}
		}
	}

	result.WriteString("\n✅ You are now ready to use MCP Bridge tools!\n")
	result.WriteString("Remember: When using GitHub search tools, use filters like 'is:pr org:tusker-direct'\n")
	result.WriteString("For Jira, use projectKey=PROJ to filter by project.\n")

	return mcp.NewToolResultText(result.String()), nil
}
