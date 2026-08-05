package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
)

func TestVersion(t *testing.T) {
	v := Version()
	if v != "debug-build" {
		t.Errorf("Version() = %q, want debug-build", v)
	}
}

func TestNewMCPBridgeServer(t *testing.T) {
	a := &app{}
	tm := &muxer.ToolMuxer{}
	srv := NewMCPBridgeServer(a, tm)
	if srv.app != a {
		t.Error("MCPBridgeServer.app not set")
	}
	if srv.toolMuxer != tm {
		t.Error("MCPBridgeServer.toolMuxer not set")
	}
}

func TestRegisterSystemTools(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "1.0")
	srv := &MCPBridgeServer{}
	srv.registerSystemTools(mcpServer)

	tools := mcpServer.ListTools()

	expectedTools := []string{
		"mcpbridge_0_README",
		"mcpbridge_ping",
		"mcpbridge_version",
		"mcpbridge_list_backends",
		"mcpbridge_refresh_tools",
		"mcpbridge_capabilities",
		"mcpbridge_pool_status",
		"mcpbridge_approval_status",
		"mcpbridge_quotas",
	}

	toolNames := make(map[string]bool)
	for name := range tools {
		toolNames[name] = true
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected system tool %q to be registered", name)
		}
	}
}

func TestHandleListBackendsTool(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "be1", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}", IsSystem: false})
	s.CreateBackend(&store.Backend{ID: "be2", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}", IsSystem: true})
	s.CreateBackend(&store.Backend{ID: "be3", Enabled: false, Command: "echo", PoolSize: 1, Env: "{}"})

	srv := &MCPBridgeServer{
		app: &app{store: s},
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handleListBackendsTool(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListBackendsTool: %v", err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "be1") {
		t.Errorf("expected output to contain be1, got: %s", text)
	}
	if !strings.Contains(text, "be2") {
		t.Errorf("expected output to contain be2, got: %s", text)
	}
	if !strings.Contains(text, "be3") {
		t.Errorf("expected output to contain be3, got: %s", text)
	}
}

func TestHandlePoolStatusTool_NoPools(t *testing.T) {
	pm := poolmgr.NewPoolManager("echo", 1)
	srv := &MCPBridgeServer{
		app: &app{poolManager: pm},
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handlePoolStatusTool(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handlePoolStatusTool: %v", err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "No pools") {
		t.Errorf("expected 'No pools' message, got: %s", text)
	}
}

func TestHandleReadmeTool(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}", ToolHints: "use with care"})
	s.CreateUser(&store.User{ID: "testuser", Name: "Test", Email: "t@t.com", Password: "x", Role: "user"})
	s.SetUserToken(&store.UserToken{UserID: "testuser", BackendID: "testbe", Value: "tok"})
	s.SetSetting("global_hints", "Welcome to MCP Bridge")

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := &MCPBridgeServer{
		app:       &app{store: s, poolManager: pm, toolMuxer: tm},
		toolMuxer: tm,
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handleReadmeTool(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleReadmeTool: %v", err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "MCP BRIDGE") {
		t.Errorf("expected MCP BRIDGE header, got: %s", text)
	}
	if !strings.Contains(text, "TESTBE") {
		t.Errorf("expected backend TESTBE in output, got: %s", text)
	}
}

func TestSeedDefaultUser_AlreadyExists(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateUser(&store.User{Name: "Admin", Email: "admin@localhost", Password: "admin", Role: "user"})
	seedDefaultUser(s)

	user, err := s.GetUserByEmail("admin@localhost")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("expected role=admin after seed, got %q", user.Role)
	}
}

func TestSeedDefaultUser_NewUser(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	seedDefaultUser(s)

	user, err := s.GetUserByEmail("admin@localhost")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("expected role=admin, got %q", user.Role)
	}
	if user.Name != "Admin" {
		t.Errorf("expected name=Admin, got %q", user.Name)
	}
}

func TestBuildEnvForScan(t *testing.T) {
	b := &store.Backend{
		ID:      "testbe",
		Command: "echo",
		Env:     `{"MY_KEY": "my_value", "OTHER": "other_val"}`,
	}

	env := buildEnvForScan(b)
	foundMyKey := false
	foundOther := false
	for _, e := range env {
		if e == "MY_KEY=my_value" {
			foundMyKey = true
		}
		if e == "OTHER=other_val" {
			foundOther = true
		}
	}
	if !foundMyKey {
		t.Error("expected MY_KEY in env")
	}
	if !foundOther {
		t.Error("expected OTHER in env")
	}
}

