# MCP Bridge

Multi-tenant SSE-to-Stdio bridge for Model Context Protocol (MCP) servers,
with OAuth 2.1 authentication, per-user credential injection, and a web
admin interface.

## Features

- **Multi-backend support** &mdash; route multiple MCP servers (Jira,
  Confluence, etc.) behind a single endpoint; backends managed via DB
- **OAuth 2.1 + PKCE** &mdash; RFC 8414 discovery, RFC 7591 dynamic client
  registration, authorization code flow with PKCE
- **Per-user credential injection** &mdash; each user's API tokens are
  injected into the MCP server environment at spawn time
- **Encryption at rest** &mdash; all secrets encrypted using AES-256-GCM
  envelope encryption with K8s secret management support
- **Bcrypt passwords** &mdash; user passwords are stored as bcrypt hashes;
  legacy plaintext is auto-upgraded on login
- **Process pools** &mdash; per-user, per-backend warm pools with idle
  garbage collection
- **Web admin UI** &mdash; manage users, backends, tokens, and probe backend
  health; cookie-based sessions with role separation (admin / user)
- **Live reload** &mdash; backend config changes take effect immediately (no
  restart required)
- **SSE streaming** &mdash; real-time stdout streaming via Server-Sent Events
- **Health checks** &mdash; `/healthz` and `/readyz` endpoints
- **Tool hints and instructions** &mdash; guide LLMs with global instructions,
  per-backend hints, and native MCP server guidance via `mcpbridge_0_README`
- **Configurable logging** &mdash; structured JSON logs with configurable
  levels (debug/info/warn/error), ideal for Kubernetes
- **Security-first** &mdash; tokens and secrets never logged; clean
  environment (no system env vars) passed to backends
- **Human-in-the-Loop (HITL) approval workflow** &mdash; administrative
  approval of tool execution with live feedback and retry capabilities
- **Enhanced precache tooling** &mdash; `--precache-tooling` flag now caches
  both tool definitions and safety profiles for faster startup

## Requirements

- Go 1.19+ (CGo enabled for SQLite)
- `gcc` / C toolchain (required by `go-sqlite3`)
- **Encryption key**: Required for production (see [Encryption Setup](docs/ENCRYPTION.md))

## Quick Start

```bash
# 1. Copy and edit config
cp config.yaml.example config.yaml

# 2. Build
go build -o mcp-bridge .

# 3. Run (creates/migrates SQLite DB automatically)
./mcp-bridge

# Default admin: admin@mcp-bridge.local / changeme
# Web UI: http://localhost:8080/web/login
```

## Project Structure

```
.
├── main.go              # App struct, HTTP wiring, auth middleware
├── main_test.go         # Integration tests (29)
├── config/
│   └── config.go        # YAML config loader (5 tests)
├── store/
│   └── store.go         # SQLite store, 7 tables, bcrypt helpers (33 tests)
├── auth/
│   └── auth.go          # OAuth 2.1 server (31 tests)
├── poolmgr/
│   └── pool.go          # Per-user process pools, probe (37 tests)
├── muxer/
│   └── muxer.go         # Tool-prefix routing, env builder (17 tests)
├── credential/
│   └── secret.go        # Legacy secret interface (9 tests)
├── web/
│   └── web.go           # Admin/user web handlers (48 tests)
├── templates/           # HTML templates (login, dashboard, admin, etc.)
├── config.yaml.example  # Annotated example configuration
└── docs/                # Design specs and project docs
```

**216 tests** across 8 packages.

## API Reference

### MCP Endpoints (OAuth-protected)

| Endpoint     | Method | Description                                |
|--------------|--------|--------------------------------------------|
| `/`          | GET    | SSE stream (opencode connects here)        |
| `/`          | POST   | JSON-RPC request/response                  |
| `/healthz`   | GET    | Health check (always 200)                  |
| `/readyz`    | GET    | Readiness (200 if pool has warm processes) |

### OAuth 2.1 Endpoints

| Endpoint                          | Method | Description                    |
|-----------------------------------|--------|--------------------------------|
| `/.well-known/oauth-authorization-server` | GET | RFC 8414 metadata     |
| `/register`                       | POST   | RFC 7591 dynamic registration  |
| `/authorize`                      | GET    | Authorization page             |
| `/authorize`                      | POST   | Authorization grant            |
| `/token`                          | POST   | Token exchange                 |

### Web UI Endpoints (cookie auth)

| Endpoint              | Description                |
|-----------------------|----------------------------|
| `/web/login`          | Login page                 |
| `/web/dashboard`      | User dashboard             |
| `/web/tokens`         | Manage API tokens          |
| `/web/password`       | Change password            |
| `/web/admin/users`    | Admin: manage users        |
| `/web/admin/backends` | Admin: manage backends     |

## Configuration

See [config.yaml.example](config.yaml.example) for a fully annotated
example. Backends can be seeded from config on first run, but the database
is the source of truth &mdash; use the admin UI for ongoing changes.

### Key Configuration Options

- `server.logLevel` - Control logging verbosity: `debug`, `info` (default), `warn`, or `error`
- `server.port` - HTTP listen port (default: 8080)
- Backends defined in config are seeded on first run only

## Development

```bash
# Run all tests
go test ./... -count=1

# Run with race detection
go test -race ./...

# Build binary
go build -o mcp-bridge .

# Build static (for containers)
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o mcp-bridge .
```

## opencode Integration

In your opencode config (`~/.config/opencode/config.json`):

```json
{
  "mcpServers": {
    "my-bridge": {
      "type": "sse",
      "url": "http://localhost:8080"
    }
  }
}
```

The bridge handles OAuth discovery and PKCE automatically.

See [USAGE.md](USAGE.md) for detailed usage information.

## Security

MCP-Bridge implements enterprise-grade security for multi-tenant deployments:

- **Encryption at rest**: All user API keys and secrets are encrypted using AES-256-GCM
- **Envelope encryption**: Each secret has its own Data Encryption Key (DEK) encrypted by a master Key Encryption Key (KEK)
- **KEK management**: Pluggable providers for env vars, K8s secrets, or external KMS
- **Secret injection**: File-based injection prevents secret leakage via `ps auxwww`

### Quick Encryption Setup

```bash
# 1. Generate encryption key
export ENCRYPTION_KEY=$(openssl rand -hex 32)

# 2. Run migration
./migrate --encryption-key=$ENCRYPTION_KEY

# 3. Verify
./migrate --encryption-key=$ENCRYPTION_KEY --verify

# 4. Start server with key
export ENCRYPTION_KEY=$ENCRYPTION_KEY
./mcp-bridge
```

For detailed encryption documentation:
- [Encryption Setup Guide](docs/ENCRYPTION.md) - Setup, migration, and troubleshooting
- [Security Architecture](docs/SECURITY.md) - Architecture details and threat model
