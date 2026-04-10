# Palæstra MCP Bridge

Multi-tenant SSE-to-Stdio bridge for Model Context Protocol (MCP) servers,
with OAuth 2.1 authentication, per-user credential injection, and a web
admin interface.

## Features

- **Multi-backend support** &mdash; route multiple MCP servers behind a single
  endpoint; backends managed via database
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

### Enforcer & HITL Features

- **Human-in-the-Loop (HITL) approval workflow** &mdash; tiered escalation
  model: Safe → Rate-Limited → User HITL → Admin HITL
- **CEL policy engine** &mdash; expressive policies with `risk`, `impact_scope`,
  `resource_cost`, `requires_hitl`, `pii_exposure`, `system_call_rate` variables
- **Per-tool safety profiles** &mdash; self-reported from backend metadata or
  explicit overrides; profiles include risk_level, impact_scope, resource_cost,
  requires_hitl, pii_exposure, idempotent
- **Mandatory justification fields** &mdash; configurable minimum length per-tool
- **Rate bucket enforcement** &mdash; per-tool call rate tracking with risk-based
  multipliers (low=1, med=2, high=4)
- **Locked policies** &mdash; admin-locked policies cannot be overridden
- **Personal safety overrides** &mdash; users can escalate specific tools
- **User-level HITL UI** &mdash; users can approve their own queue items,
  view policies (read-only), manage personal overrides
- **Real-time queue SSE** &mdash; live pending-count updates via
  `/web/user/enforcer/events`
- **Admin cross-queue view** &mdash; admins can see all user-tier queues

### Supported Backends

MCP Bridge manages these MCP server backends:

| Backend      | Description                          | Tools |
|-------------|-------------------------------------|-------|
| `appscan_asoc` | AppScan on Cloud security scanning     | 21   |
| `atlassian`  | Jira + Confluence                 | 18   |
| `aws`       | AWS CLI integration              | 28   |
| `circleci`  | CircleCI CI/CD                   | 14   |
| `github`    | GitHub API                     | 22   |
| `k8s`      | Kubernetes                     | 40   |
| `newrelic`  | New Relic monitoring             | 18   |
| `oracle`   | Oracle database                 | 11   |
| `qdrant`   | Qdrant vector database          | 25   |
| `slack`    | Slack messaging                | 30   |
| `mongodb`   | MongoDB (disabled)             | 0    |

Total: 227 self-reported safety profiles from enabled backends.

Backends marked as `no_keys_required` (e.g., qdrant) use system credentials
instead of per-user tokens.

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
├── main_test.go         # Integration tests
├── config/
│   └── config.go        # YAML config loader
├── store/
│   ├── store.go         # SQLite store, schema migration
│   ├── backend.go       # Backend definitions
│   └── enforcer_store.go # Enforcer data (profiles, policies, overrides)
├── auth/
│   └── auth.go          # OAuth 2.1 server
├── poolmgr/
│   └── pool.go          # Per-user process pools, probe
├── muxer/
│   ├── muxer.go         # Tool-prefix routing
│   └── augment.go       # Tool augmentation (instructions, justification)
├── enforcer/
│   ├── enforcer.go      # Core enforcer, HandleToolCall, interfaces
│   ├── cel_engine.go    # CEL policy evaluator
│   ├── resolver.go      # Tool profile resolution (3-tier chain)
│   ├── types.go        # DecisionContext, EnforcerConfig, CallOptions
│   └── enforcer_test.go # Unit tests
├── web/
│   └── web.go           # Admin/user web handlers
├── scan.go             # Self-reporting profile scanner
├── precache.go          # Tool + profile precaching
├── templates/           # HTML templates (login, dashboard, admin, enforcer)
├── Makefile            # Build automation
├── config.yaml.example  # Annotated example
├── seed.go            # Default data seeding
└── devdocs/           # Design specs and specs
```

**100+ tests** across packages.

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

| Endpoint                         | Description                           |
|----------------------------------|--------------------------------------|
| `/web/login`                     | Login page                            |
| `/web/dashboard`                 | User dashboard                      |
| `/web/tokens`                    | Manage API tokens                     |
| `/web/password`                  | Change password                    |
| `/web/user/enforcer/queue`       | User: view & act on HITL queue  |
| `/web/user/enforcer/overrides`   | User: manage safety overrides |
| `/web/user/enforcer/policies`    | User: view policies (read-only) |
| `/web/user/enforcer/events`   | User: SSE queue updates         |
| `/web/admin/users`             | Admin: manage users           |
| `/web/admin/backends`          | Admin: manage backends       |
| `/web/admin/enforcer/queue`    | Admin: HITL approval queue |
| `/web/admin/enforcer/user-queues` | Admin: view all user queues |
| `/web/admin/enforcer/policies`  | Admin: manage CEL policies   |
| `/web/admin/enforcer/overrides` | Admin: global overrides |

### System Tools (mcpbridge)

The bridge provides built-in system tools:

| Tool                    | Description                          |
|--------------------------|--------------------------------------|
| `mcpbridge_ping`         | Check bridge connectivity            |
| `mcpbridge_version`      | Get version information             |
| `mcpbridge_list_backends` | List configured backends            |
| `mcpbridge_refresh_tools`   | Refresh tools from backends          |
| `mcpbridge_capabilities`  | Get bridge capabilities            |
| `mcpbridge_approval_status` | Check pending approval status |

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
