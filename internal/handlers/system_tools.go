package handlers

import mcp "github.com/mark3labs/mcp-go/mcp"

// SystemToolDefinition represents a system tool provided by the bridge
type SystemToolDefinition struct {
	Name        string
	Description string
	InputSchema mcp.ToolInputSchema
}

// SystemTools returns all system tools provided by the bridge
var SystemTools = []SystemToolDefinition{
	{
		Name:        "mcpbridge_ping",
		Description: "Check bridge connectivity and get current timestamp",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
	{
		Name:        "mcpbridge_version",
		Description: "Get mcp-bridge version information",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
	{
		Name:        "mcpbridge_list_backends",
		Description: "List configured backends (admin sees all, users see their token-enabled backends)",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
	{
		Name:        "mcpbridge_refresh_tools",
		Description: "Refresh and list tools from all enabled backends",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
	{
		Name:        "mcpbridge_pool_status",
		Description: "Get pool status for all user pools: warm process count, current size, min/max pool sizes",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
	{
		Name:        "mcpbridge_capabilities",
		Description: "Get bridge capabilities: available backends, user configuration status, and system tools",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	},
}

// SystemToolNames returns just the names of system tools for quick lookup
var SystemToolNames = func() map[string]bool {
	m := make(map[string]bool)
	for _, t := range SystemTools {
		m[t.Name] = true
	}
	return m
}()

// IsSystemTool returns true if the given tool name is a system tool
func IsSystemTool(name string) bool {
	return SystemToolNames[name]
}
