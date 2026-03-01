# MCP Bridge - Usage Guide

A high-performance SSE-to-Stdio bridge for Model Context Protocol (MCP) servers.

## Quick Start

### Run the Bridge

```bash
# Default command (echoes input for testing)
./mcp-bridge

# With custom MCP server command
COMMAND="npx @modelcontextprotocol/server-jira" ./mcp-bridge
```

### Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/sse` | GET | SSE stream from MCP server stdout |
| `/messages` | POST | Send JSON-RPC to MCP server, returns response (synchronous) |
| `/healthz` | GET | Health check (always 200) |
| `/readyz` | GET | Readiness check (200 if warm processes available) |

### Examples

#### Connect to SSE stream
```bash
curl -N http://localhost:8080/sse
```

#### Send a message (synchronous request/response)
```bash
curl -X POST http://localhost:8080/messages \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"list_tools","id":1}'
```

The `/messages` endpoint:
- Takes a process from the warm pool
- Writes JSON-RPC request to stdin
- Waits for response from stdout
- Returns the response to the HTTP client
- Returns process to pool
- **30 second timeout** - returns 504 if no response

#### Check health
```bash
curl http://localhost:8080/healthz  # Returns OK
curl http://localhost:8080/readyz   # Returns OK if pool has warm processes
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `COMMAND` | `sh -c 'cat; sleep 1'` | The command to execute for MCP server |
| `STRICT_HANDSHAKE` | `false` | Enable JSON-RPC handshake validation |

### Example Commands

```bash
# Use npx with a specific MCP server
COMMAND="npx @modelcontextprotocol/server-jira" ./mcp-bridge

# Use a local Python script
COMMAND="python3 /path/to/mcp_server.py" ./mcp-bridge

# Use a Docker container
COMMAND="docker run --rm mcp-server" ./mcp-bridge

# Enable strict handshake validation
STRICT_HANDSHAKE=true COMMAND="npx @modelcontextprotocol/server-jira" ./mcp-bridge
```

## Architecture

- **Process Pool**: Maintains 2 warm instances (Fixed Buffer)
- **Synchronous /messages**: Request/response with 30s timeout
- **SSE Broadcast**: Responses from /messages are also broadcast to /sse
- **Clean Slate**: Each SSE disconnect kills the process and spawns a replacement
- **JSON Logging**: All output is structured JSON for New Relic integration

## Production Deployment

See [infra/](../infra/) for Kubernetes manifests:

```bash
# Deploy to Kubernetes
kubectl apply -k overlays/production/
```
