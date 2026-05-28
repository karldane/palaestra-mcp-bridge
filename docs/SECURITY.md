# Security Architecture

## Threat Model

### Protected Assets
- **User API tokens**: OAuth access tokens, API keys
- **Backend credentials**: Database passwords, service secrets
- **User data**: Email addresses, policy overrides

### Threat Vectors
1. **Database compromise**: Attacker gains read access to SQLite file
2. **Memory dump**: Attacker inspects process memory
3. **Log leakage**: Secrets accidentally written to logs
4. **Token injection**: Malicious backend exfiltrates user tokens
5. **Backend escape**: Compromised backend gains host access

## Defenses

### Encryption at Rest
- AES-256-GCM for all stored secrets
- Per-secret DEKs (envelope encryption)
- Key separated from data

### Clean Environment
- No system environment variables passed to backends
- Only explicitly configured env vars injected
- Tokens passed via files, not env vars (see below)

### Secret Injection
Tokens written to temp files with `0600` permissions:
```
/tmp/mcp-bridge-token-{random}/token
```

Backend reads from `MCP_TOKEN_FILE` env var - never visible in `ps auxwww`.

### Logging Security
- `logLevel: debug` still never logs tokens/keys
- Structured JSON with explicit allow-lists
- Audit logs for policy decisions

### Process Isolation
- Per-user, per-backend process pools
- Processes run as same user (no additional isolation currently)
- Resource limits via pool size

## Limitations

- No process sandboxing (e.g., gvisor, landlock)
- Single-tenant design - don't share deployment
- No network isolation for backends
- SQLite not designed for high-security environments

## Recommendations

1. **Deploy single-tenant**: One MCP Bridge per security boundary
2. **Rotate keys regularly**: Use `--rotate-keys` quarterly
3. **Monitor access**: Review logs for anomalies
4. **Limit pool sizes**: Prevent resource exhaustion
5. **Audit policies**: Review CEL expressions for correctness