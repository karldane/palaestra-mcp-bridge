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

## Configuration Reference

### config.yaml

```yaml
server:
  port: "8080"
  logLevel: info    # debug | info | warn | error

backends:
  atlassian:
    command: "npx -y @xuandev/atlassian-mcp"
    poolSize: 2
    toolPrefix: ""    # empty = tools exposed with original names
    env:
      ATLASSIAN_DOMAIN: "example.atlassian.net"
      ATLASSIAN_EMAIL: "you@example.com"
    secrets:
      - name: atlassian-token
        envKey: ATLASSIAN_API_TOKEN
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

## Production Notes

- Set a strong password for the admin account immediately after first run
- Use HTTPS (terminate TLS at a reverse proxy)
- The SQLite database should be backed up regularly
- For Kubernetes deployment, see `infra/` directory