func TestBuildEnvForScan_EmptyEnv(t *testing.T) {
	b := &store.Backend{
		ID:      "testbe",
		Command: "echo",
		Env:     "{}",
	}
	env := buildEnvForScan(b)
	if len(env) < 1 {
		t.Error("expected at least parent env vars")
	}
}

func TestBuildEnvForScan_InvalidJSON(t *testing.T) {
	b := &store.Backend{
		ID:      "testbe",
		Command: "echo",
		Env:     "not-json",
	}
	env := buildEnvForScan(b)
	_ = env
}

func TestLoadOverridesIntoResolver(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	override := enforcer.EnforcerOverrideRow{
		ID:           "ov1",
		ToolName:     "test_tool",
		BackendID:    "test_be",
		RiskLevel:    "high",
		ImpactScope:  "write",
		ResourceCost: 8,
		RequiresHITL: true,
		PIIExposure:  false,
	}
	if err := es.UpsertOverride(override); err != nil {
		t.Fatalf("UpsertOverride: %v", err)
	}

	loadOverridesIntoResolver(s, enf, t.Logf, t.Logf)

	profile, err := enf.GetResolver().Resolve("test_tool", "test_be")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !profile.RequiresHITL {
		t.Error("expected RequiresHITL=true from override")
	}
	if string(profile.Risk) != "high" {
		t.Errorf("expected risk=high, got %q", profile.Risk)
	}
}

func TestLoadOverridesIntoResolver_Empty(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	loadOverridesIntoResolver(s, enf, t.Logf, t.Logf)
}

func TestUserStoreAdapter_GetUser(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateUser(&store.User{ID: "u1", Name: "Test", Email: "test@example.com", Password: "pw", Role: "admin"})

	adapter := &userStoreAdapter{store: s}
	user, err := adapter.GetUser("u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.ID != "u1" {
		t.Errorf("expected ID=u1, got %q", user.ID)
	}
	if user.Email != "test@example.com" {
		t.Errorf("expected email=test@example.com, got %q", user.Email)
	}
	if user.Role != "admin" {
		t.Errorf("expected role=admin, got %q", user.Role)
	}
}

func TestUserStoreAdapter_GetUser_NotFound(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	adapter := &userStoreAdapter{store: s}
	_, err = adapter.GetUser("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestHandleReadmeTool_NoHints(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	s.CreateUser(&store.User{ID: "testuser", Name: "Test", Email: "t@t.com", Password: "x", Role: "user"})

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := &MCPBridgeServer{
		app:       &app{store: s, poolManager: pm, toolMuxer: tm},
		toolMuxer: tm,
	}
	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handleReadmeTool(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleReadmeTool: %v", err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "MCP BRIDGE") {
		t.Errorf("expected MCP BRIDGE header, got: %s", text)
	}
}

func TestHandlePoolStatusTool_WithPools(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := &MCPBridgeServer{
		app:       &app{store: s, poolManager: pm, toolMuxer: tm},
		toolMuxer: tm,
	}

	_ = srv.app.getPoolForUser("testuser", "testbe")

	ctx := context.WithValue(context.Background(), "user_id", "testuser")
	result, err := srv.handlePoolStatusTool(ctx, mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handlePoolStatusTool: %v", err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Pool:") {
		t.Errorf("expected pool status, got: %s", text)
	}
}

func TestGetUserStoreError(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	adapter := &userStoreAdapter{store: s}
	_, err = adapter.GetUser("")
	if err == nil {
		t.Error("expected error for empty user ID")
	}
}

func TestSeedDefaultUser_AlreadyAdmin(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateUser(&store.User{Name: "Admin", Email: "admin@localhost", Password: "admin", Role: "admin"})
	seedDefaultUser(s)

	user, err := s.GetUserByEmail("admin@localhost")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("expected role=admin, got %q", user.Role)
	}
}

func TestBuildEnvForScan_NilEnv(t *testing.T) {
	b := &store.Backend{
		ID:      "testbe",
		Command: "echo",
	}
	env := buildEnvForScan(b)
	if len(env) == 0 {
		t.Error("expected at least some env vars")
	}
}

