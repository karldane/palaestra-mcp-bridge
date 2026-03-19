This **Testing Specification** is designed for a **Test-Driven Development (TDD)** workflow. The goal is to validate that the Oracle Backend isn't just "functional" but is "safely functional," strictly adhering to the **Enforcer Safety Spec** and **Transactional Integrity** requirements.

---

# 🧪 TDD Testing Spec: Oracle MCP Enforcer Backend

## 1. Test Suite: Metadata & Self-Reporting

**Objective:** Ensure the backend correctly identifies itself and its risks to the Bridge.

### **Test 1.1: Handshake Metadata Presence**

* **Action:** Call the `tools/list` endpoint.
* **Assertion:** For every tool returned, the `annotations` object **must** contain `enforcer.risk_level`, `enforcer.impact_scope`, and `enforcer.resource_cost`.
* **Failure Condition:** Any tool missing these fields or providing "null/default" values instead of explicit definitions.

### **Test 1.2: Profile Accuracy**

* **Action:** Inspect `oracle_execute_write`.
* **Assertion:** `risk_level` must be `high` and `approval_req` must be `true`.
* **Action:** Inspect `oracle_list_tables`.
* **Assertion:** `risk_level` must be `low`.

---

## 2. Test Suite: Schema Introspection (The "Schema King")

**Objective:** Verify that the ported Meppiel logic accurately maps the self-hosted Oracle instance.

### **Test 2.1: Metadata Caching**

* **Action:** Mock `all_tables` with a list of 5 tables. Initialize the backend.
* **Assertion:** The internal `metadata_cache` must contain all 5 tables within 2 seconds of startup.
* **Assertion:** Calling `oracle_list_tables` must return data from the **cache**, not hit the mock database.

### **Test 2.2: Relationship Discovery**

* **Action:** Mock a Foreign Key relationship between `ORDERS` and `CUSTOMERS`.
* **Assertion:** `oracle_describe_table(table: "ORDERS")` must return a structured JSON object identifying the `customer_id` as a Foreign Key.

---

## 3. Test Suite: Execution Safety & SQL Injection

**Objective:** Ensure the "Enforcer" logic within the framework prevents common database nightmares.

### **Test 3.1: Read/Write Scope Enforcement**

* **Action:** Call `oracle_execute_read` with the argument `sql: "DELETE FROM users"`.
* **Assertion:** The tool **must reject the call** with a "Scope Mismatch" error before sending the query to Oracle.
* **Requirement:** The framework must detect that a `DELETE` operation violates the `ImpactScope: read` annotation.

### **Test 3.2: Automatic Row Limiting**

* **Action:** Call `oracle_execute_read` with `sql: "SELECT * FROM giant_table"`.
* **Assertion:** The actual SQL sent to the driver must be intercepted and rewritten to include `FETCH FIRST 100 ROWS ONLY`.
* **Edge Case:** If the agent provides `FETCH FIRST 10 ROWS ONLY`, the backend should **not** double-append the limit.

---

## 4. Test Suite: Transactional Integrity

**Objective:** Verify that the "Fail-Safe" rollback mechanism works as intended.

### **Test 4.1: Default Rollback**

* **Action:** 1. Start a session.
2. Call `oracle_execute_write` with `INSERT INTO test_table (id) VALUES (999)`.
3. Close the session.
* **Assertion:** Querying the database directly (outside MCP) must show **zero records** added.
* **Logic:** The `sql.Tx` must be rolled back by the framework handler unless a specific `commit` was requested.

---

## 5. Test Suite: Read-Only Mode (Global Flag)

**Objective:** Test the "Hard Lock" safety feature.

### **Test 5.1: Global Restriction**

* **Setup:** Set environment variable `ORACLE_READ_ONLY=true`.
* **Action:** Call `oracle_execute_write`.
* **Assertion:** The tool must return a **503 Service Unavailable** or **403 Forbidden** error stating that the backend is in read-only mode.

---

## 🏗️ Implementation Guide for the Dev Agent

The dev agent should utilize **`testcontainers-go`** or a **Mock SQL Driver** to run these tests.

### **The "Test-First" Workflow**

1. **Define the Interface:** Ensure the `mcp-framework` has a `Tool` interface that includes `GetSafetyProfile()`.
2. **Write the Failure:** Write a test that expects `oracle_execute_write` to require approval.
3. **Implement the Metadata:** Add the `EnforcerProfile` to the Oracle tool.
4. **Fix the Logic:** Implement the transaction rollback logic in the Go handler.

---

### 🛡️ Pre-Release Goal: "The Chaos Test"

Before moving to the Bridge, run one final test: provide the agent with a prompt designed to "trick" it into dropping a table. If the backend blocks it via **ImpactScope enforcement**, the test is a pass.

