# Palæstra MCP Bridge

Multi-tenant MCP bridge for Model Context Protocol (MCP) servers, with
OAuth 2.1 authentication, per-user credential injection, and a web admin
interface.

Supports both **Streamable HTTP** (MCP 2024-11-05 spec, `/mcp/v2`) and
legacy **SSE** transport (`/`).

## Features

- **Multi-backend support** &mdash; route multiple MCP servers behind a single
  endpoint; backends managed via database
- **Streamable HTTP transport** &mdash; MCP 2024-11-05 spec-compliant `/mcp/v2`
  endpoint; lazy tool discovery via `{backend}_expand` / `{backend}_call`
- **OAuth 2.1 + PKCE** &mdash; RFC 8414 discovery, RFC 7591 dynamic client
  registration, authorization code flow with PKCE
- **API key auth** &mdash; `mcp_` prefixed keys for programmatic access
- **Per-user credential injection** &mdash; each user's API tokens are
  injected into the MCP server environment at spawn time
- **Encryption at rest** &mdash; all secrets encrypted using AES-256-GCM
  envelope encryption with K8s secret management support
- **Bcrypt passwords** &mdash; user passwords are stored as bcrypt hashes;
  legacy plaintext is auto-upgraded on login
- **Process pools** &mdash; per-user, per-backend warm pools with idle
  garbage collection; processes block rather than fail when pool is at capacity
- **Web admin UI** &mdash; manage users, backends, tokens, and probe backend
  health; cookie-based sessions with role separation (admin / user)
- **Live reload** &mdash; backend config changes take effect immediately (no
  restart required)
- **SSE streaming** &mdash; real-time stdout streaming via Server-Sent Events
  (legacy `/` endpoint)
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

MCP Bridge manages these MCP server backends (tested with; others work too):

| Backend | Description | Repository |
|---------|-------------|------------|
| **appscan_asoc** | AppScan on Cloud security scanning | [tusker-direct/appscan-asoc-mcp](https://github.com/tusker-direct/appscan-asoc-mcp) |
| **argocd** | ArgoCD GitOps controller | [argocd-mcp](https://www.npmjs.com/package/argocd-mcp) (npm) |
| **atlassian** | Jira + Confluence integration | [@xuandev/atlassian-mcp](https://www.npmjs.com/package/@xuandev/atlassian-mcp) (npm) |
| **aws** | AWS API via awslabs | [awslabs/aws-api-mcp-server](https://github.com/awslabs/aws-api-mcp-server) |
| **backoffice** | Git LSP for internal repos | [tusker-direct/git-lsp-mcp](https://github.com/tusker-direct/git-lsp-mcp) |
| **circleci** | CircleCI CI/CD | [@circleci/mcp-server-circleci](https://www.npmjs.com/package/@circleci/mcp-server-circleci) (npm) |
| **github** | GitHub API | [github/mcp-server-github](https://github.com/github/mcp-server-github) |
| **k8s** | Kubernetes cluster management | [kubernetes-mcp-server](https://github.com/kubernetes-sigs/kubernetes-mcp-server) |
| **mongodb** | MongoDB database (disabled) | [tusker-direct/mongodb-mcp](https://github.com/tusker-direct/mongodb-mcp) |
| **newrelic** | New Relic monitoring & alerting | [tusker-direct/newrelic-mcp](https://github.com/tusker-direct/newrelic-mcp) |
| **oracle** | Oracle database | [tusker-direct/oracle-mcp](https://github.com/tusker-direct/oracle-mcp) |
| **qdrant** | Qdrant vector database | [tusker-direct/qdrant-mcp](https://github.com/tusker-direct/qdrant-mcp) |
| **slack** | Slack messaging & workflows | [tusker-direct/slack-mcp](https://github.com/tusker-direct/slack-mcp) |

Total: 227+ self-reported safety profiles from enabled backends.

- **Custom backends** (written by maintainer): appscan_asoc, backoffice, mongodb, newrelic, oracle, qdrant, slack
- **3rd party backends**: argocd, atlassian, aws, circleci, github, k8s

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

## API Reference

### MCP Endpoints (OAuth / API key protected)

| Endpoint     | Method | Description                                              |
|--------------|--------|----------------------------------------------------------|
| `/mcp/v2`    | POST   | **Streamable HTTP** (MCP 2024-11-05 spec) — recommended  |
| `/`          | GET    | SSE stream (legacy)                                      |
| `/`          | POST   | JSON-RPC request/response (legacy)                       |
| `/healthz`   | GET    | Health check (always 200)                                |
| `/readyz`    | GET    | Readiness (200 if pool has warm processes)               |

#### `/mcp/v2` — Streamable HTTP Transport

Fully compliant with the MCP 2024-11-05 Streamable HTTP spec. Supports
`initialize`, `tools/list`, and `tools/call`. Tool discovery is lazy:
`tools/list` returns lightweight `{backend}_expand` and `{backend}_call`
entry-points; calling `{backend}_expand` fetches the full tool list from
the backend.

All responses include `Connection: close` so clients receive TCP EOF
immediately after the response body — no polling or SSE stream required.

**Authentication:** Bearer token (`Authorization: Bearer <token>`) or
API key (`mcp_` prefix).

**opencode config** (`~/.config/opencode/config.json`):
```json
{
  "mcpServers": {
    "my-bridge": {
      "type": "http",
      "url": "http://localhost:8080/mcp/v2"
    }
  }
}
```

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
      "type": "http",
      "url": "http://localhost:8080/mcp/v2"
    }
  }
}
```

The bridge handles OAuth discovery, PKCE, and the Streamable HTTP
handshake automatically. Use an API key (`mcp_...`) for simpler setups.

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
