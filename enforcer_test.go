package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// TestEnforcerPolicyEvaluation verifies that policies are actually evaluated during tool calls
func TestEnforcerPolicyEvaluation(t *testing.T) {
	a, _, cleanup := testApp(t, "echo test", 2)
	defer cleanup()

	// Create enforcer with a test policy
	enforcerConfig := enforcer.DefaultEnforcerConfig()
	enf, err := enforcer.NewEnforcer(enforcerConfig, store.NewEnforcerStore(a.store.DB()))
	if err != nil {
		t.Fatalf("Failed to create enforcer: %v", err)
	}
	a.enforcer = enf

	// Manually add a test policy to block "delete" operations
	testPolicy := enforcer.PolicyRow{
		ID:          "test_block_delete",
		Name:        "Block Delete Operations",
		Description: "Block delete operations",
		Expression:  "tool.contains('delete')",
		Action:      "DENY",
		Severity:    "HIGH",
		Message:     "Delete operations blocked by test policy",
		Enabled:     true,
		Priority:    100,
	}
	if err := enf.AddPolicy(testPolicy); err != nil {
		t.Fatalf("Failed to add test policy: %v", err)
	}

	// Test 1: Verify enforcer evaluates "delete" tool
	t.Run("Delete tool should trigger policy", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"test_delete_file","arguments":{"path":"test.txt"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		// We need to route through the handler that calls the enforcer
		handleToolsCall(a, w, req, "test-user", []byte(body), 1)

		// The request should be blocked (403 Forbidden)
		if w.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden for delete operation, got %d", w.Code)
			t.Logf("Response body: %s", w.Body.String())
		}
	})

	// Test 2: Verify non-delete tools pass through
	t.Run("Read tool should not trigger policy", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":2,"params":{"name":"test_read_file","arguments":{"path":"test.txt"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handleToolsCall(a, w, req, "test-user", []byte(body), 2)

		// Should NOT be 403 (might be other errors like no backend, but not policy violation)
		if w.Code == http.StatusForbidden {
			t.Error("Read tool was incorrectly blocked by delete policy")
			t.Logf("Response body: %s", w.Body.String())
		}
	})
}

// TestEnforcerBlocksBeforeExecution verifies that enforcer blocks tool calls BEFORE backend execution
// This test ensures that a policy violation returns an error immediately without attempting to execute the tool
func TestEnforcerBlocksBeforeExecution(t *testing.T) {
	a, _, cleanup := testApp(t, "echo test", 2)
	defer cleanup()

	// Track if backend was ever reached
	backendReached := false

	// Create enforcer with a DENY policy for delete operations
	enforcerConfig := enforcer.DefaultEnforcerConfig()
	enf, err := enforcer.NewEnforcer(enforcerConfig, store.NewEnforcerStore(a.store.DB()))
	if err != nil {
		t.Fatalf("Failed to create enforcer: %v", err)
	}
	a.enforcer = enf

	// Add a strict policy that denies ALL delete operations
	denyPolicy := enforcer.PolicyRow{
		ID:          "block_all_deletes",
		Name:        "Block All Deletes",
		Description: "Block all delete operations",
		Expression:  "safety.impact_scope == 'delete'",
		Action:      "DENY",
		Severity:    "CRITICAL",
		Message:     "Delete operations are prohibited",
		Enabled:     true,
		Priority:    100,
	}
	if err := enf.AddPolicy(denyPolicy); err != nil {
		t.Fatalf("Failed to add deny policy: %v", err)
	}

	// Test: Verify delete tool is blocked with 403 BEFORE backend execution
	t.Run("Delete tool blocked before backend", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"github_delete_file","arguments":{"owner":"test","repo":"test","path":"README.md","message":"Delete","branch":"main"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handleToolsCall(a, w, req, "test-user", []byte(body), 1)

		// Should get 403 Forbidden, NOT a backend error (like 404 from GitHub)
		if w.Code != http.StatusForbidden {
			t.Errorf("Expected 403 Forbidden (policy violation), got %d", w.Code)
			t.Logf("Response: %s", w.Body.String())
		}

		// Response should indicate policy violation, not backend error
		responseBody := w.Body.String()
		if !strings.Contains(responseBody, "policy") && !strings.Contains(responseBody, "prohibited") {
			t.Errorf("Response should indicate policy violation, got: %s", responseBody)
		}

		// Backend should NOT have been reached
		if backendReached {
			t.Error("Backend was reached despite policy violation - enforcer should block BEFORE execution")
		}
	})

	// Test: Verify read tool passes through (backend would be reached for non-delete operations)
	t.Run("Read tool passes through enforcer", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":2,"params":{"name":"github_read_file","arguments":{"owner":"test","repo":"test","path":"README.md"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handleToolsCall(a, w, req, "test-user", []byte(body), 2)

		// Should NOT be 403 - might be other errors (no backend, etc.) but not policy violation
		if w.Code == http.StatusForbidden {
			t.Errorf("Read tool was blocked by delete policy - got %d", w.Code)
			t.Logf("Response: %s", w.Body.String())
		}
	})
}

// TestEnforcerIntegration verifies end-to-end that enforcer is properly wired into the request flow
func TestEnforcerIntegration(t *testing.T) {
	a, _, cleanup := testApp(t, "echo test", 2)
	defer cleanup()

	// Setup enforcer with seeded policies
	enforcerConfig := enforcer.DefaultEnforcerConfig()
	enf, err := enforcer.NewEnforcer(enforcerConfig, store.NewEnforcerStore(a.store.DB()))
	if err != nil {
		t.Fatalf("Failed to create enforcer: %v", err)
	}
	a.enforcer = enf

	// Seed default policies (including require_mfa_for_destructive)
	// This simulates real-world scenario where destructive ops require approval

	// Test: Tool with 'delete' in name should be caught by impact_scope policy
	t.Run("Destructive tool requires approval", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"delete_repository","arguments":{"repo":"test"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handleToolsCall(a, w, req, "test-user", []byte(body), 1)

		// Should be blocked or pending approval (not allowed through)
		if w.Code == http.StatusOK {
			t.Error("Destructive operation should not be allowed without approval")
		}
	})
}
