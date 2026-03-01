# MCP Bridge

High-performance SSE-to-Stdio bridge for Model Context Protocol (MCP) servers.

## Features

- **Fixed Process Pool**: Maintains 2 warm MCP server instances
- **SSE Streaming**: Real-time stdout streaming via Server-Sent Events
- **JSON-RPC Routing**: POST messages to `/messages` endpoint for stdin routing
- **Health Checks**: `/healthz` and `/readyz` endpoints
- **Clean Slate**: Restart on disconnect to prevent state pollution
- **Structured Logging**: JSON output for New Relic aggregation

## Requirements

- Go 1.19+
- Kubernetes (for production)

## Development

### Run Tests

```bash
# Run all tests
go test -v ./...

# Run with race detection
go test -v -race ./...

# Run specific test
go test -v -run TestIntegration_DefaultCommandProducesSSEOutput
```

### Build

```bash
# Build binary
go build -o mcp-bridge .

# Build with specific OS/ARCH
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o mcp-bridge .
```

### Run Locally

```bash
# Default command (yes - produces continuous output)
./mcp-bridge

# With custom command
COMMAND="cat" ./mcp-bridge
```

### Docker

```bash
# Build image
docker build -t mcp-bridge .

# Run container
docker run -p 8080:8080 mcp-bridge
```

## Project Structure

```
.
├── main.go              # Main application code
├── main_test.go         # Integration tests
├── USAGE.md             # Usage documentation
├── Dockerfile           # Multi-stage Docker build
├── go.mod               # Go module
├── .github/workflows/   # CI/CD pipeline
└── infra/              # Kubernetes manifests
    ├── base/            # Base Kustomize templates
    └── overlays/        # Environment-specific overlays
```

## Kubernetes Deployment

```bash
# Deploy to staging
kubectl apply -k infra/overlays/project-x/

# Deploy to production
kubectl apply -k infra/overlays/jira-mcp/
```

See [USAGE.md](USAGE.md) for detailed usage information.

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/sse` | GET | SSE stream from MCP server stdout |
| `/messages` | POST | Send JSON-RPC to MCP server stdin |
| `/healthz` | GET | Health check (always 200) |
| `/readyz` | GET | Readiness (200 if warm processes available) |

## Testing

The project includes comprehensive integration tests covering:

- Pool initialization and maintenance
- SSE streaming
- Connection handling and cleanup
- High concurrency stress testing
- Process reaping and pool refill
- JSON-RPC message routing
- Structured logging

Run all tests:
```bash
go test -v -race ./...
```

## CI/CD

GitHub Actions pipeline:
1. Lint (`go fmt`, `go vet`)
2. Security scan (`govulncheck`)
3. Test (`go test -race`)
4. Build (static binary)
5. Push to GHCR
6. Update staging overlay
