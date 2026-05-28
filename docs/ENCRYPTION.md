# Encryption Guide

MCP Bridge encrypts all secrets at rest using AES-256-GCM with envelope encryption.

## How It Works

1. **Master Key** (KEK): Your `ENCRYPTION_KEY` - 32 bytes
2. **Data Keys** (DEKs): Generated per-secret, encrypted by the master key
3. **Storage**: Encrypted DEK + nonce + ciphertext stored in DB

This means:
- Compromised DB doesn't reveal secrets
- Each secret has its own DEK (defense in depth)
- Key rotation doesn't require re-encrypting everything

## Setup

### Development

```bash
export ENCRYPTION_KEY="$(openssl rand -hex 32)"
./mcp-bridge
```

### Production

#### Option 1: Environment Variable

```bash
# In your systemd service or deployment
Environment="ENCRYPTION_KEY=your-32-byte-key"
```

#### Option 2: Kubernetes Secret

```yaml
apiSecret:
apiVersion: v1
kind: Secret
metadata:
  name: mcp-bridge-secrets
type: Opaque
stringData:
  encryption-key: "your-32-byte-key"
```

Mount at `/secrets/encryption-key/` (default path).

#### Option 3: External KMS

```yaml
encryption:
  provider: kms
  kms:
    region: us-east-1
    key_id: arn:aws:kms:...:key/...
```

Currently supports AWS KMS.

## Key Rotation

```bash
# 1. Export current key
export OLD_KEY="your-old-key"

# 2. Start with new key
export NEW_KEY="$(openssl rand -hex 32)"
./mcp-bridge --rotate-keys

# 3. Verify
./mcp-bridge --verify-encryption
```

## Verification

```bash
./mcp-bridge --verify-encryption
```

Checks:
- All encrypted fields can be decrypted
- No corruption in ciphertext
- Key is present

## Migration (Unencrypted → Encrypted)

```bash
# Backup first!
cp mcp-bridge.db mcp-bridge.db.backup

# Run migration
export ENCRYPTION_KEY="your-key"
./mcp-bridge --migrate-encryption

# Verify
./mcp-bridge --verify-encryption
```

## Troubleshooting

### "key not found"

Set `ENCRYPTION_KEY` env var before starting.

### "decryption failed"

- Key changed since data was encrypted
- Data corrupted
- Restore from backup

### "migration already applied"

Migration is idempotent - safe to re-run.

## Admin Backend Environment Encryption

Admin-level backend env vars (the JSON object set in the admin UI's "Environment Template" field) are also encrypted at rest using the same master-key envelope encryption.

### How It Works

1. When a backend is created or updated via the admin UI, the `Env` JSON is encrypted and stored in the `encrypted_env` column
2. The plaintext `env` column is cleared to `"{}"`
3. On read (`GetBackend`, `ListBackends`), the `encrypted_env` is transparently decrypted back into the `Env` field — all callers (muxer, scan, probe, admin templates) see decrypted env without any code changes

### Storage Format

```
backends table:
  env             TEXT  DEFAULT '{}'   -- cleared after encryption
  encrypted_env   TEXT                  -- encrypted blob (DEK + nonce + ciphertext)
```

### Legacy Fallback

Backends created before encryption was enabled have `encrypted_env = ''` and their plaintext env in the `env` column. These continue to work — `GetBackend`/`ListBackends` return the `env` column as-is when `encrypted_env` is empty.

### No-Key Enforcement

If no `ENCRYPTION_KEY` is configured (e.g., dev/test), `encryptBackendEnv` returns an error and `CreateBackend`/`UpdateBackend` fail for any backend with non-empty `Env`. This is deliberate — encryption is **required** to store environment variable values.

Backends with `Env = "{}"` (empty) are exempt and can be created without encryption. Legacy backends with plaintext env in the `env` column (and no `encrypted_env`) remain readable — the decryption path falls back to the `env` column when `encrypted_env` is empty.

### Migration

To encrypt existing plaintext backend envs:

```bash
# Backup first!
cp mcp-bridge.db mcp-bridge.db.backup

# Migrate admin backend envs
./migrate --db-path=mcp-bridge.db --encryption-key="your-key" --admin-env

# Verify
./migrate --db-path=mcp-bridge.db --encryption-key="your-key" --admin-verify
```

### Verification

```bash
./migrate --db-path=mcp-bridge.db --encryption-key="your-key" --admin-verify
```

Output:
```
=== Admin Environment Encryption Status ===
Encrypted: 5
Plaintext: 0

✅ All backend envs are encrypted.
```

### UI Indicator

The admin backends page shows a blue <span class="badge badge-encrypted">encrypted</span> badge next to each backend whose env is stored encrypted.