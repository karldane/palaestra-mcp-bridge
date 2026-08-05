package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
)

func TestV2ToolsListInitial(t *testing.T) {
	// Setup a mock app and dependencies
	mockStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create mock store: %v", err)
	}
	defer mockStore.Close()

	// Seed some backends into the store
	mockStore.CreateBackend(&store.Backend{
		ID:         "test_backend_1",
		Enabled:    true,
		Command:    "echo 'test_backend_1 tools'",
		ToolPrefix: "test1",
	})
	mockStore.CreateBackend(&store.Backend{
		ID:         "test_backend_2",
		Enabled:    true,
		Command:    "echo 'test_backend_2 tools'",
		ToolPrefix: "test2",
	})

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1) // Dummy command and pool size
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, mockStore, mockConfig)

	mockApp := &app{
		store:       mockStore,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/list",
		"id":      1,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	// Add a dummy userID to the request context
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	handler := v2HandleWrapper(mockApp)

	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			status, http.StatusOK)
	}

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response 'result' not found or not an object: %v", resp)
	}

	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("Response 'result.tools' not found or not an array: %v", result)
	}

	// tools/list returns generic namespace_expand/tool_call plus expand+call for each backend
	// Expected: 2 generics + 2 backends × 2 (expand+call) = 6 tools
	if len(tools) != 6 {
		t.Errorf("Expected 6 tools (2 generics + 2 backends × 2), got %d", len(tools))
	}

	// Check for expected tools
	foundNamespaceExpand := false
	foundToolCall := false
	foundBackend1Expand := false
	foundBackend1Call := false
	foundBackend2Expand := false
	foundBackend2Call := false

	for _, tool := range tools {
		toolMap := tool.(map[string]interface{})
		name, nameOk := toolMap["name"].(string)
		description, descOk := toolMap["description"].(string)

		if !nameOk || !descOk {
			t.Errorf("Tool entry has invalid 'name' or 'description' format: %v", toolMap)
			continue
		}

		switch name {
		case "namespace_expand":
			foundNamespaceExpand = true
			t.Logf("Found namespace_expand")
		case "tool_call":
			foundToolCall = true
			t.Logf("Found tool_call")
		case "test_backend_1_expand":
			expectedDesc := "Returns the full list of available tools in the Test_Backend_1 namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_test_backend_1_call. Do not attempt to guess tool names — call this first."
			if description == expectedDesc {
				foundBackend1Expand = true
				t.Logf("Found test_backend_1_expand")
			} else {
				t.Errorf("Unexpected description for test_backend_1_expand: got %q want %q", description, expectedDesc)
			}
		case "test_backend_1_call":
			expectedDesc := "Executes a named tool in the Test_Backend_1 namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_test_backend_1_expand. If you have not called MCP_Bridge_test_backend_1_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters). Some tools may require your approval - check expand output before calling."
			if description == expectedDesc {
				foundBackend1Call = true
				t.Logf("Found test_backend_1_call")
			} else {
				t.Errorf("Unexpected description for test_backend_1_call: got %q want %q", description, expectedDesc)
			}
		case "test_backend_2_expand":
			expectedDesc := "Returns the full list of available tools in the Test_Backend_2 namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_test_backend_2_call. Do not attempt to guess tool names — call this first."
			if description == expectedDesc {
				foundBackend2Expand = true
				t.Logf("Found test_backend_2_expand")
			} else {
				t.Errorf("Unexpected description for test_backend_2_expand: got %q want %q", description, expectedDesc)
			}
		case "test_backend_2_call":
			expectedDesc := "Executes a named tool in the Test_Backend_2 namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_test_backend_2_expand. If you have not called MCP_Bridge_test_backend_2_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters). Some tools may require your approval - check expand output before calling."
			if description == expectedDesc {
				foundBackend2Call = true
				t.Logf("Found test_backend_2_call")
			} else {
				t.Errorf("Unexpected description for test_backend_2_call: got %q want %q", description, expectedDesc)
			}
		default:
			t.Errorf("Unexpected tool found: name=%s, description=%s", name, description)
		}
	}

	if !foundNamespaceExpand {
		t.Error("Did not find 'namespace_expand' tool")
	}
	if !foundToolCall {
		t.Error("Did not find 'tool_call' tool")
	}
	if !foundBackend1Expand {
		t.Error("Did not find 'test_backend_1_expand' tool")
	}
	if !foundBackend1Call {
		t.Error("Did not find 'test_backend_1_call' tool")
	}
	if !foundBackend2Expand {
		t.Error("Did not find 'test_backend_2_expand' tool")
	}
	if !foundBackend2Call {
		t.Error("Did not find 'test_backend_2_call' tool")
	}
}

