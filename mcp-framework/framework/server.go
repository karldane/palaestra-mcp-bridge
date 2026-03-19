package framework

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolHandler defines the interface for MCP tool implementations
type ToolHandler interface {
	// Name returns the unique name of the tool
	Name() string

	// Description returns the tool description shown to users
	Description() string

	// Schema returns the JSON schema for tool parameters
	Schema() mcp.ToolInputSchema

	// Handle executes the tool with the provided arguments
	Handle(ctx context.Context, args map[string]interface{}) (string, error)

	// GetEnforcerProfile returns the self-reported safety metadata for the tool
	// This profile is transmitted during the tools/list handshake via annotations
	GetEnforcerProfile() EnforcerProfile
}

// Config holds server configuration
type Config struct {
	Name         string
	Version      string
	Instructions string
}

// Server provides the base MCP server functionality
type Server struct {
	name         string
	version      string
	instructions string
	writeEnabled bool
	tools        map[string]ToolHandler
	mcpServer    *server.MCPServer
}

// NewServer creates a new MCP server with the given name and version
func NewServer(name, version string) *Server {
	s := &Server{
		name:         name,
		version:      version,
		writeEnabled: false,
		tools:        make(map[string]ToolHandler),
	}
	return s
}

// SetWriteEnabled enables or disables write tools
func (s *Server) SetWriteEnabled(enabled bool) {
	s.writeEnabled = enabled
}

// IsWriteEnabled returns whether write tools are enabled
func (s *Server) IsWriteEnabled() bool {
	return s.writeEnabled
}

// NewServerWithConfig creates a server with full configuration
func NewServerWithConfig(config *Config) *Server {
	s := NewServer(config.Name, config.Version)
	s.instructions = config.Instructions
	return s
}

// RegisterTool adds a tool handler to the server
func (s *Server) RegisterTool(handler ToolHandler) error {
	name := handler.Name()
	if _, exists := s.tools[name]; exists {
		return fmt.Errorf("tool '%s' already registered", name)
	}
	s.tools[name] = handler
	return nil
}

// ListTools returns a list of registered tool names
func (s *Server) ListTools() []string {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	return names
}

// ExecuteTool runs a tool by name with the provided arguments
func (s *Server) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	handler, exists := s.tools[name]
	if !exists {
		return "", fmt.Errorf("tool '%s' not found", name)
	}

	// Check if write tools are disabled and this is a write tool
	profile := handler.GetEnforcerProfile()
	if !s.writeEnabled && (profile.ImpactScope == ImpactWrite || profile.ImpactScope == ImpactDelete || profile.ImpactScope == ImpactAdmin) {
		return "", fmt.Errorf("Write tools are disabled. Enable with --write-enabled flag.")
	}

	return handler.Handle(ctx, args)
}

// Initialize sets up the MCP server with all registered tools
func (s *Server) Initialize() {
	serverOptions := []server.ServerOption{}

	if s.instructions != "" {
		serverOptions = append(serverOptions, server.WithInstructions(s.instructions))
	}

	s.mcpServer = server.NewMCPServer(s.name, s.version, serverOptions...)

	// Register all tools with the MCP server
	for _, handler := range s.tools {
		profile := handler.GetEnforcerProfile()

		// Helper function to convert bool to *bool
		boolPtr := func(b bool) *bool {
			return &b
		}

		tool := mcp.Tool{
			Name:        handler.Name(),
			Description: handler.Description(),
			InputSchema: handler.Schema(),
			Annotations: mcp.ToolAnnotation{
				Title:          handler.Name(),
				ReadOnlyHint:   boolPtr(profile.ImpactScope == ImpactRead),
				IdempotentHint: boolPtr(profile.Idempotent),
				OpenWorldHint:  boolPtr(profile.PIIExposure),
			},
			// Store the full profile in Meta for the Bridge to access
			Meta: &mcp.Meta{
				AdditionalFields: map[string]any{
					"enforcer_profile": profile,
				},
			},
		}

		// Store values needed in closure
		toolHandler := handler
		toolProfile := profile

		// Register the tool handler
		s.mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// Check if write tools are disabled and this is a write tool
			if !s.writeEnabled && (toolProfile.ImpactScope == ImpactWrite || toolProfile.ImpactScope == ImpactDelete || toolProfile.ImpactScope == ImpactAdmin) {
				return mcp.NewToolResultError("Write tools are disabled. Enable with --write-enabled flag."), nil
			}

			var args map[string]interface{}
			if request.Params.Arguments != nil {
				if argMap, ok := request.Params.Arguments.(map[string]interface{}); ok {
					args = argMap
				}
			}
			result, err := toolHandler.Handle(ctx, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		})
	}
}

// Start begins serving MCP requests via stdio (blocking)
func (s *Server) Start() error {
	if s.mcpServer == nil {
		s.Initialize()
	}
	return server.ServeStdio(s.mcpServer)
}

// GetMCPServer returns the underlying MCP server for testing or customization
func (s *Server) GetMCPServer() *server.MCPServer {
	return s.mcpServer
}