func TestHandleListBackendsTool_NoUserCtx(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	srv := &MCPBridgeServer{
		app: &app{store: s},
	}
	// Calling without user_id in ctx should not panic
	_, err = srv.handleListBackendsTool(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handleListBackendsTool: %v", err)
	}
}

// ---------- MCPSchemaValidator tests ----------

func TestValidateInitializeResponse_Valid(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"serverInfo":      map[string]interface{}{"name": "test"},
		},
	}
	err := v.ValidateInitializeResponse(resp)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateInitializeResponse_MissingResult(t *testing.T) {
	v := &MCPSchemaValidator{}
	err := v.ValidateInitializeResponse(map[string]interface{}{})
	if err == nil {
		t.Error("expected error for missing result")
	}
}

func TestValidateInitializeResponse_MissingProtocolVersion(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"capabilities": map[string]interface{}{},
			"serverInfo":   map[string]interface{}{},
		},
	}
	err := v.ValidateInitializeResponse(resp)
	if err == nil {
		t.Error("expected error for missing protocolVersion")
	}
}

func TestValidateInitializeResponse_MissingCapabilities(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]interface{}{},
		},
	}
	err := v.ValidateInitializeResponse(resp)
	if err == nil {
		t.Error("expected error for missing capabilities")
	}
}

func TestValidateInitializeResponse_MissingServerInfo(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
		},
	}
	err := v.ValidateInitializeResponse(resp)
	if err == nil {
		t.Error("expected error for missing serverInfo")
	}
}

func TestValidateToolsListResponse_Valid(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{"name": "tool1"},
			},
		},
	}
	err := v.ValidateToolsListResponse(resp)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateToolsListResponse_MissingResult(t *testing.T) {
	v := &MCPSchemaValidator{}
	err := v.ValidateToolsListResponse(map[string]interface{}{})
	if err == nil {
		t.Error("expected error for missing result")
	}
}

func TestValidateToolsListResponse_ToolNotMap(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": []interface{}{"not-a-map"},
		},
	}
	err := v.ValidateToolsListResponse(resp)
	if err == nil {
		t.Error("expected error for tool not a map")
	}
}

func TestValidateToolsListResponse_ToolMissingName(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{"description": "no name"},
			},
		},
	}
	err := v.ValidateToolsListResponse(resp)
	if err == nil {
		t.Error("expected error for tool missing name")
	}
}

func TestValidateToolsCallResponse_Valid(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": []interface{}{
				map[string]interface{}{"name": "tool1"},
			},
		},
	}
	err := v.ValidateToolsCallResponse(resp)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateToolsCallResponse_MissingResult(t *testing.T) {
	v := &MCPSchemaValidator{}
	err := v.ValidateToolsCallResponse(map[string]interface{}{})
	if err == nil {
		t.Error("expected error for missing result")
	}
}

func TestValidateToolsCallResponse_EmptyTools(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": []interface{}{},
		},
	}
	err := v.ValidateToolsCallResponse(resp)
	if err == nil {
		t.Error("expected error for empty tools array")
	}
}

func TestValidateToolsCallResponse_NoTools(t *testing.T) {
	v := &MCPSchemaValidator{}
	resp := map[string]interface{}{
		"result": map[string]interface{}{},
	}
	err := v.ValidateToolsCallResponse(resp)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// ---------- writeJSONRPCError tests ----------

func TestWriteJSONRPCError(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONRPCError(w, 1, -32001, "test error", map[string]interface{}{"detail": "info"})

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", resp.Header.Get("Content-Type"))
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object")
	}
	if errObj["code"] != float64(-32001) {
		t.Errorf("expected code -32001, got %v", errObj["code"])
	}
	if errObj["message"] != "test error" {
		t.Errorf("expected message 'test error', got %v", errObj["message"])
	}
}

func TestWriteJSONRPCError_NilID(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONRPCError(w, nil, -32602, "invalid params", nil)

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if _, hasID := body["id"]; hasID {
		t.Error("expected no id field when id is nil")
	}
}

// ---------- findAvailableBackend tests ----------

func TestFindAvailableBackend_NotFound(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	a := &app{store: s}
	pool, id := findAvailableBackend(a, "user1", "")
	if pool != nil || id != "" {
		t.Errorf("expected nil pool and empty id on no backends, got pool=%v id=%q", pool, id)
	}
}