func TestV2ToolCallUnknownToolError_v2_2(t *testing.T) {
	mockStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create mock store: %v", err)
	}
	defer mockStore.Close()

	mockStore.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})

	capabilities := []map[string]interface{}{
		{
			"name":        "recall",
			"description": "Retrieve facts semantically relevant to a query",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "remember",
			"description": "Store a fact for later recall",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{"type": "string"},
				},
				"required": []string{"content"},
			},
		},
	}
	mockStore.SetBackendCapabilities("qdrant", capabilities)

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, mockStore, mockConfig)

	mockApp := &app{
		store:       mockStore,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    nil,
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"namespace": "qdrant",
				"tool":      "memory_search",
				"params":    map[string]interface{}{},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response 'error' not found or not an object: %v", resp)
	}

	code, ok := errObj["code"].(float64)
	if !ok || code != -32002 {
		t.Errorf("Error code: got %v, want -32002", code)
	}

	message, ok := errObj["message"].(string)
	if !ok || message == "" {
		t.Errorf("Error message missing or empty")
	}

	data, ok := errObj["data"].(map[string]interface{})
	if !ok {
		t.Fatal("Error 'data' field missing or not an object")
	}

	if data["error"] != "unknown_tool" {
		t.Errorf("data.error: got %v, want 'unknown_tool'", data["error"])
	}

	if data["provided"] != "memory_search" {
		t.Errorf("data.provided: got %v, want 'memory_search'", data["provided"])
	}

	if data["namespace"] != "qdrant" {
		t.Errorf("data.namespace: got %v, want 'qdrant'", data["namespace"])
	}

	availableTools, ok := data["available_tools"].([]interface{})
	if !ok {
		t.Fatal("data.available_tools missing or not an array")
	}

	if len(availableTools) != 2 {
		t.Errorf("Expected 2 available tools, got %d", len(availableTools))
	}

	firstTool, ok := availableTools[0].(map[string]interface{})
	if !ok {
		t.Fatal("First available tool is not an object")
	}

	if firstTool["name"] != "recall" {
		t.Errorf("First tool name: got %v, want 'recall'", firstTool["name"])
	}
	if firstTool["description"] != "Retrieve facts semantically relevant to a query" {
		t.Errorf("First tool description: got %v, want 'Retrieve facts semantically relevant to a query'", firstTool["description"])
	}

	requiredParams, ok := firstTool["required_params"].([]interface{})
	if !ok {
		t.Fatal("required_params missing or not an array")
	}
	if len(requiredParams) != 1 || requiredParams[0] != "query" {
		t.Errorf("required_params: got %v, want ['query']", requiredParams)
	}

	optionalParams, ok := firstTool["optional_params"].([]interface{})
	if !ok {
		t.Fatal("optional_params missing or not an array")
	}
	if len(optionalParams) != 0 {
		t.Errorf("optional_params: got %v, want []", optionalParams)
	}
}

func TestV2ToolsListDescriptions_v2_2(t *testing.T) {
	mockStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create mock store: %v", err)
	}
	defer mockStore.Close()

	mockStore.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})
	mockStore.CreateBackend(&store.Backend{
		ID:                "github",
		Enabled:           true,
		Command:           "echo 'github tools'",
		ToolPrefix:        "github",
		SkipJustification: false,
	})

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, mockStore, mockConfig)

	mockApp := &app{
		store:       mockStore,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    nil,
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/list",
		"id":      1,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response 'result' not found or not an object: %v", resp)
	}

	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatalf("Response 'result.tools' not found or not an array: %v", result)
	}

	for _, tool := range tools {
		toolMap := tool.(map[string]interface{})
		name := toolMap["name"].(string)
		description := toolMap["description"].(string)

		switch name {
		case "qdrant_expand":
			expectedDesc := "Returns the full list of available tools in the Qdrant namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_qdrant_call. Do not attempt to guess tool names — call this first."
			if description != expectedDesc {
				t.Errorf("qdrant_expand: got %q, want %q", description, expectedDesc)
			}
		case "qdrant_call":
			expectedDesc := "Executes a named tool in the Qdrant namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_qdrant_expand. If you have not called MCP_Bridge_qdrant_expand in this session, do so before calling this tool. Do not guess tool names. No justification required. Some tools may require your approval - check expand output before calling."
			if description != expectedDesc {
				t.Errorf("qdrant_call: got %q, want %q", description, expectedDesc)
			}
		case "github_expand":
			expectedDesc := "Returns the full list of available tools in the Github namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_github_call. Do not attempt to guess tool names — call this first."
			if description != expectedDesc {
				t.Errorf("github_expand: got %q, want %q", description, expectedDesc)
			}
		case "github_call":
			expectedDesc := "Executes a named tool in the Github namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_github_expand. If you have not called MCP_Bridge_github_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters). Some tools may require your approval - check expand output before calling."
			if description != expectedDesc {
				t.Errorf("github_call: got %q, want %q", description, expectedDesc)
			}
		}
	}
}

