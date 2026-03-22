# MCP Bridge - Usage Guide

Multi-tenant SSE-to-Stdio bridge for MCP servers with OAuth 2.1
authentication and per-user credential injection.

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

The bridge also creates an MCP Bridge system backend that provides:
- `mcpbridge_ping` - Check bridge connectivity and get current timestamp
- `mcpbridge_version` - Get mcp-bridge version information
- `mcpbridge_list_backends` - List configured backends
- `mcpbridge_refresh_tools` - Refresh and list tools from all enabled backends

### 3. Access the Web UI

Open `http://localhost:8080/web/login` and sign in with the default
admin credentials. From the admin interface you can:

- **Manage backends** &mdash; add/edit/delete MCP server backends
- **Manage users** &mdash; create users, assign roles (admin/user)
- **Probe backends** &mdash; test that a backend command starts and
  completes the MCP handshake

### 4. User Token Setup

Each user stores their own API tokens via the web UI at `/web/tokens`.
These tokens are injected into the MCP server environment when processes
are spawned for that user. For example, a user's Atlassian API token is
set as `ATLASSIAN_API_TOKEN` in the backend process environment.

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

opencode will:
1. Discover OAuth metadata via `/.well-known/oauth-authorization-server`
2. Register a dynamic client via `/register`
3. Perform PKCE authorization flow
4. Connect SSE and send JSON-RPC requests to the root path `/`

### 6. Tool Hints and Instructions (New!)

The bridge now provides contextual guidance to help LLMs use tools effectively:

**Global Instructions** - Set via Admin UI (`/web/admin/backends`):
- Company-wide guidance, common patterns, cross-backend tips
- Appears in the `mcpbridge_0_README` tool output

**Per-Backend Hints** - Configure per backend:
- Backend-specific usage guidance (e.g., "Use org:tusker-direct for GitHub searches")
- Appears in the README tool under each backend section

**Backend Native Instructions** - Captured automatically:
- Instructions provided by MCP servers during initialization (e.g., GitHub MCP Server's detailed guidance)
- Automatically captured and displayed in README tool

**The `mcpbridge_0_README` tool** should always be called first - it contains:
- Global company information
- Per-backend usage hints
- Live backend instructions from initialize responses

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

**Note:** Debug logging is verbose and should only be used for troubleshooting. All logs are structured JSON written to stdout (ideal for Kubernetes).

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

System environment variables (PATH, HOME, etc.) are **not** passed to backends. If your backend needs specific system variables, add them to the backend's environment configuration in the admin UI.

## Configuration Reference

### config.yaml

```yaml
server:
  port: "8080"
  logLevel: info    # debug | info | warn | error

backends:
  filesystem:
    command: "npx -y @modelcontextprotocol/server-filesystem /path/to/allowed"
    poolSize: 2
    toolPrefix: ""    # empty = tools exposed with original names
    env: {}
    secrets:
      - name: fs-token
        envKey: FS_API_KEY
        context: user   # per-user secret lookup
```

Backends defined in `config.yaml` are seeded into the database on first
run. After that, the **database is the source of truth** &mdash; use the
admin UI for changes. Config-file backends will not overwrite existing DB
entries.

### Environment Variables

| Variable   | Default | Description                           |
|------------|---------|---------------------------------------|
| `PORT`     | `8080`  | HTTP listen port                      |
| `DB_PATH`  | `mcp-bridge.db` | SQLite database path           |

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
- `CreateUser` and `UpdateUser` auto-hash plaintext passwords before
  storing

### Database Schema (SQLite)

Seven tables managed with auto-migration:

| Table              | Purpose                                |
|--------------------|----------------------------------------|
| `users`            | User accounts (name, email, bcrypt pw) |
| `backends`         | MCP server backend definitions         |
| `user_tokens`      | Per-user API tokens for backends       |
| `oauth_clients`    | Dynamic OAuth client registrations     |
| `authorization_codes` | OAuth authorization codes (PKCE)    |
| `access_tokens`    | OAuth bearer tokens                    |
| `web_sessions`     | Cookie-based web UI sessions           |

## API Endpoints

### MCP (OAuth-protected)

```bash
# SSE stream (typically used by opencode, not directly)
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
curl http://localhost:8080/readyz    # 200 if pools are healthy
```

### OAuth 2.1

```bash
# Discovery
curl http://localhost:8080/.well-known/oauth-authorization-server

# Dynamic client registration
curl -X POST http://localhost:8080/register \
  -H "Content-Type: application/json" \
  -d '{"redirect_uris":["http://localhost:9999/callback"]}'
```

## Testing

```bash
# Run all 216 tests
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
  key_file_env: "ENCRYPTION_KEY_FILE"
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
- [docs/ENCRYPTION.md](docs/ENCRYPTION.md) - Complete setup guide
- [docs/SECURITY.md](docs/SECURITY.md) - Security architecture

## Production Notes

- **Encryption is required** for production - generate and protect your KEK
- Set a strong password for the admin account immediately after first run
- Use HTTPS (terminate TLS at a reverse proxy)
- The SQLite database should be backed up regularly
- Store the encryption key separately from the database backup
- For Kubernetes deployment, see `infra/` directory
