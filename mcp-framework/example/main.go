package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mcp-bridge/mcp-framework/framework"
)

// HelloTool is a simple example tool
type HelloTool struct{}

func (t *HelloTool) Name() string {
	return "hello"
}

func (t *HelloTool) Description() string {
	return "Say hello to someone"
}

func (t *HelloTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the person to greet",
			},
		},
		Required: []string{"name"},
	}
}

func (t *HelloTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("name is required")
	}
	return fmt.Sprintf("Hello, %s!", name), nil
}

func (t *HelloTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.DefaultEnforcerProfile()
}

// CalculatorTool demonstrates a more complex example
type CalculatorTool struct{}

func (t *CalculatorTool) Name() string {
	return "calculator"
}

func (t *CalculatorTool) Description() string {
	return "Perform basic arithmetic operations"
}

func (t *CalculatorTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Operation to perform: add, subtract, multiply, divide",
				"enum":        []string{"add", "subtract", "multiply", "divide"},
			},
			"a": map[string]interface{}{
				"type":        "number",
				"description": "First number",
			},
			"b": map[string]interface{}{
				"type":        "number",
				"description": "Second number",
			},
		},
		Required: []string{"operation", "a", "b"},
	}
}

func (t *CalculatorTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	operation, _ := args["operation"].(string)
	a, aOk := args["a"].(float64)
	b, bOk := args["b"].(float64)

	if !aOk || !bOk {
		return "", fmt.Errorf("a and b must be numbers")
	}

	var result float64
	switch operation {
	case "add":
		result = a + b
	case "subtract":
		result = a - b
	case "multiply":
		result = a * b
	case "divide":
		if b == 0 {
			return "", fmt.Errorf("cannot divide by zero")
		}
		result = a / b
	default:
		return "", fmt.Errorf("unknown operation: %s", operation)
	}

	return fmt.Sprintf("%.2f", result), nil
}

func (t *CalculatorTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.DefaultEnforcerProfile()
}

func main() {
	// Create server with configuration
	config := &framework.Config{
		Name:    "example-mcp-server",
		Version: "1.0.0",
		Instructions: `Example MCP Server

This server demonstrates how to build MCP tools using the framework.

Available tools:
- hello: Greet someone by name
- calculator: Perform basic arithmetic

Example usage:
  hello: {"name": "World"}
  calculator: {"operation": "add", "a": 5, "b": 3}`,
	}

	server := framework.NewServerWithConfig(config)

	// Register tools
	if err := server.RegisterTool(&HelloTool{}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register hello tool: %v\n", err)
		os.Exit(1)
	}

	if err := server.RegisterTool(&CalculatorTool{}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register calculator tool: %v\n", err)
		os.Exit(1)
	}

	// Initialize server
	server.Initialize()

	fmt.Fprintln(os.Stderr, "Example MCP Server initialized")
	fmt.Fprintln(os.Stderr, "Tools:", server.ListTools())

	// In a real implementation, you would start serving here
	// server.Start() would typically start the stdio server
	// For this example, we'll just print a message
	fmt.Fprintln(os.Stderr, "Server ready. This example doesn't actually serve requests.")
	fmt.Fprintln(os.Stderr, "See newrelic/main.go for a full working example.")
}