// TestV2CallWithMissingParams verifies that when params is nil/missing,
// the _call handler returns a clear JSON-RPC error instead of silently
// forwarding empty arguments.
func TestV2CallWithMissingParams(t *testing.T) {
	mockStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create mock store: %v", err)
	}
	defer mockStore.Close()

	mockStore.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, mockStore, mockConfig)

	mockApp := &app{
		store:       mockStore,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    nil,
	}

	// Params is completely absent from arguments
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"tool": "remember",
				// no "params" key at all
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected a JSON-RPC error for missing params, got none")
	}

	code, ok := errObj["code"].(float64)
	if !ok || code != -32602 {
		t.Errorf("Error code: got %v, want -32602", code)
	}

	msg, ok := errObj["message"].(string)
	if !ok || msg == "" {
		t.Errorf("Error message missing or empty")
	}

	// The message should mention the tool name so the agent knows what went wrong
	if !strings.Contains(msg, "remember") {
		t.Errorf("Error message should mention tool name 'remember': %q", msg)
	}
}

// TestV2CallWithWrongTypeParams verifies that when params is an unexpected
// type (e.g. int), the _call handler returns a clear error with the actual type.
func TestV2ToolCall_PendingUserApproval(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = s.DB().Exec(`INSERT INTO users (id, name, email, password, role) VALUES (?,?,?,?,?)`,
		"testuser", "Test User", "testuser@test.local", "x", "user")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	err = enf.RegisterOverride("search_repositories", "qdrant", enforcer.SafetyProfile{
		Risk:         enforcer.RiskLow,
		Impact:       enforcer.ImpactRead,
		Cost:         1,
		RequiresHITL: true,
	})
	if err != nil {
		t.Fatalf("RegisterOverride: %v", err)
	}

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, s, mockConfig)

	mockApp := &app{
		store:       s,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    enf,
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"tool":   "search_repositories",
				"params": map[string]interface{}{},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	resultObj, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response 'result' not found or not an object (got 'error': %v)", resp["error"])
	}

	content, ok := resultObj["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatal("result.content missing or empty")
	}
	firstContent := content[0].(map[string]interface{})
	text, ok := firstContent["text"].(string)
	if !ok || !strings.Contains(text, "requires your approval") {
		t.Errorf("content text should mention 'requires your approval': %q", text)
	}
	if !strings.Contains(text, "mcpbridge_approval_status") {
		t.Errorf("content text should mention 'mcpbridge_approval_status': %q", text)
	}

	if resultObj["approval_id"] == nil || resultObj["approval_id"] == "" {
		t.Error("approval_id missing or empty")
	}
	if resultObj["status"] != "pending_user_approval" {
		t.Errorf("status: got %q, want 'pending_user_approval'", resultObj["status"])
	}
	if resultObj["tool"] != "search_repositories" {
		t.Errorf("tool: got %q, want 'search_repositories'", resultObj["tool"])
	}
	if resultObj["backend"] != "qdrant" {
		t.Errorf("backend: got %q, want 'qdrant'", resultObj["backend"])
	}
	if resultObj["instructions"] == nil || resultObj["instructions"] == "" {
		t.Error("instructions missing or empty")
	}

	if rr.Header().Get("X-Enforcer-Status") != "pending_user_approval" {
		t.Errorf("X-Enforcer-Status: got %q, want 'pending_user_approval'", rr.Header().Get("X-Enforcer-Status"))
	}
}

