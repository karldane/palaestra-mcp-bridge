# MCP Framework

A reusable Go framework for building MCP (Model Context Protocol) servers.

## Overview

This framework provides a simple, extensible base for creating MCP servers. It handles the MCP protocol details and provides a clean interface for implementing custom tools.

## Installation

```bash
go get github.com/mcp-bridge/mcp-framework
```

## Quick Start

### Creating a Basic MCP Server

```go
package main

import (
    "context"
    "fmt"
    
    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mcp-bridge/mcp-framework/framework"
)

// MyTool implements the ToolHandler interface
type MyTool struct{}

func (t *MyTool) Name() string {
    return "my_tool"
}

func (t *MyTool) Description() string {
    return "A sample tool"
}

func (t *MyTool) Schema() mcp.ToolInputSchema {
    return mcp.ToolInputSchema{
        Type: "object",
        Properties: map[string]interface{}{
            "input": map[string]interface{}{
                "type":        "string",
                "description": "Input to process",
            },
        },
        Required: []string{"input"},
    }
}

func (t *MyTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
    input := args["input"].(string)
    return fmt.Sprintf("Processed: %s", input), nil
}

func main() {
    // Create server
    server := framework.NewServer("my-server", "1.0.0")
    
    // Register tool
    server.RegisterTool(&MyTool{})
    
    // Initialize and start
    server.Initialize()
    server.Start()
}
```

## Core Concepts

### ToolHandler Interface

The `ToolHandler` interface is the heart of the framework. Implement this interface to create custom tools:

```go
type ToolHandler interface {
    Name() string                    // Unique tool name
    Description() string             // Tool description for users
    Schema() mcp.ToolInputSchema     // JSON Schema for parameters
    Handle(ctx context.Context, args map[string]interface{}) (string, error)
}
```

### Server Configuration

```go
config := &framework.Config{
    Name:         "my-server",
    Version:      "1.0.0",
    Instructions: "Optional usage instructions",
}

server := framework.NewServerWithConfig(config)
```

### Safety Self-Reporting (EnforcerProfile)

The framework supports the MCP Self-Reporting Safety Protocol. Each tool must declare its safety metadata via `GetEnforcerProfile()`:

```go
type MyTool struct{}

func (t *MyTool) GetEnforcerProfile() framework.EnforcerProfile {
    return framework.NewEnforcerProfile(
        framework.WithRisk(framework.RiskMed),        // low, med, high, critical
        framework.WithImpact(framework.ImpactRead),   // read, write, delete, admin
        framework.WithResourceCost(5),                // 1-10 scale
        framework.WithPII(true),                      // exposes sensitive data?
        framework.WithIdempotent(true),               // safe to retry?
        framework.WithApprovalReq(false),             // require human approval?
    )
}
```

**Defaults:**
- Risk: `med`
- Impact: `read`
- Resource Cost: `5`
- PII: `true` (assume sensitive until proven otherwise)
- Idempotent: `false`
- Approval Required: `false`

This metadata is transmitted during the `tools/list` handshake in tool annotations, allowing the MCP-Enforcer Bridge to make automated security decisions.

## New Relic MCP Server

The framework includes a ready-to-use New Relic MCP server implementation.

### Usage

```go
package main

import (
    "github.com/mcp-bridge/mcp-framework/newrelic"
)

func main() {
    // Create server with your New Relic API key
    // For US region (default):
    server := newrelic.NewServer("YOUR_API_KEY")
    
    // For EU region:
    server := newrelic.NewServerWithRegion("YOUR_API_KEY", "eu")
    
    // Initialize and start
    server.Initialize()
    server.Start()
}
```

### Environment Variables

Set your New Relic API key as an environment variable:

```bash
export NEWRELIC_API_KEY=your_api_key_here

# Optional: specify region (us or eu, defaults to us)
export NEWRELIC_REGION=eu
```

**Important**: If your New Relic account is in the EU region, you **must** set `NEWRELIC_REGION=eu` or you'll get 403 authorization errors.

### Available Tools

The New Relic MCP server provides these tools:

**Core Query Tools:**
- **`nrql_query`**: Execute NRQL queries
  - Required: `query` (string)
  - Optional: `account_id` (string - auto-detected from API key)

**Log Tools:**
- **`search_logs`**: Search logs using Lucene-like syntax
  - Required: `query` (string) - supports `level:ERROR`, `service:myapp`, `message:"error text"`, boolean operators (AND, OR, NOT)
  - Optional: `account_id`, `duration`, `limit`

- **`tail_logs`**: Tail logs in real-time (returns latest logs, use with polling)
  - Optional: `query`, `limit`, `include_timestamp`

**APM Tools:**
- **`get_apm_metrics`**: Get basic APM metrics for an application
  - Required: `app_name` (string)
  - Optional: `account_id`, `duration`

- **`get_application_metrics`**: Get comprehensive APM metrics (throughput, error rate, response time, Apdex)
  - Required: `app_name` (string)
  - Optional: `account_id`, `duration`

- **`list_applications`**: List all APM applications
  - Optional: `account_id`, `limit`

- **`query_traces`**: Search distributed traces
  - Optional: `service_name`, `duration`, `limit`, `error_only`

- **`get_transaction_traces`**: Get slowest transaction traces for an application
  - Required: `app_name`
  - Optional: `account_id`, `duration`, `limit`, `min_duration`

- **`get_trace_details`**: Get detailed span waterfall for a specific trace
  - Required: `trace_id`
  - Optional: `account_id`

