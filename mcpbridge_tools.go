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

	// Approval status tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_approval_status",
		Description: "Check the status of a pending approval request. Use this after receiving an approval_id from a blocked tool call to see if an admin has approved or denied your request.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"approval_id": map[string]interface{}{
					"type":        "string",
					"description": "The approval request ID returned from the blocked tool call",
				},
			},
			Required: []string{"approval_id"},
		},
	}, s.handleApprovalStatusTool)

	// Rate limit quotas tool
	mcpServer.AddTool(mcp.Tool{
		Name:        "mcpbridge_quotas",
		Description: "Get your current rate limit quotas. Shows available tokens in risk and resource buckets per backend. Risk buckets limit destructive operations (writes/deletes), resource buckets limit expensive API calls.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"backend_id": map[string]interface{}{
					"type":        "string",
					"description": "Optional: specific backend to check (e.g., 'slack', 'newrelic'). If not provided, shows all backends.",
				},
			},
			Required: []string{},
		},
	}, s.handleQuotasTool)
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

func (s *MCPBridgeServer) handleApprovalStatusTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.app.enforcer == nil {
		return mcp.NewToolResultText("Error: Enforcer not available"), nil
	}

	args, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultText("Error: Invalid arguments"), nil
	}

	approvalID, ok := args["approval_id"].(string)
	if !ok || approvalID == "" {
		return mcp.NewToolResultText("Error: approval_id is required"), nil
	}

	approval, err := s.app.enforcer.GetApprovalRequest(approvalID)
	if err != nil {
		return mcp.NewToolResultText("Error: Approval request not found or expired"), nil
	}

	var result strings.Builder
	result.WriteString("=== Approval Status ===\n\n")
	result.WriteString(fmt.Sprintf("Approval ID: %s\n", approval.ID))
	result.WriteString(fmt.Sprintf("Status: %s\n", approval.Status))
	result.WriteString(fmt.Sprintf("Tool: %s\n", approval.ToolName))
	result.WriteString(fmt.Sprintf("Requested at: %s\n", approval.RequestedAt.Format("2006-01-02 15:04:05 MST")))
	result.WriteString(fmt.Sprintf("Expires at: %s\n", approval.ExpiresAt.Format("2006-01-02 15:04:05 MST")))

	switch approval.Status {
	case "PENDING":
		result.WriteString("\n⏳ Request is pending administrator review.\n")
		result.WriteString("Please wait for an administrator to approve or deny.\n")

	case "EXECUTING":
		result.WriteString("\n🔄 Request is being executed.\n")
		result.WriteString("Please check back shortly for results.\n")

	case "COMPLETED":
		result.WriteString("\n✅ Request was APPROVED and EXECUTED!\n")
		if approval.ApprovedBy.Valid {
			result.WriteString(fmt.Sprintf("Approved by: %s\n", approval.ApprovedBy.String))
		}
		if approval.Comments != "" {
			result.WriteString(fmt.Sprintf("Comments: %s\n", approval.Comments))
		}
		result.WriteString(fmt.Sprintf("\nHTTP Status Code: %d\n", approval.ResponseStatus))
		result.WriteString("\nResponse Body:\n")
		result.WriteString(approval.ResponseBody)

	case "FAILED":
		result.WriteString("\n❌ Request was APPROVED but EXECUTION FAILED.\n")
		if approval.ApprovedBy.Valid {
			result.WriteString(fmt.Sprintf("Approved by: %s\n", approval.ApprovedBy.String))
		}
		if approval.ErrorMsg != "" {
			result.WriteString(fmt.Sprintf("\nError: %s\n", approval.ErrorMsg))
		}
		if approval.ResponseStatus > 0 {
			result.WriteString(fmt.Sprintf("HTTP Status Code: %d\n", approval.ResponseStatus))
		}
		if approval.ResponseBody != "" {
			result.WriteString("\nResponse from server:\n")
			result.WriteString(approval.ResponseBody)
		}
		result.WriteString("\nOriginal request body:\n")
		result.WriteString(approval.RequestBody)

	case "DENIED":
		result.WriteString("\n❌ Request was DENIED.\n")
		if approval.ApprovedBy.Valid {
			result.WriteString(fmt.Sprintf("Denied by: %s\n", approval.ApprovedBy.String))
		}
		if approval.DenialReason != "" {
			result.WriteString(fmt.Sprintf("Reason: %s\n", approval.DenialReason))
		}

	case "EXPIRED":
		result.WriteString("\n⏰ Request has EXPIRED.\n")
		result.WriteString("Please submit a new request if needed.\n")
	}

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

func (s *MCPBridgeServer) handleQuotasTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.app.enforcer == nil {
		return mcp.NewToolResultText("Error: Enforcer not available"), nil
	}

	userID := ctx.Value("user_id").(string)
	backendID, _ := request.Params.Arguments.(map[string]interface{})["backend_id"].(string)

	var result strings.Builder
	result.WriteString("=== Rate Limit Quotas ===\n\n")

	backends, err := s.app.store.ListBackends()
	if err != nil {
		return mcp.NewToolResultText("Error: " + err.Error()), nil
	}

	for _, backend := range backends {
		if !backend.Enabled {
			continue
		}

		if backendID != "" && backend.ID != backendID {
			continue
		}

		status := s.app.enforcer.GetRateLimitStatus(userID, backend.ID)

		result.WriteString(fmt.Sprintf("📦 BACKEND: %s\n", strings.ToUpper(backend.ID)))
		result.WriteString(strings.Repeat("─", 30+len(backend.ID)))
		result.WriteString("\n\n")

		if riskBucket, ok := status["risk_bucket"].(map[string]interface{}); ok {
			riskAvailable := int(riskBucket["available"].(float64))
			riskCapacity := int(riskBucket["capacity"].(float64))
			riskRefill := int(riskBucket["refill_rate"].(float64))
			riskUsed := riskCapacity - riskAvailable
			riskPct := float64(riskUsed) / float64(riskCapacity) * 100
			result.WriteString(fmt.Sprintf("  🔴 Risk Bucket: %d/%d tokens (%.0f%% used)\n", riskAvailable, riskCapacity, riskPct))
			result.WriteString(fmt.Sprintf("     Refill rate: %d tokens/minute\n", riskRefill))
			result.WriteString(fmt.Sprintf("     ⚠️  Limits write and delete operations\n\n"))
		}

		if resBucket, ok := status["resource_bucket"].(map[string]interface{}); ok {
			resAvailable := int(resBucket["available"].(float64))
			resCapacity := int(resBucket["capacity"].(float64))
			resRefill := int(resBucket["refill_rate"].(float64))
			resUsed := resCapacity - resAvailable
			resPct := float64(resUsed) / float64(resCapacity) * 100
			result.WriteString(fmt.Sprintf("  🟡 Resource Bucket: %d/%d tokens (%.0f%% used)\n", resAvailable, resCapacity, resPct))
			result.WriteString(fmt.Sprintf("     Refill rate: %d tokens/minute\n", resRefill))
			result.WriteString(fmt.Sprintf("     ⚠️  Limits expensive API operations\n\n"))
		}

		result.WriteString("\n")
	}

	result.WriteString("💡 Tips:\n")
	result.WriteString("- Risk bucket depletes on write/delete operations\n")
	result.WriteString("- Resource bucket depletes on all operations\n")
	result.WriteString("- Both buckets refill automatically over time\n")
	result.WriteString("- Contact admin if you need higher limits\n")

	return mcp.NewToolResultText(result.String()), nil
}
