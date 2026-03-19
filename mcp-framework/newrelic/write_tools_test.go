package newrelic

import (
	"context"
	"testing"
)

func TestWriteToolsRegistered(t *testing.T) {
	server := NewServer("test-key")

	tools := server.ListTools()

	// Check that write tools are registered
	writeTools := []string{
		"acknowledge_alert_violation",
		"create_alert_condition",
		"add_dashboard_widget",
	}

	for _, toolName := range writeTools {
		found := false
		for _, registeredTool := range tools {
			if registeredTool == toolName {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected write tool '%s' to be registered", toolName)
		}
	}
}

func TestWriteToolsDisabledByDefault(t *testing.T) {
	server := NewServer("test-key") // writeEnabled defaults to false

	ctx := context.Background()

	// Try to execute a write tool
	_, err := server.ExecuteTool(ctx, "acknowledge_alert_violation", map[string]interface{}{
		"violation_id": "12345",
		"comment":      "Test acknowledgment",
	})

	if err == nil {
		t.Fatal("Expected error when executing write tool with writeEnabled=false")
	}

	expectedError := "Write tools are disabled. Enable with --write-enabled flag."
	if err.Error() != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, err.Error())
	}
}

func TestWriteToolsEnabled(t *testing.T) {
	server := NewServer("test-key", true) // writeEnabled = true

	ctx := context.Background()

	// Try to execute a write tool
	result, err := server.ExecuteTool(ctx, "acknowledge_alert_violation", map[string]interface{}{
		"violation_id": "12345",
		"comment":      "Test acknowledgment",
	})

	if err != nil {
		t.Fatalf("Expected no error when executing write tool with writeEnabled=true, got: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty result")
	}

	if !contains(result, "Acknowledged") {
		t.Errorf("Expected result to contain 'Acknowledged', got: %s", result)
	}
}

func TestCreateAlertConditionWriteDisabled(t *testing.T) {
	server := NewServer("test-key") // writeEnabled defaults to false

	ctx := context.Background()

	_, err := server.ExecuteTool(ctx, "create_alert_condition", map[string]interface{}{
		"policy_id":          "123",
		"name":               "Test Condition",
		"nrql_query":         "SELECT count(*) FROM Transaction",
		"critical_threshold": 10,
	})

	if err == nil {
		t.Fatal("Expected error when executing create_alert_condition with writeEnabled=false")
	}

	expectedError := "Write tools are disabled. Enable with --write-enabled flag."
	if err.Error() != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, err.Error())
	}
}

func TestCreateAlertConditionWriteEnabled(t *testing.T) {
	server := NewServer("test-key", true) // writeEnabled = true

	ctx := context.Background()

	result, err := server.ExecuteTool(ctx, "create_alert_condition", map[string]interface{}{
		"policy_id":          "123",
		"name":               "Test Condition",
		"nrql_query":         "SELECT count(*) FROM Transaction",
		"critical_threshold": 10,
	})

	if err != nil {
		t.Fatalf("Expected no error when executing create_alert_condition with writeEnabled=true, got: %v", err)
	}

	if !contains(result, "Created alert condition") {
		t.Errorf("Expected result to contain 'Created alert condition', got: %s", result)
	}
}

func TestAddDashboardWidgetWriteDisabled(t *testing.T) {
	server := NewServer("test-key") // writeEnabled defaults to false

	ctx := context.Background()

	_, err := server.ExecuteTool(ctx, "add_dashboard_widget", map[string]interface{}{
		"dashboard_guid": "guid-123",
		"widget_title":   "Test Widget",
		"nrql_query":     "SELECT count(*) FROM Transaction",
	})

	if err == nil {
		t.Fatal("Expected error when executing add_dashboard_widget with writeEnabled=false")
	}

	expectedError := "Write tools are disabled. Enable with --write-enabled flag."
	if err.Error() != expectedError {
		t.Errorf("Expected error message '%s', got '%s'", expectedError, err.Error())
	}
}

func TestAddDashboardWidgetWriteEnabled(t *testing.T) {
	server := NewServer("test-key", true) // writeEnabled = true

	ctx := context.Background()

	result, err := server.ExecuteTool(ctx, "add_dashboard_widget", map[string]interface{}{
		"dashboard_guid": "guid-123",
		"widget_title":   "Test Widget",
		"nrql_query":     "SELECT count(*) FROM Transaction",
	})

	if err != nil {
		t.Fatalf("Expected no error when executing add_dashboard_widget with writeEnabled=true, got: %v", err)
	}

	if !contains(result, "Added widget") {
		t.Errorf("Expected result to contain 'Added widget', got: %s", result)
	}
}

func TestReadToolsStillWorkWithWriteDisabled(t *testing.T) {
	server := NewServer("test-key") // writeEnabled defaults to false

	ctx := context.Background()

	// Read tools should still work even when write tools are disabled
	result, err := server.ExecuteTool(ctx, "list_applications", map[string]interface{}{})

	if err != nil {
		t.Fatalf("Read tools should work even when write tools are disabled, got error: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty result from read tool")
	}
}

func TestAllToolsWithRegionAndWriteEnabled(t *testing.T) {
	server := NewServerWithRegion("test-key", "eu", true)

	if server == nil {
		t.Fatal("Expected server to be created with region and writeEnabled")
	}

	tools := server.ListTools()
	if len(tools) == 0 {
		t.Error("Expected tools to be registered")
	}

	// Verify writeEnabled is set
	if !server.IsWriteEnabled() {
		t.Error("Expected writeEnabled to be true")
	}
}
