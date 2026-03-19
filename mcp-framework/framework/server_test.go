package framework

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// MockToolHandler is a test implementation of ToolHandler

type MockToolHandler struct {
	name        string
	description string
	schema      mcp.ToolInputSchema
	result      string
	err         error
}

func (m *MockToolHandler) Name() string {
	return m.name
}

func (m *MockToolHandler) Description() string {
	return m.description
}

func (m *MockToolHandler) Schema() mcp.ToolInputSchema {
	return m.schema
}

func (m *MockToolHandler) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.result, nil
}

func (m *MockToolHandler) GetEnforcerProfile() EnforcerProfile {
	return DefaultEnforcerProfile()
}

func TestServerCreation(t *testing.T) {
	server := NewServer("test-server", "1.0.0")
	if server == nil {
		t.Fatal("Expected server to be created")
	}

	if server.name != "test-server" {
		t.Errorf("Expected server name 'test-server', got '%s'", server.name)
	}

	if server.version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", server.version)
	}
}

func TestToolRegistration(t *testing.T) {
	server := NewServer("test", "1.0.0")

	handler := &MockToolHandler{
		name:        "test-tool",
		description: "A test tool",
		schema:      mcp.ToolInputSchema{},
		result:      "test result",
	}

	err := server.RegisterTool(handler)
	if err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	tools := server.ListTools()
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	if tools[0] != "test-tool" {
		t.Errorf("Expected tool 'test-tool', got '%s'", tools[0])
	}
}

func TestToolExecution(t *testing.T) {
	server := NewServer("test", "1.0.0")

	handler := &MockToolHandler{
		name:        "test-tool",
		description: "A test tool",
		schema:      mcp.ToolInputSchema{},
		result:      "test result",
	}

	err := server.RegisterTool(handler)
	if err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	ctx := context.Background()
	result, err := server.ExecuteTool(ctx, "test-tool", map[string]interface{}{})

	if err != nil {
		t.Fatalf("Tool execution failed: %v", err)
	}

	if result != "test result" {
		t.Errorf("Expected result 'test result', got '%s'", result)
	}
}

func TestToolExecutionNotFound(t *testing.T) {
	server := NewServer("test", "1.0.0")

	ctx := context.Background()
	_, err := server.ExecuteTool(ctx, "non-existent", map[string]interface{}{})

	if err == nil {
		t.Fatal("Expected error for non-existent tool")
	}

	if err.Error() != "tool 'non-existent' not found" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestDuplicateToolRegistration(t *testing.T) {
	server := NewServer("test", "1.0.0")

	handler1 := &MockToolHandler{
		name: "test-tool",
	}
	handler2 := &MockToolHandler{
		name: "test-tool",
	}

	err := server.RegisterTool(handler1)
	if err != nil {
		t.Fatalf("Failed to register first tool: %v", err)
	}

	err = server.RegisterTool(handler2)
	if err == nil {
		t.Fatal("Expected error for duplicate tool registration")
	}
}

func TestServerWithConfig(t *testing.T) {
	config := &Config{
		Name:         "configured-server",
		Version:      "2.0.0",
		Instructions: "This is a test server",
	}

	server := NewServerWithConfig(config)
	if server == nil {
		t.Fatal("Expected server to be created with config")
	}

	if server.name != "configured-server" {
		t.Errorf("Expected name 'configured-server', got '%s'", server.name)
	}

	if server.instructions != "This is a test server" {
		t.Errorf("Expected instructions, got '%s'", server.instructions)
	}
}
