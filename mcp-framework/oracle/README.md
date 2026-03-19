# Oracle MCP Backend

A Go-native MCP (Model Context Protocol) backend for Oracle databases, providing schema introspection and query execution tools with self-reporting safety metadata.

## Overview

This is a port of the Python `oracle-mcp-server` to Go, built on the `mcp-framework` with the following features:

- **Pure Go Implementation**: Uses `go-ora` driver (no Oracle Instant Client required)
- **Schema Introspection**: Deep table/column analysis with caching
- **Safety First**: All tools include `EnforcerProfile` metadata for the Enforcer Bridge
- **Read-Only by Default**: Write operations require explicit opt-in

## Tools

### Schema Introspection (Read-Only)

| Tool | Description | Risk | Impact | Approval |
|------|-------------|------|--------|----------|
| `oracle_list_tables` | List all tables in the database | Low | Read | No |
| `oracle_describe_table` | Get table schema (columns, relationships) | Low | Read | No |
| `oracle_search_tables` | Search tables by name pattern | Low | Read | No |
| `oracle_search_columns` | Search columns across all tables | Low | Read | No |
| `oracle_get_constraints` | Get PK/FK/UNIQUE/CHECK constraints | Low | Read | No |
| `oracle_get_indexes` | Get table indexes | Low | Read | No |
| `oracle_get_related_tables` | Get FK relationships | Low | Read | No |
| `oracle_explain_query` | Get query execution plan | Low | Read | No |

### Query Execution

| Tool | Description | Risk | Impact | Approval |
|------|-------------|------|--------|----------|
| `oracle_execute_read` | Execute SELECT queries (100 row limit) | Med | Read | **Yes** |
| `oracle_execute_write` | Execute INSERT/UPDATE/DELETE | High | Write | **Yes** |

## Installation

```bash
cd mcp-framework
go build -o oracle-mcp ./cmd/oracle-mcp/main.go
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `ORACLE_CONNECTION_STRING` | Oracle connection string (required) | - |
| `ORACLE_READ_ONLY` | Enable read-only mode | `true` |
| `CACHE_DIR` | Schema cache directory | `.cache` |

### Connection String Format

```
oracle://user:password@host:port/service_name
```

Examples:
```bash
# Basic connection
export ORACLE_CONNECTION_STRING="oracle://scott:tiger@localhost:1521/ORCL"

# With service name
export ORACLE_CONNECTION_STRING="oracle://user:pass@db.example.com:1521/XEPDB1"
```

## Usage

### Basic Usage (Read-Only)

```bash
export ORACLE_CONNECTION_STRING="oracle://user:pass@host:1521/SERVICE"
./oracle-mcp
```

### Enable Write Operations

Write operations require TWO conditions:
1. `ORACLE_READ_ONLY=false` environment variable
2. `-write-enabled` command-line flag

```bash
export ORACLE_CONNECTION_STRING="oracle://user:pass@host:1521/SERVICE"
export ORACLE_READ_ONLY=false
./oracle-mcp -write-enabled
```

## Safety Features

### EnforcerProfile Metadata

Every tool reports its safety characteristics via `EnforcerProfile`:

```go
type EnforcerProfile struct {
    RiskLevel    RiskLevel   // low, med, high, critical
    ImpactScope  ImpactScope // read, write, delete, admin
    ResourceCost int         // 1-10 (CPU/Memory weight)
    PIIExposure  bool        // Returns sensitive data?
    Idempotent   bool        // Safe to retry?
    ApprovalReq  bool        // Requires human approval?
}
```

This metadata is transmitted to the Enforcer Bridge during the `tools/list` handshake.

### Query Classification

The backend automatically classifies SQL:

- **SELECT/WITH**: Allowed via `oracle_execute_read`
- **INSERT/UPDATE/DELETE/MERGE**: Require `oracle_execute_write` + write-enabled flag
- **DDL (CREATE/ALTER/DROP)**: Blocked in read-only mode

### Row Limiting

All SELECT queries are automatically limited to prevent resource exhaustion:
- Default: 100 rows
- Maximum: 1,000 rows
- Can be overridden with `max_rows` parameter

### Transaction Safety

Write operations use transactions:
- **Default**: Rollback (dry-run mode)
- **Commit**: Only when `commit=true` parameter is set

## Architecture

### Schema Caching

- Tables and columns are cached on startup
- Lazy loading: Full table details loaded on first access
- Cache persisted to disk (`.cache/{schema}.json`)
- Rebuild with `rebuild_schema_cache` tool

### Database Connection

- Uses `go-ora` pure Go driver
- Connection pooling via `database/sql`
- Configurable read-only mode at connection level

## Testing

```bash
cd mcp-framework
go test ./oracle -v
```

## License

This project uses the FSL-1.1-ALv2 license.

## References

- [MCP Framework](../framework/) - Base framework with EnforcerProfile support
- [New Relic Backend](../newrelic/) - Example of another backend implementation
- [Oracle MCP Server (Python)](https://github.com/danielmeppiel/oracle-mcp-server) - Original Python implementation
