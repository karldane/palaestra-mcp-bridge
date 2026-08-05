package main

import (
	"context"
	"strings"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

func TestHandlePingTool(t *testing.T) {
	srv := &MCPBridgeServer{}
	result, err := srv.handlePingTool(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handlePingTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.HasPrefix(text.Text, "pong ") {
		t.Errorf("expected 'pong ' prefix, got %q", text.Text)
	}
}

func TestHandleVersionTool(t *testing.T) {
	srv := &MCPBridgeServer{}
	result, err := srv.handleVersionTool(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleVersionTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "mcp-bridge version") {
		t.Errorf("expected version string, got %q", text.Text)
	}
}

func TestHandleCapabilitiesTool(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo tools",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})
	s.SetBackendCapabilities("qdrant", []map[string]interface{}{
		{"name": "search", "description": "search"},
	})

	s.CreateUser(&store.User{ID: "testuser", Name: "test", Email: "test@local", Password: "x", Role: "user"})
	s.SetUserToken(&store.UserToken{UserID: "testuser", BackendID: "qdrant", Value: "token"})

	srv := &MCPBridgeServer{
		app: &app{
			store: s,
		},
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handleCapabilitiesTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{},
	})
	if err != nil {
		t.Fatalf("handleCapabilitiesTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "MCP Bridge Capabilities") {
		t.Errorf("expected capabilities header, got %q", text.Text)
	}
	if !strings.Contains(text.Text, "qdrant") {
		t.Errorf("expected qdrant backend info, got %q", text.Text)
	}
}

func TestHandleApprovalStatusTool_NotFound(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	srv := &MCPBridgeServer{
		app: &app{
			enforcer: enf,
		},
	}
	result, err := srv.handleApprovalStatusTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{
				"approval_id": "nonexistent",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleApprovalStatusTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "not found") {
		t.Errorf("expected not-found message, got %q", text.Text)
	}
}

func TestHandleApprovalStatusTool_Pending(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	_, err = s.DB().Exec(`INSERT INTO users (id, name, email, password, role) VALUES (?,?,?,?,?)`,
		"testuser", "Test User", "test@local", "x", "user")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	approvalID, err := enf.RequestApproval(context.Background(), enforcer.DecisionContext{
		UserID:    "testuser",
		Tool:      "test_tool",
		BackendID: "test",
		Args:      map[string]interface{}{},
	}, "test_policy", "test", "user", enforcer.MatchContextRiskLimit)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	srv := &MCPBridgeServer{
		app: &app{
			enforcer: enf,
		},
	}
	result, err := srv.handleApprovalStatusTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{
				"approval_id": approvalID,
			},
		},
	})
	if err != nil {
		t.Fatalf("handleApprovalStatusTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, approvalID) {
		t.Errorf("expected approval ID in output, got %q", text.Text)
	}
	if !strings.Contains(text.Text, "PENDING") {
		t.Errorf("expected PENDING status in output, got %q", text.Text)
	}
}

func TestHandleApprovalStatusTool_NoEnforcer(t *testing.T) {
	srv := &MCPBridgeServer{
		app: &app{},
	}
	result, err := srv.handleApprovalStatusTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{
				"approval_id": "test",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleApprovalStatusTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "Enforcer not available") {
		t.Errorf("expected enforcer-not-available message, got %q", text.Text)
	}
}

func TestHandleApprovalStatusTool_MissingID(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	srv := &MCPBridgeServer{
		app: &app{
			enforcer: enf,
		},
	}
	result, err := srv.handleApprovalStatusTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{},
		},
	})
	if err != nil {
		t.Fatalf("handleApprovalStatusTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "approval_id is required") {
		t.Errorf("expected missing-id message, got %q", text.Text)
	}
}

func TestHandleApprovalStatusTool_InvalidArgsType(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	srv := &MCPBridgeServer{
		app: &app{
			enforcer: enf,
		},
	}
	result, err := srv.handleApprovalStatusTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: "not a map",
		},
	})
	if err != nil {
		t.Fatalf("handleApprovalStatusTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "Invalid arguments") {
		t.Errorf("expected invalid-args message, got %q", text.Text)
	}
}

func TestHandleQuotasTool_NoEnforcer(t *testing.T) {
	srv := &MCPBridgeServer{
		app: &app{},
	}
	result, err := srv.handleQuotasTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{},
	})
	if err != nil {
		t.Fatalf("handleQuotasTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "Enforcer not available") {
		t.Errorf("expected enforcer-not-available message, got %q", text.Text)
	}
}

func TestHandleQuotasTool_WithConfig(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	srv := &MCPBridgeServer{
		app: &app{
			store:    s,
			enforcer: enf,
		},
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handleQuotasTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]interface{}{
				"backend_id": "testbackend",
			},
		},
	})
	if err != nil {
		t.Fatalf("handleQuotasTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "Rate Limit Quotas") {
		t.Errorf("expected rate limit header, got %q", text.Text)
	}
}