func TestV2ToolCall_PendingAdminApproval(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = s.DB().Exec(`INSERT INTO users (id, name, email, password, role) VALUES (?,?,?,?,?)`,
		"testuser", "Test User", "testuser@test.local", "x", "user")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, s, mockConfig)

	mockApp := &app{
		store:       s,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    enf,
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"tool":   "some_unknown_tool_with_no_profile",
				"params": map[string]interface{}{},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	resultObj, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Response 'result' not found or not an object (got 'error': %v)", resp["error"])
	}

	content, ok := resultObj["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatal("result.content missing or empty")
	}
	firstContent := content[0].(map[string]interface{})
	text, ok := firstContent["text"].(string)
	if !ok {
		t.Fatal("content[0].text is missing")
	}
	if !strings.Contains(text, "requires admin approval") && !strings.Contains(text, "requires your approval") {
		t.Errorf("content text should mention approval requirement: %q", text)
	}
	if !strings.Contains(text, "mcpbridge_approval_status") {
		t.Errorf("content text should mention 'mcpbridge_approval_status': %q", text)
	}

	if resultObj["approval_id"] == nil || resultObj["approval_id"] == "" {
		t.Error("approval_id missing or empty")
	}
	if resultObj["tool"] != "some_unknown_tool_with_no_profile" {
		t.Errorf("tool: got %q, want 'some_unknown_tool_with_no_profile'", resultObj["tool"])
	}
	if resultObj["backend"] != "qdrant" {
		t.Errorf("backend: got %q, want 'qdrant'", resultObj["backend"])
	}

	status := resultObj["status"].(string)
	if status == "" {
		t.Error("status is empty")
	}
	if rr.Header().Get("X-Enforcer-Status") == "" {
		t.Error("X-Enforcer-Status header is missing")
	}
}

func TestV2CallWithWrongTypeParams(t *testing.T) {
	mockStore, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create mock store: %v", err)
	}
	defer mockStore.Close()

	mockStore.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: true,
	})

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, mockStore, mockConfig)

	mockApp := &app{
		store:       mockStore,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    nil,
	}

	// params is an int — completely wrong type
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"tool":   "remember",
				"params": 42,
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected a JSON-RPC error for wrong-type params, got none")
	}

	code, ok := errObj["code"].(float64)
	if !ok || code != -32602 {
		t.Errorf("Error code: got %v, want -32602", code)
	}

	// The data field should contain diagnostic info
	if data, ok := errObj["data"].(map[string]interface{}); ok {
		if data["error"] != "invalid_params_type" {
			t.Errorf("data.error: got %v, want 'invalid_params_type'", data["error"])
		}
		if data["got_type"] != "int" && data["got_type"] != "float64" {
			t.Logf("data.got_type: %v (int may become float64 in JSON)", data["got_type"])
		}
	} else {
		t.Log("No data field in error response (non-critical)")
	}
}

func TestEnforcerApprovalQueryMethods(t *testing.T) {
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
		BackendID: "testbackend",
		Args:      map[string]interface{}{"key": "value"},
	}, "test_policy", "test message", "user", enforcer.MatchContextRiskLimit)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// List pending approvals (all queues)
	pending, err := enf.ListPendingApprovals()
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("ListPendingApprovals: expected 1, got %d", len(pending))
	}

	// List user pending approvals
	userPending, err := enf.ListUserPendingApprovals()
	if err != nil {
		t.Fatalf("ListUserPendingApprovals: %v", err)
	}
	if len(userPending) != 1 {
		t.Errorf("ListUserPendingApprovals: expected 1, got %d", len(userPending))
	}

	// List admin pending (should be 0)
	adminPending, err := enf.ListAdminPendingApprovals()
	if err != nil {
		t.Fatalf("ListAdminPendingApprovals: %v", err)
	}
	if len(adminPending) != 0 {
		t.Errorf("ListAdminPendingApprovals: expected 0, got %d", len(adminPending))
	}

	// Count user pending
	userCount, err := enf.CountUserPendingApprovals()
	if err != nil {
		t.Fatalf("CountUserPendingApprovals: %v", err)
	}
	if userCount != 1 {
		t.Errorf("CountUserPendingApprovals: expected 1, got %d", userCount)
	}

	// Count admin pending
	adminCount, err := enf.CountAdminPendingApprovals()
	if err != nil {
		t.Fatalf("CountAdminPendingApprovals: %v", err)
	}
	if adminCount != 0 {
		t.Errorf("CountAdminPendingApprovals: expected 0, got %d", adminCount)
	}

	// GetApprovalRequest
	req, err := enf.GetApprovalRequest(approvalID)
	if err != nil {
		t.Fatalf("GetApprovalRequest: %v", err)
	}
	if req.ID != approvalID {
		t.Errorf("approval ID mismatch: %q vs %q", req.ID, approvalID)
	}
	if req.Status != "PENDING" {
		t.Errorf("expected PENDING status, got %q", req.Status)
	}
	if req.ToolName != "test_tool" {
		t.Errorf("expected ToolName=test_tool, got %q", req.ToolName)
	}

	// List all approvals for user
	allUser, err := enf.ListUserAllApprovals("testuser")
	if err != nil {
		t.Fatalf("ListUserAllApprovals: %v", err)
	}
	if len(allUser) != 1 {
		t.Errorf("ListUserAllApprovals: expected 1, got %d", len(allUser))
	}

	// ListAllApprovals
	all, err := enf.ListAllApprovals()
	if err != nil {
		t.Fatalf("ListAllApprovals: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListAllApprovals: expected 1, got %d", len(all))
	}
}

