package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
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

	// For lazy-loading, tools/list returns expand + call tools for each enabled backend
	// Expected: 2 backends * 2 (expand+call) = 4 tools
	if len(tools) != 4 {
		t.Errorf("Expected 4 lazy-load tools (2 backends × 2), got %d", len(tools))
	}

	// Check for expected lazy-load tools
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
		case "test_backend_1_expand":
			expectedDesc := "Returns the full list of available tools in the Test_Backend_1 namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_test_backend_1_call. Do not attempt to guess tool names — call this first."
			if description == expectedDesc {
				foundBackend1Expand = true
				t.Logf("Found test_backend_1_expand")
			} else {
				t.Errorf("Unexpected description for test_backend_1_expand: got %q want %q", description, expectedDesc)
			}
		case "test_backend_1_call":
			expectedDesc := "Executes a named tool in the Test_Backend_1 namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_test_backend_1_expand. If you have not called MCP_Bridge_test_backend_1_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters)."
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
			expectedDesc := "Executes a named tool in the Test_Backend_2 namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_test_backend_2_expand. If you have not called MCP_Bridge_test_backend_2_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters)."
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
			expectedDesc := "Executes a named tool in the Qdrant namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_qdrant_expand. If you have not called MCP_Bridge_qdrant_expand in this session, do so before calling this tool. Do not guess tool names. No justification required."
			if description != expectedDesc {
				t.Errorf("qdrant_call: got %q, want %q", description, expectedDesc)
			}
		case "github_expand":
			expectedDesc := "Returns the full list of available tools in the Github namespace, including parameter names, types, and descriptions. You MUST call this tool before calling MCP_Bridge_github_call. Do not attempt to guess tool names — call this first."
			if description != expectedDesc {
				t.Errorf("github_expand: got %q, want %q", description, expectedDesc)
			}
		case "github_call":
			expectedDesc := "Executes a named tool in the Github namespace. The value of `tool` must exactly match a tool name returned by MCP_Bridge_github_expand. If you have not called MCP_Bridge_github_expand in this session, do so before calling this tool. Do not guess tool names. Justification required (minimum 40 characters)."
			if description != expectedDesc {
				t.Errorf("github_call: got %q, want %q", description, expectedDesc)
			}
		}
	}
}