**Alert Tools:**
- **`list_alerts`**: List alert policies
  - Optional: `account_id`

- **`get_alert_conditions`**: Get conditions for a specific alert policy
  - Required: `policy_id`
  - Optional: `account_id`

- **`get_alert_violations`**: Get recent alert violations and incidents
  - Optional: `policy_id`, `duration`, `status` (open/closed/all), `limit`

**Infrastructure Tools:**
- **`get_infrastructure_metrics`**: Get infrastructure metrics for hosts, containers, or Kubernetes
  - Optional: `hostname`, `container_name`, `cluster_name`, `metric_type` (cpu/memory/disk/network), `duration`

**Dashboard Tools:**
- **`list_dashboards`**: List all dashboards in your New Relic account
  - Optional: `limit`

- **`get_dashboard_data`**: Get data from a specific dashboard's widgets
  - Required: `dashboard_name`
  - Optional: `duration`

### Example Queries

```
# Execute NRQL query (account_id auto-detected from API key)
nrql_query:
  query: "SELECT count(*) FROM Transaction"

# List all APM applications
list_applications: {}

# List alert policies
list_alerts: {}

# Get alert conditions for a policy
get_alert_conditions:
  policy_id: "12345"

# Get basic APM metrics
get_apm_metrics:
  app_name: "MyApplication"
  duration: "1 hour"

# Get comprehensive application metrics
get_application_metrics:
  app_name: "MyApplication"
  duration: "1 hour"

# Search logs with Lucene syntax
search_logs:
  query: "level:ERROR AND service:myapp"
  duration: "30 minutes"

# Query distributed traces
query_traces:
  service_name: "MyService"
  error_only: true
  duration: "1 hour"
```

## Architecture

### Project Structure

```
mcp-framework/
├── framework/          # Core framework
│   ├── server.go      # Base server implementation
│   └── server_test.go # Framework tests
├── newrelic/          # New Relic implementation
│   ├── newrelic.go           # Main server implementation
│   ├── newrelic_test.go      # Main tool tests
│   ├── log_query.go          # Log query parser
│   ├── log_query_test.go     # Log query parser tests
│   └── new_tools_test.go     # Tests for new tools
├── cmd/
│   └── newrelic-mcp/
│       └── main.go    # CLI entry point
├── example/
│   └── main.go        # Example implementation
├── go.mod
└── README.md
```

### Design Principles

1. **Simple**: Minimal abstractions, clear interfaces
2. **Extensible**: Easy to add new tool types
3. **Testable**: Built with TDD, comprehensive test coverage
4. **Production-Ready**: Error handling, timeouts, retries

### Dependencies

- `github.com/mark3labs/mcp-go`: MCP protocol implementation
- Standard library only (no external deps)

## Building MCP Tools

The framework includes several ready-to-use MCP server implementations. Each tool is a standalone binary that can be built from the `cmd/` directory.

### Available Tools

| Tool | Path | Description |
|------|------|-------------|
| **newrelic-mcp** | `cmd/newrelic-mcp/main.go` | New Relic integration |
| **oracle-mcp** | `cmd/oracle-mcp/main.go` | Oracle database integration |

### Building Individual Tools

To build a specific tool:

```bash
cd mcp-framework

# Build newrelic-mcp
go build -o newrelic-mcp ./cmd/newrelic-mcp/main.go

# Build oracle-mcp  
go build -o oracle-mcp ./cmd/oracle-mcp/main.go
```

### Building All Tools

To build all available tools at once:

```bash
cd mcp-framework

# Build all tools into a bin/ directory
mkdir -p bin
for tool in newrelic-mcp oracle-mcp; do
    go build -o bin/$tool ./cmd/$tool/main.go
done
```

### Installing Tools

To install tools to your `$GOPATH/bin`:

```bash
cd mcp-framework
go install ./cmd/...
```

Or to install a specific tool:

```bash
cd mcp-framework
go install ./cmd/newrelic-mcp
```

### Cross-Platform Builds

Build for different platforms using Go's cross-compilation:

```bash
cd mcp-framework

# Build for Linux (AMD64)
GOOS=linux GOARCH=amd64 go build -o newrelic-mcp-linux-amd64 ./cmd/newrelic-mcp/main.go

# Build for macOS (ARM64 - Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o newrelic-mcp-darwin-arm64 ./cmd/newrelic-mcp/main.go

# Build for Windows
GOOS=windows GOARCH=amd64 go build -o newrelic-mcp.exe ./cmd/newrelic-mcp/main.go
```

## Testing

Run all tests:

```bash
cd mcp-framework
go test ./...
```

Run with verbose output:

```bash
go test ./... -v
```

## Best Practices

1. **Always use TDD**: Write tests before implementation
2. **Handle errors gracefully**: Return meaningful error messages
3. **Validate inputs**: Check required parameters in Handle()
4. **Use context**: Respect cancellation and timeouts
5. **Log appropriately**: Don't log sensitive data (API keys, tokens)
6. **Keep tools focused**: Each tool should do one thing well

## Creating Custom Backends

To create a new backend (e.g., for Datadog, AWS, etc.):

1. Create a new package under `mcp-framework/`
2. Implement the `ToolHandler` interface for your tools
3. Create a Server struct that embeds `framework.Server`
4. Register your tools in a constructor function
5. Write comprehensive tests

See `newrelic/` for a complete example.

## License

MIT