func TestFindAvailableBackend_WithDisabledBackend(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: false, Command: "echo", PoolSize: 1, Env: "{}"})
	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	a := &app{store: s, poolManager: pm, toolMuxer: tm}

	pool, id := findAvailableBackend(a, "user1", "")
	if pool != nil || id != "" {
		t.Errorf("expected nil pool for disabled backend, got pool=%v id=%q", pool, id)
	}
}

// ---------- getBackendIDForRequest tests ----------

func TestGetBackendIDForRequest_NonDefault(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	a := &app{store: s, config: &config.InternalConfig{}}
	result := a.getBackendIDForRequest("mybackend")
	if result != "mybackend" {
		t.Errorf("expected 'mybackend', got %q", result)
	}
}

func TestGetBackendIDForRequest_DefaultNoBackends(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	a := &app{store: s, config: &config.InternalConfig{}}
	result := a.getBackendIDForRequest("default")
	if result != "mcpbridge" {
		t.Errorf("expected 'mcpbridge', got %q", result)
	}
}

func TestGetBackendIDForRequest_DefaultWithMCPBridge(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "mcpbridge", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	a := &app{store: s, config: &config.InternalConfig{}}
	result := a.getBackendIDForRequest("default")
	if result != "mcpbridge" {
		t.Errorf("expected 'mcpbridge', got %q", result)
	}
}

// ---------- seedBackendsFromConfig tests ----------

func TestSeedBackendsFromConfig_Empty(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	cfg := &config.InternalConfig{Backends: map[string]config.BackendConfig{}}
	seedBackendsFromConfig(s, cfg)

	backends, err := s.ListBackends()
	if err != nil {
		t.Fatalf("ListBackends: %v", err)
	}
	if len(backends) != 1 {
		t.Errorf("expected 1 backend (mcpbridge built-in), got %d", len(backends))
	}
	if backends[0].ID != "mcpbridge" {
		t.Errorf("expected mcpbridge backend, got %q", backends[0].ID)
	}
}

func TestSeedBackendsFromConfig_WithBackends(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	enabled := true
	cfg := &config.InternalConfig{
		Backends: map[string]config.BackendConfig{
			"testbe": {
				Command:    "echo",
				PoolSize:   2,
				ToolPrefix: "t_",
				Enabled:    &enabled,
			},
		},
	}
	seedBackendsFromConfig(s, cfg)

	backends, _ := s.ListBackends()
	found := false
	for _, b := range backends {
		if b.ID == "testbe" {
			found = true
			if b.ToolPrefix != "t_" {
				t.Errorf("expected prefix 't_', got %q", b.ToolPrefix)
			}
			if b.PoolSize != 2 {
				t.Errorf("expected PoolSize 2, got %d", b.PoolSize)
			}
		}
	}
	if !found {
		t.Error("expected testbe backend to be seeded")
	}
}

func TestSeedBackendsFromConfig_ExistingBackends(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "existing", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})

	cfg := &config.InternalConfig{
		Backends: map[string]config.BackendConfig{
			"newbe": {Command: "cat", PoolSize: 1},
		},
	}
	seedBackendsFromConfig(s, cfg)

	backends, _ := s.ListBackends()
	if len(backends) != 1 {
		t.Errorf("expected 1 existing backend, got %d", len(backends))
	}
}

func TestSeedBackendsFromConfig_DisabledBackend(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	disabled := false
	cfg := &config.InternalConfig{
		Backends: map[string]config.BackendConfig{
			"disabledbe": {
				Command:  "echo",
				PoolSize: 1,
				Enabled:  &disabled,
			},
		},
	}
	seedBackendsFromConfig(s, cfg)

	backends, _ := s.ListBackends()
	for _, b := range backends {
		if b.ID == "disabledbe" && b.Enabled {
			t.Error("expected disabledbe to be disabled")
		}
	}
}

// ---------- defaultBackendID tests ----------

func TestDefaultBackendID_WithBackends(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "be1", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	s.CreateBackend(&store.Backend{ID: "be2", Enabled: false, Command: "echo", PoolSize: 1, Env: "{}"})

	a := &app{store: s, config: &config.InternalConfig{}}
	id := a.defaultBackendID()
	if id != "be1" {
		t.Errorf("expected 'be1', got %q", id)
	}
}

func TestDefaultBackendID_NoEnabled(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "be1", Enabled: false, Command: "echo", PoolSize: 1, Env: "{}"})

	a := &app{store: s, config: &config.InternalConfig{}}
	id := a.defaultBackendID()
	if id != "default" {
		t.Errorf("expected 'default', got %q", id)
	}
}

