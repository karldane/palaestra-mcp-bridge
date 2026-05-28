# Getting Started with Palæstra MCP Bridge

This guide walks you through setting up MCP Bridge from scratch.

## Prerequisites

- Go 1.19+ with CGo enabled
- `gcc` / C toolchain (for `go-sqlite3`)
- SQLite (usually pre-installed on Linux/macOS)

## Quick Start

### 1. Clone and Build

```bash
git clone https://github.com/karldane/palaestra-mcp-bridge.git
cd palaestra-mcp-bridge
go build -o mcp-bridge .
```

### 2. Generate Encryption Key

For production, you need an encryption key:

```bash
openssl rand -hex 32
```

Store this securely—you'll need it each time the server starts.

### 3. Create Config

```bash
cp config.yaml.example config.yaml
```

Edit `config.yaml`:

```yaml
server:
  port: 8080
  logLevel: info

# Point to your backends
backends:
  - id: github
    tool_prefix: github
    command: /path/to/github-mcp-server stdio
    pool_size: 2

# Optional: static user for testing (not needed for OAuth)
# users:
#   - email: admin@local
#     password: changeme
#     role: admin
```

### 4. Run

```bash
export ENCRYPTION_KEY="your-32-byte-key-from-step-2"
./mcp-bridge
```

The server will:
- Create `mcp-bridge.db` with SQLite schema
- Seed default admin user: `admin@mcp-bridge.local` / `changeme`
- Start HTTP server on port 8080

### 5. Access Web UI

- **URL**: http://localhost:8080/web/login
- **Admin login**: `admin@mcp-bridge.local` / `changeme`

From the admin UI you can:
- Add/edit backends
- Manage users
- Configure enforcer policies
- View system status

## Adding Your First Backend

### Via Web UI

1. Log in as admin
2. Go to **Backends** in the nav
3. Click **Add Backend**
4. Fill in:
   - **ID**: unique identifier (e.g., `github`, `qdrant`)
   - **Tool Prefix**: routing prefix for `{prefix}_expand` / `{prefix}_call`
   - **Command**: how to spawn the MCP server
   - **Pool Size**: concurrent processes per user (default: 1-2)
5. Click **Save**

### Via Database

```sql
INSERT INTO backends (id, tool_prefix, command, pool_size, enabled)
VALUES ('github', 'github', '/home/user/code/github-mcp-server stdio', 2, 1);
```

### Verifying the Backend

From the Backends page, click **Test** to verify the backend spawns and returns tools correctly.

## Encryption Setup

All user tokens and secrets are encrypted at rest using AES-256-GCM.

### First-Time Encryption Migration

If you're starting fresh:

```bash
export ENCRYPTION_KEY="$(openssl rand -hex 32)"
./mcp-bridge
```

The key is used automatically for all new secrets.

### Migrating Existing Unencrypted Data

```bash
# Backup first!
cp mcp-bridge.db mcp-bridge.db.backup

# Run migration with key
export ENCRYPTION_KEY="your-32-byte-key"
./mcp-bridge --migrate-encryption

# Verify
./mcp-bridge --verify-encryption
```

### Key Providers

The server tries these in order:

1. **`ENCRYPTION_KEY`** env var (simplest)
2. **K8s secret**: mounted at `/secrets/encryption-key/`
3. **External KMS**: configured in `config.yaml`

See [ENCRYPTION.md](ENCRYPTION.md) for details.

## Authentication

### OAuth 2.1 (Recommended for Production)

1. Configure OAuth in `config.yaml`:
```yaml
oauth:
  issuer: https://your-domain.com
  audience: mcp-bridge
  jwks_url: https://your-domain.com/.well-known/jwks.json
```

2. Users authenticate via the OAuth flow at `/authorize`

### API Keys (Simpler)

Generate an API key for a user, then use:

```bash
curl -H "Authorization: mcp_your-api-key-here" \
     http://localhost:8080/mcp/v2 -d '{"jsonrpc":"2.0","method":"initialize",...}'
```

## Enforcer Policies

The enforcer provides tiered safety controls:

| Tier | Description |
|------|-------------|
| **Safe** | No intervention required |
| **Rate-Limited** | Tool call rate limited |
| **User HITL** | User must approve before execution |
| **Admin HITL** | Admin must approve before execution |

### Policy Variables

CEL policies have access to:

- `risk`: `low`, `medium`, `high`, `critical`
- `impact_scope`: `self`, `backend`, `user`, `system`
- `resource_cost`: positive integer
- `requires_hitl`: boolean
- `pii_exposure`: boolean
- `tool_name`: string
- `user.role`: string

### Example Policies

**Block all destructive operations on non-qdrant backends:**
```
backend_id != "qdrant" && risk == "high"
```

**Require HITL for PII exposure:**
```
pii_exposure == true
```

**Rate limit high-cost tools:**
```
resource_cost > 5
```

### Creating Policies

1. Go to **Admin → Enforcer → Policies**
2. Click **Add Policy**
3. Enter:
   - **Name**: descriptive name
   - **Expression**: CEL expression
   - **Action**: `allow`, `deny`, `hitl_user`, `hitl_admin`
   - **Priority**: higher = evaluated first
4. Save

**Note**: Currently policies are best written with agentic tooling that analyses your backend's tool profiles. Future tooling will provide interactive policy creation.

### Tool Profiles

Each tool has a safety profile with:
- `risk_level`: low/medium/high/critical
- `impact_scope`: self/backend/user/system
- `resource_cost`: 1-10 scale
- `requires_hitl`: boolean
- `pii_exposure`: boolean
- `idempotent`: boolean

Backends can self-report via `mcpbridge_0_README` metadata. Admin can override via the UI.

## Connecting an MCP Client

### opencode

```json
{
  "mcpServers": {
    "palaestra": {
      "type": "http",
      "url": "http://localhost:8080/mcp/v2"
    }
  }
}
```

### Claude Desktop

```json
{
  "mcpServers": {
    "palaestra": {
      "command": "node",
      "args": ["/path/to/mcp-client", "http://localhost:8080/mcp/v2"]
    }
  }
}
```

### Direct HTTP

```bash
curl -X POST http://localhost:8080/mcp/v2 \
  -H "Authorization: mcp_your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
```

## Troubleshooting

### Backend Won't Start

- Check the **Test** button in the UI for errors
- Verify the command path exists and is executable
- Check logs at `debug` logLevel for details

### Encryption Key Lost

- There's no recovery—secrets are encrypted with the key
- Restore from backup or re-create tokens

### Database Locked

- Only one process can write at a time
- Ensure no other instances are running

### Pool Exhaustion

- Increase `pool_size` for the backend
- Check `max_pool_size` setting (0 = unlimited)

## Next Steps

- Read [ENCRYPTION.md](ENCRYPTION.md) for production encryption setup
- Read [SECURITY.md](SECURITY.md) for threat model and architecture
- Explore the admin UI for all configuration options