# MCP Bridge - Usage Guide

Multi-tenant SSE-to-Stdio bridge for MCP servers with OAuth 2.1
authentication, per-user credential injection, and HITL approval workflow.

## Quick Start

### 1. Configure

```bash
cp config.yaml.example config.yaml
# Edit config.yaml with your backend commands and environment
```

### 2. Build and Run

```bash
go build -o mcp-bridge .
./mcp-bridge
```

The bridge creates/migrates its SQLite database (`mcp-bridge.db`)
automatically on startup. A default admin user is seeded:

- **Email:** `admin@mcp-bridge.local`
- **Password:** `changeme`

The bridge also creates an MCP Bridge system backend with these tools:
- `mcpbridge_ping` - Check bridge connectivity
- `mcpbridge_version` - Get version information
- `mcpbridge_list_backends` - List configured backends
- `mcpbridge_refresh_tools` - Refresh tools from backends
- `mcpbridge_capabilities` - Get bridge capabilities
- `mcpbridge_approval_status` - Check pending approval status

### 3. Access the Web UI

Open `http://localhost:8080/web/login` and sign in with the default
admin credentials. From the admin interface you can:

- **Manage backends** - add/edit/delete MCP server backends
- **Manage users** - create users, assign roles (admin/user)
- **Manage enforcer policies** - CEL policies with risk gates
- **Manage enforcer queue** - admin-tier HITL approvals

### 4. User Token Setup

Each user stores their own API tokens via the web UI at `/web/tokens`.
These tokens are injected into the MCP server environment when processes
are spawned for that user.

### 5. Connect opencode

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

opencode handles OAuth discovery, PKCE, and SSE automatically.

### 6. Connect via curl

```bash
# Get access token
TOKEN=$(curl -s -X POST http://localhost:8080/token \
  -d "grant_type=client_credentials" \
  -d "client_id=<your-client-id>" \
  -d "client_secret=<your-client-secret>" | jq -r .access_token)

# List tools
curl -X POST http://localhost:8080/ \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
```

### 7. Tool Hints and Instructions

The bridge provides contextual guidance via `mcpbridge_0_README`:

**Global Instructions** - Set via Admin UI (`/web/admin/backends`):
- Company-wide guidance, patterns, cross-backend tips

**Per-Backend Hints** - Configure per backend:
- Backend-specific usage guidance