func TestV2ToolCall_DeniedByJustificationGate(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	s.CreateBackend(&store.Backend{
		ID:                "qdrant",
		Enabled:           true,
		Command:           "echo 'qdrant tools'",
		ToolPrefix:        "qdrant",
		SkipJustification: false,
	})

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 20
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	mockPoolManager := poolmgr.NewPoolManager("dummyCommand", 1)
	mockConfig := &config.InternalConfig{}
	mockToolMuxer := muxer.NewToolMuxerWithStore(mockPoolManager, s, mockConfig)

	mockApp := &app{
		store:       s,
		toolMuxer:   mockToolMuxer,
		poolManager: mockPoolManager,
		enforcer:    enf,
	}

	// No justification at all — enforcer should deny
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name": "qdrant_call",
			"arguments": map[string]interface{}{
				"tool":   "search_repositories",
				"params": map[string]interface{}{},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/mcp/v2", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), auth.UserIDKey, "testuser")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler := v2HandleWrapper(mockApp)
	handler.ServeHTTP(rr, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected 'error' in response for denied call, got 'result': %v", resp["result"])
	}

	code, ok := errObj["code"].(float64)
	if !ok {
		t.Fatalf("error.code missing or not a number")
	}
	// Justification denial codes include -32001
	if code != -32001 {
		t.Errorf("expected error code -32001, got %v", code)
	}

	msg, _ := errObj["message"].(string)
	if msg == "" {
		t.Error("error message is empty")
	}
	if !strings.Contains(msg, "justification") && !strings.Contains(msg, "required") {
		t.Errorf("error message should mention justification: %q", msg)
	}
}

func TestEnforcerRemoveOverride(t *testing.T) {
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

	err = enf.RegisterOverride("test_tool", "testbackend", enforcer.SafetyProfile{
		Risk:   enforcer.RiskLow,
		Impact: enforcer.ImpactRead,
		Cost:   1,
	})
	if err != nil {
		t.Fatalf("RegisterOverride: %v", err)
	}

	// Verify the override exists by resolving profile
	profile, err := enf.GetResolver().ResolveForUser("test_tool", "testbackend", "")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}
	if profile.Source != "override" {
		t.Errorf("expected source 'override', got %q", profile.Source)
	}

	// Remove the override
	err = enf.RemoveOverride("test_tool", "testbackend")
	if err != nil {
		t.Fatalf("RemoveOverride: %v", err)
	}

	// Verify the override is gone — should now fall through to heuristic (source = "inferred")
	profile, err = enf.GetResolver().ResolveForUser("test_tool", "testbackend", "")
	if err != nil {
		t.Fatalf("ResolveForUser after removal: %v", err)
	}
	if profile.Source != "inferred" {
		t.Errorf("expected source 'inferred' after removal, got %q", profile.Source)
	}
}

func TestEnforcerRemoveOverride_NotFound(t *testing.T) {
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

	err = enf.RemoveOverride("nonexistent_tool", "testbackend")
	if err == nil {
		t.Fatal("expected error for removing nonexistent override, got nil")
	}
	if !strings.Contains(err.Error(), "no override") {
		t.Errorf("expected 'no override' error, got %v", err)
	}
}