func TestDefaultBackendID_NoBackends(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	a := &app{store: s, config: &config.InternalConfig{}}
	id := a.defaultBackendID()
	if id != "default" {
		t.Errorf("expected 'default', got %q", id)
	}
}

func TestDefaultBackendID_FallsBackToConfig(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	a := &app{
		store:  s,
		config: &config.InternalConfig{Backends: map[string]config.BackendConfig{"cfgbe": {Command: "echo", PoolSize: 1}}},
	}
	id := a.defaultBackendID()
	if id != "cfgbe" {
		t.Errorf("expected 'cfgbe' from config fallback, got %q", id)
	}
}

// ---------- jsonRPCError test ----------

func TestJsonRPCError(t *testing.T) {
	err := jsonRPCError("req1", -32000, "something went wrong", nil)
	errMap, ok := err["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error map")
	}
	code, ok := errMap["code"].(int)
	if !ok {
		t.Fatalf("expected int code, got %T", errMap["code"])
	}
	if code != -32000 {
		t.Errorf("expected code -32000, got %d", code)
	}
	if errMap["message"] != "something went wrong" {
		t.Errorf("expected message, got %v", errMap["message"])
	}
	if err["id"] != "req1" {
		t.Errorf("expected id req1, got %v", err["id"])
	}
}

func TestJsonRPCError_WithData(t *testing.T) {
	err := jsonRPCError(nil, -32002, "error with data", map[string]interface{}{"key": "val"})
	errMap := err["error"].(map[string]interface{})
	data := errMap["data"].(map[string]interface{})
	if data["key"] != "val" {
		t.Errorf("expected data.key=val, got %v", data["key"])
	}
}

func TestJsonRPCError_NilID(t *testing.T) {
	err := jsonRPCError(nil, -32602, "no id", nil)
	if _, exists := err["id"]; exists {
		t.Error("expected no id field")
	}
}

// ---------- v2 handleToolsList tests (cached path) ----------

func TestHandleToolsList_Cached(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}", ToolPrefix: "t", SkipJustification: true})
	s.SetBackendCapabilities("testbe", []map[string]interface{}{
		{"name": "tool1", "description": "first tool"},
		{"name": "tool2", "description": "second tool"},
	})
	s.CreateBackend(&store.Backend{ID: "disabledbe", Enabled: false, Command: "echo", PoolSize: 1, Env: "{}"})

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, config: &config.InternalConfig{}}, tm)

	body := []byte(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	srv.handleToolsList(w, r, "testuser", body, 1)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	tools, ok := result["result"].(map[string]interface{})["tools"].([]interface{})
	if !ok {
		t.Fatal("expected tools array")
	}
	// Should have 2 prefixed tools + system tools
	if len(tools) < 2 {
		t.Errorf("expected at least 2 tools, got %d", len(tools))
	}

	foundPrefixed := false
	foundSystem := false
	for _, tif := range tools {
		tool := tif.(map[string]interface{})
		name, _ := tool["name"].(string)
		if name == "t_tool1" {
			foundPrefixed = true
		}
		if name == "mcpbridge_ping" {
			foundSystem = true
		}
	}
	if !foundPrefixed {
		t.Error("expected prefixed tool t_tool1")
	}
	if !foundSystem {
		t.Error("expected system tool mcpbridge_ping")
	}
}

// ---------- v2 handleToolsCall tests (enforcer early-return paths) ----------

func TestHandleToolsCall_Denied(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.AddPolicy(enforcer.PolicyRow{
		ID: "test_deny", Name: "test deny", Expression: "tool.contains('delete')",
		Action: "DENY", Severity: "HIGH", Message: "delete blocked",
		Enabled: true, Priority: 100,
	})

	s.CreateBackend(&store.Backend{ID: "backend1", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, enforcer: enf}, tm)

	body := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"backend1_delete_stuff","arguments":{}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	srv.handleToolsCall(w, r, "testuser", body, 1)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for denied call (JSON-RPC error body), got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if _, hasError := result["error"]; !hasError {
		t.Error("expected error in response for denied tool")
	}
}

// ---------- MCPBridgeServer Handler() test ----------

func TestMCPBridgeServerHandler_RejectsUnauthenticated(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm}, tm)

	handler := srv.Handler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	handler.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", resp.StatusCode)
	}
}