**Backend Native Instructions** - Captured automatically from
initialize response (e.g., GitHub MCP Server's detailed guidance)

The `mcpbridge_0_README` tool should always be called first.

## Supported Backends

MCP Bridge manages these MCP server backends:

| Backend      | Description                          | Tools |
|-------------|-------------------------------------|-------|
| `appscan_asoc` | AppScan on Cloud security scanning     | 21   |
| `atlassian`  | Jira + Confluence                    | 18   |
| `aws`       | AWS CLI integration                 | 28   |
| `circleci`  | CircleCI CI/CD                     | 14   |
| `github`    | GitHub API                        | 22   |
| `k8s`       | Kubernetes                        | 40   |
| `newrelic`  | New Relic monitoring              | 18   |
| `oracle`   | Oracle database                  | 11   |
| `qdrant`   | Qdrant vector database           | 25   |
| `slack`    | Slack messaging                   | 30   |

Total: 227 self-reported safety profiles from enabled backends.

Some backends (e.g., qdrant) are `no_keys_required` - they use system
credentials instead of per-user tokens.

## Enforcer & HITL

The enforcer evaluates every tool call against CEL policies and
decides: ALLOW, DENY, or PENDING (requires HITL approval).

### Escalation Tiers

1. **Safe** - No restrictions, tool executes immediately
2. **Rate-Limited** - Allowed but tracked, may be throttled
3. **User HITL** - User must approve their own queue item
4. **Admin HITL** - Admin must approve in admin queue

### Safety Profiles

Each tool has a safety profile from:
1. Self-reported at startup from backend metadata
2. Explicit admin override in database
3. Pattern inference (fallback)

Profile fields: `risk_level`, `impact_scope`, `resource_cost`,
`requires_hitl`, `pii_exposure`, `idempotent`

### CEL Policy Variables

| Variable         | Description                    |
|-----------------|--------------------------------|
| `risk`          | low, medium, high, critical    |
| `impact_scope`  | read, write, admin, destructive|
| `resource_cost` | 1-10 (cost multiplier)        |
| `requires_hitl` | boolean                       |
| `pii_exposure` | boolean                       |
| `system_call_rate` | calls in current window     |

### User HITL Queue

Users access their queue at `/web/user/enforcer/queue`:
- View pending items requiring approval
- Approve/deny their own items
- Set personal safety overrides at `/web/user/enforcer/overrides`
- View policies (read-only) at `/web/user/enforcer/policies`

### Admin HITL Queue

Admins access queue at `/web/admin/enforcer/queue`:
- View/approve/deny admin-tier items
- View all user queues at `/web/admin/enforcer/user-queues`
- Manage policies at `/web/admin/enforcer/policies`
- Manage global overrides at `/web/admin/enforcer/overrides`

## Rate Limits

Risk-based multipliers:
- **low**: 1x
- **medium**: 2x
- **high**: 4x
- **critical**: blocks by default with global_block_destructive

Rate bucket configuration per backend in `enforcer/enforcer.go`
`SetDefaultRateLimits(backend, riskCapacity, riskRefill, resourceCapacity, resourceRefill)`.

## Encryption Setup

MCP-Bridge uses envelope encryption to protect all user secrets at rest.

### Quick Setup

```bash
# 1. Generate encryption key
export ENCRYPTION_KEY=$(openssl rand -hex 32)

# 2. Check status
./migrate --encryption-key=$ENCRYPTION_KEY --status

# 3. Encrypt existing secrets
./migrate --encryption-key=$ENCRYPTION_KEY

# 4. Verify
./migrate --encryption-key=$ENCRYPTION_KEY --verify

# 5. Start server with key
export ENCRYPTION_KEY=$ENCRYPTION_KEY
./mcp-bridge
```

### Configuration

Add to `config.yaml`:

```yaml
encryption:
  provider: "envvar"      # or "k8s"
  key_env: "ENCRYPTION_KEY"
  require_encryption: true
```

### Kubernetes Secret Provider

```yaml
# Create secret
kubectl create secret generic mcp-bridge-encryption-key \
  --from-literal=master.key="$(openssl rand -hex 32)" \
  --namespace=mcp-bridge

# Mount in deployment
spec:
  containers:
  - name: mcp-bridge
    env:
    - name: ENCRYPTION_PROVIDER
      value: "k8s"
    - name: K8S_SECRET_PATH
      value: "/var/run/secrets/encryption"
    volumeMounts:
    - name: encryption-key
      mountPath: /var/run/secrets/encryption
      readOnly: true
  volumes:
  - name: encryption-key
    secret:
      secretName: mcp-bridge-encryption-key
```

### Migration Tool Commands

```bash
# Show status
./migrate --status

# Dry run
./migrate --dry-run

# Migrate
./migrate

# Verify
./migrate --verify

# Rollback (emergency)
./migrate --rollback
```

For detailed documentation, see:
- [devdocs/ENCRYPTION.md](devdocs/ENCRYPTION.md) - Complete setup guide
- [devdocs/SECURITY.md](devdocs/SECURITY.md) - Security architecture
- [devdocs/ENFORCER_IMPLEMENTATION.md](devdocs/ENFORCER_IMPLEMENTATION.md) - HITL and policy details

## Production Notes

- **Encryption is required** for production - generate and protect your KEK
- Set a strong password for the admin account immediately after first run
- Use HTTPS (terminate TLS at a reverse proxy)
- The SQLite database should be backed up regularly
- Store the encryption key separately from the database backup
- For Kubernetes deployment, see [devdocs/SPEC_DEPLOYMENT.md](devdocs/SPEC_DEPLOYMENT.md)

## Logging

### Log Levels

Control verbosity via `config.yaml`:

```yaml
server:
  logLevel: info    # debug | info | warn | error
```

| Level | Output |
|-------|--------|
| `error` | Only errors |
| `warn` | Warnings and errors |
| `info` | Startup, important events, errors (default) |
| `debug` | All request/response details, full debugging |

**Note:** Debug logging is verbose. All logs are structured JSON
written to stdout (ideal for Kubernetes).

### Security Note

**Token and secret values are never logged.** The bridge only logs:
- Environment variable key names (not values)
- Request/response metadata (not sensitive payloads)
- Process lifecycle events

### Clean Environment

The bridge passes **only configured environment variables** to backend processes:
- User tokens from the database
- Systemwide backend env vars (from admin config)
- Mapped env vars (via env_mappings)

System environment variables (PATH, HOME, etc.) are **not** passed.

## Configuration Reference

### config.yaml

```yaml
server:
  port: "8080"
  logLevel: info    # debug | info | warn | error
```

### Environment Variables

| Variable   | Default | Description           |
|------------|---------|----------------------|
| `PORT`     | `8080`  | HTTP listen port     |
| `DB_PATH`  | mcp-bridge.db | SQLite DB path |

## Architecture

### Process Lifecycle

1. When a user authenticates and makes a request, the bridge looks up (or
   creates) a process pool for that user + backend combination.
2. A warm process is taken from the pool. The process was spawned with the
   user's credentials injected into its environment.
3. The JSON-RPC request is written to the process's stdin; the response is
   read from stdout and returned to the HTTP client.
4. The process is returned to the pool (or replaced if unhealthy).
5. Idle pools are garbage-collected after a configurable timeout.

### MCP Handshake

Each spawned process goes through the MCP initialization sequence:
1. Bridge sends `initialize` JSON-RPC request
2. Server responds with capabilities
3. Bridge sends `notifications/initialized`
4. Process is now ready for tool calls

### Password Security

- All passwords are stored as **bcrypt** hashes (cost 10)
- Legacy plaintext passwords are auto-upgraded to bcrypt on successful
  login

### Database Schema (SQLite)

Managed with auto-migration:

| Table              | Purpose                          |
|--------------------|--------------------------------|
| `users`            | User accounts                   |
| `backends`         | MCP server backend definitions |
| `user_tokens`      | Per-user API tokens            |
| `oauth_clients`    | Dynamic OAuth registrations     |
| `authorization_codes` | OAuth codes (PKCE)          |
| `access_tokens`    | OAuth bearer tokens            |
| `web_sessions`     | Cookie sessions                |
| `enforcer_policies` | HITL policies                 |
| `enforcer_tool_profiles` | Tool safety profiles       |
| `enforcer_overrides` | Global overrides            |
| `enforcer_approvals` | HITL approval requests      |

## API Endpoints

### MCP (OAuth-protected)

```bash
# SSE stream (typical for opencode)
curl -N -H "Authorization: Bearer <token>" http://localhost:8080/

# JSON-RPC request
curl -X POST http://localhost:8080/ \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
```

### Health

```bash
curl http://localhost:8080/healthz   # 200 OK
curl http://localhost:8080/readyz    # 200 if pools healthy
```

### OAuth 2.1

```bash
# Discovery
curl http://localhost:8080/.well-known/oauth-authorization-server

# Dynamic registration
curl -X POST http://localhost:8080/register \
  -H "Content-Type: application/json" \
  -d '{"redirect_uris":["http://localhost:9999/callback"]}'
```

## Testing

```bash
# Run all tests
go test ./... -count=1

# With race detection
go test -race ./...

# Verbose output
go test -v ./...

# Single package
go test -v ./store/...
go test -v ./auth/...
go test -v ./web/...
```