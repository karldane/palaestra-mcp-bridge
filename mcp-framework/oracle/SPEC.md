This specification defines the architecture and implementation requirements for the **Oracle MCP Backend**. It is a Go-native port of the `danielmeppiel/oracle-mcp-server` (Python), re-engineered to use our custom `mcp-framework` and provide self-reporting safety metadata to the **Enforcer Bridge**.

danielmeppiel/oracle-mcp-server has been downloaded to ../../../oss/oracle-mcp-server/
---

# 📑 Spec: Oracle MCP "Enforcer" Backend (Go-Native)

## 1. Core Objectives

* **Portability:** Use a pure Go driver (`go-ora`) to eliminate the need for the Oracle Instant Client or Python/Java runtimes.
* **Schema Intelligence:** Port the "Schema King" introspection logic from the Meppiel server to provide the LLM with deep context (tables, columns, constraints).
* **Safety Integration:** Implement the `EnforcerProfile` spec so every tool self-reports its risk, cost, and impact.
* **Licensing:** Ensure the code is compatible with our **FSL-1.1-ALv2** project license.

---

## 2. Technical Stack

* **Language:** Go 1.22+
* **Driver:** `github.com/sijms/go-ora/v2` (Pure Go Wire Protocol).
* **Framework:** Internal `mcp-framework`.
* **Database Target:** Self-hosted Oracle 12c/19c/21c/23ai instances.

---

## 3. Architecture & Component Mapping

| Python Component (Meppiel) | Go Framework Requirement | Implementation Notes |
| --- | --- | --- |
| `connection_manager.py` | `database/sql` + `go-ora` | Implement connection pooling with configurable timeouts. |
| `schema_inspector.py` | `internal/metadata` | Background goroutine to cache `all_tab_columns` and `all_constraints`. |
| `query_validator.py` | **Discard** | Validation logic is now the responsibility of the **Enforcer Bridge** via CEL. |
| `tool_definitions.py` | `framework.NewTool()` | DSL-based definitions with mandatory `EnforcerProfile`. |

---

## 4. Requirement: The "Schema King" Port

The dev agent must extract and port the following SQL introspection queries from the Meppiel repository:

1. **Metadata Indexing:** A startup worker must fetch all table and view names available to the current user to build a thread-safe `map[string]TableMetadata`.
2. **Deep Description:** A `describe_table` tool that returns columns, data types, and primary/foreign key relationships.
3. **Search:** A fuzzy-search tool to help the agent find relevant tables based on keywords in comments (`all_tab_comments`).

---

## 5. Requirement: Mandatory Safety Metadata

Every tool implemented in this backend **must** include the following `EnforcerProfile` annotations.

### Tool: `execute_query`

* **RiskLevel:** `high` (or `med` if strictly verified as `SELECT`).
* **ImpactScope:** `read` (Default) / `write` (If DML detected).
* **ResourceCost:** `8` (Heavy CPU/IO potential).
* **ApprovalReq:** `true` (Trigger HITL for any ad-hoc SQL).

### Tool: `list_tables`

* **RiskLevel:** `low`.
* **ImpactScope:** `read`.
* **ResourceCost:** `2`.
* **ApprovalReq:** `false`.

---

## 6. Security & Transactional Safety

* **Read-Only Mode:** Provide a configuration flag `ORACLE_READ_ONLY=true`. If set, the driver must execute `SET TRANSACTION READ ONLY` or reject any tool that doesn't have `ImpactScope: read`.
* **Transactional Enclosure:** Every `execute_query` call must run within a `sql.Tx`. The transaction should be **rolled back** by default unless a specific `commit_transaction` tool is explicitly called by the agent (which should be tagged as `Risk: Critical`).
* **Row Limiting:** Automatically append/inject `FETCH FIRST 100 ROWS ONLY` to any query that does not include a limit clause, unless overridden by an admin-level tool call.

---

## 7. Implementation Instructions for Dev Agent

1. **Initialize Backend:** Create `cmd/oracle-backend/main.go` using the `mcp-framework`.
2. **Driver Setup:** Initialize `go-ora` using environment variables: `ORA_HOST`, `ORA_PORT`, `ORA_SERVICE`, `ORA_USER`, `ORA_PASSWORD`.
3. **Port Queries:** Analyze the Meppiel Python code to extract the SQL for:
* Listing tables and views.
* Getting column details.
* Retrieving Foreign Key constraints.


4. **Metadata Cache:** Implement a background worker that refreshes every 30 minutes.
5. **Expose Tools:** Implement the following toolset:
* `oracle_list_tables`: Lists indexed tables.
* `oracle_describe_table`: Returns detailed schema for a specific table.
* `oracle_execute_read`: For `SELECT` statements (Safety: `RiskMed`).
* `oracle_execute_write`: For `INSERT/UPDATE/DELETE` (Safety: `RiskHigh`, `ApprovalReq: true`).



---

## 8. Final Delivery Format

* A single Go package that compiles to a static binary.
* Zero external dependencies other than `go-ora` and the internal `mcp-framework`.
* A `README.md` explaining the self-reporting features and how the Enforcer Bridge should interpret its metadata.

---