func TestMCPBridgeServerHandler_ToolsListWithUserID(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}", SkipJustification: true})
	s.SetBackendCapabilities("testbe", []map[string]interface{}{
		{"name": "tool1", "description": "first"},
	})

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, config: &config.InternalConfig{}}, tm)

	handler := srv.Handler()
	body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	// Simulate auth middleware setting userID in context (using typed key)
	ctx := context.WithValue(r.Context(), auth.UserIDKey, "testuser")
	r = r.WithContext(ctx)
	handler.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMCPBridgeServerHandler_SystemToolCall(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{ID: "testbe", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})

	pm := poolmgr.NewPoolManager("echo", 1)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, config: &config.InternalConfig{}}, tm)

	handler := srv.Handler()
	body := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"mcpbridge_ping","arguments":{}}}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), auth.UserIDKey, "testuser")
	r = r.WithContext(ctx)
	handler.ServeHTTP(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for system tool, got %d", resp.StatusCode)
	}
}

// ---------- Additional getPoolForUser tests ----------

func TestGetPoolForUser_FallbackToDefaults(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	a := &app{store: s, poolManager: pm, toolMuxer: tm, config: &config.InternalConfig{}}

	pool := a.getPoolForUser("user1", "nonexistent")
	if pool == nil {
		t.Error("expected non-nil pool even for nonexistent backend (falls back to defaults)")
	}
}

func TestGetPoolForUser_FromConfig(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{
		Backends: map[string]config.BackendConfig{
			"cfg_be": {Command: "echo", PoolSize: 1},
		},
	})
	a := &app{store: s, poolManager: pm, toolMuxer: tm, config: &config.InternalConfig{
		Backends: map[string]config.BackendConfig{
			"cfg_be": {Command: "echo", PoolSize: 1},
		},
	}}

	pool := a.getPoolForUser("user1", "cfg_be")
	if pool == nil {
		t.Error("expected non-nil pool from config backend")
	}
}

// ---------- HandleToolsCall with pending approval paths ----------

func TestHandleToolsCall_PendingAdminApproval(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateUser(&store.User{ID: "testuser", Name: "Test", Email: "t@t.com", Password: "pw", Role: "user"})

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.AddPolicy(enforcer.PolicyRow{
		ID: "test_admin_approval", Name: "admin approval", Expression: "tool.contains('delete')",
		Action: "PENDING_ADMIN_APPROVAL", Severity: "HIGH", Message: "needs admin approval",
		Enabled: true, Priority: 100,
	})

	s.CreateBackend(&store.Backend{ID: "backend1", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, enforcer: enf}, tm)

	body := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"backend1_delete_stuff","arguments":{}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	srv.handleToolsCall(w, r, "testuser", body, 1)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if _, hasResult := result["result"]; !hasResult {
		t.Error("expected result with approval details")
	}
	if resp.Header.Get("X-Enforcer-Status") != "pending_approval" {
		t.Errorf("expected X-Enforcer-Status=pending_approval, got %q", resp.Header.Get("X-Enforcer-Status"))
	}
}

func TestHandleToolsCall_PendingUserApproval(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	s.CreateUser(&store.User{ID: "testuser", Name: "Test", Email: "t@t.com", Password: "pw", Role: "user"})

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.AddPolicy(enforcer.PolicyRow{
		ID: "test_user_approval", Name: "user approval", Expression: "tool.contains('delete')",
		Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Message: "needs your approval",
		Enabled: true, Priority: 100,
	})

	s.CreateBackend(&store.Backend{ID: "backend1", Enabled: true, Command: "echo", PoolSize: 1, Env: "{}"})
	pm := poolmgr.NewPoolManager("echo", 0)
	tm := muxer.NewToolMuxerWithStore(pm, s, &config.InternalConfig{})
	srv := NewMCPBridgeServer(&app{store: s, poolManager: pm, toolMuxer: tm, enforcer: enf}, tm)

	body := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"backend1_delete_stuff","arguments":{}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	srv.handleToolsCall(w, r, "testuser", body, 1)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if _, hasResult := result["result"]; !hasResult {
		t.Error("expected result with user approval details")
	}
	if resp.Header.Get("X-Enforcer-Status") != "pending_user_approval" {
		t.Errorf("expected X-Enforcer-Status=pending_user_approval, got %q", resp.Header.Get("X-Enforcer-Status"))
	}
}
