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
			expectedDesc := "Expand Test_Backend_1 namespace to get available tools. No justification required. Call this before test_backend_1_call to discover tool names."
			if description == expectedDesc {
				foundBackend1Expand = true
				t.Logf("Found test_backend_1_expand")
			} else {
				t.Errorf("Unexpected description for test_backend_1_expand: got %q want %q", description, expectedDesc)
			}
		case "test_backend_1_call":
			expectedDesc := "Call a tool in the Test_Backend_1 namespace"
			if description == expectedDesc {
				foundBackend1Call = true
				t.Logf("Found test_backend_1_call")
			} else {
				t.Errorf("Unexpected description for test_backend_1_call: got %q want %q", description, expectedDesc)
			}
		case "test_backend_2_expand":
			expectedDesc := "Expand Test_Backend_2 namespace to get available tools. No justification required. Call this before test_backend_2_call to discover tool names."
			if description == expectedDesc {
				foundBackend2Expand = true
				t.Logf("Found test_backend_2_expand")
			} else {
				t.Errorf("Unexpected description for test_backend_2_expand: got %q want %q", description, expectedDesc)
			}
		case "test_backend_2_call":
			expectedDesc := "Call a tool in the Test_Backend_2 namespace"
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
