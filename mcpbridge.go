package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/mark3labs/mcp-go/server"
	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
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
	s.registerSystemTools(mcpServer)

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
