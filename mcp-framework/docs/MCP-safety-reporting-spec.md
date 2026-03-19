This specification defines a **Self-Reporting Safety Protocol** for your custom MCP framework. By moving safety metadata into the tool definition itself, we transform the Bridge from a hardcoded "Dumb Pipe" into a context-aware **Policy Enforcement Point (PEP)**.

---

# 🛡️ Spec: MCP Self-Reporting Safety Metadata (v1.0)

## 1. Objective

To allow MCP backends built with the `mcp-framework` to self-declare their risk profile, resource intensity, and safety requirements. This metadata is consumed by the **MCP-Enforcer Bridge** to automate security decisions (Allow, Deny, or Human-in-the-Loop).

## 2. The "Safety Manifest" Schema

Every tool defined in the framework must now include an `EnforcerProfile`. This data is transmitted during the `tools/list` handshake via the `annotations` field (supported in MCP v1.1+).

### 2.1 Metadata Fields

| Field | Type | Values | Description |
| --- | --- | --- | --- |
| `risk_level` | Enum | `low`, `med`, `high`, `critical` | The potential for system damage. |
| `impact_scope` | Enum | `read`, `write`, `delete`, `admin` | The nature of the operation. |
| `resource_cost` | Integer | `1` (Lite) to `10` (Heavy) | CPU/Memory/API-Credit weight. |
| `pii_exposure` | Boolean | `true` / `false` | Does the tool return sensitive user data? |
| `idempotent` | Boolean | `true` / `false` | Is it safe to retry on timeout? |
| `approval_req` | Boolean | `true` / `false` | Force Human-in-the-Loop regardless of role. |

---

## 3. Framework Implementation (DSL)

The `mcp-framework` Go DSL should be extended to support these fields fluently.

### Example: Custom Oracle Tool Definition

```go
// Using the mcp-framework to build an Oracle backend
framework.NewTool("execute_query").
    WithDescription("Run a custom SQL query against the Oracle DB").
    // --- New Enforcer Metadata ---
    WithRisk(RiskHigh).
    WithImpact(ImpactRead).
    WithResourceCost(8). // Heavy query
    WithPII(true).
    WithValidationRegex(`(?i)\b(SELECT)\b`). // Ensure only SELECTs
    // ----------------------------
    WithHandler(func(args Args) {
        // Logic for Oracle execution
    })

```

---

## 4. Bridge Interpretation Logic

The **MCP-Enforcer Bridge** uses this metadata to map incoming requests to a **Global Safety Matrix**.

### 4.1 The Decision Engine

When a `tools/call` arrives, the Bridge checks the cached `annotations`:

1. **Direct Block**: If `risk_level == critical` AND `user.role != admin`.
2. **HITL Trigger**: If `approval_req == true` OR (`risk_level == high` AND `user.trust_score < 80`).
3. **Throttling**: If `resource_cost > 7` AND `system.load > threshold`, return a "Busy" error.
4. **Sanitization**: If `validation_regex` exists, run it against the `arguments` before passing the call to the backend.

---

## 5. Why This Architecture Wins

* **Scale**: You can add 50 new Oracle tools without updating the Bridge’s code. The Bridge "learns" the danger of the new tools during the initial handshake.
* **Auditability**: Every log entry now includes the self-reported risk. *"Denied tool 'oracle_purge' because risk 'Critical' requires 'Senior' role."*
* **AI Context**: The calling agent (OpenCode) can see these annotations. If it sees `approval_req: true`, it can warn the user: *"I need to run a high-risk query; please watch for an approval prompt."*

---

## 6. Security Note: "The Liar's Problem"

Since the backend is self-reporting, a compromised or malicious backend could report `risk_level: low` for a `DROP TABLE` command.

* **Defense**: The Bridge should maintain a **"Verification Override"** in its `config.yaml`. If the Bridge sees a tool named `*delete*` reporting `risk: low`, it should flag a "Metadata Mismatch" and force a High-Risk verdict.

---

