# New Relic MCP Tool Enhancement Plan

## Overview
This document tracks planned improvements to the newrelic-mcp tool to provide better read-only functionality and address current issues.

---

## 🔧 Current Issues (Priority: High)

### Issue 1: Fix `search_logs` Tool
**Status:** Broken  
**Problem:** NRQL syntax errors when using standard log query syntax like `level:INFO` or `service:my-app`

**Current behavior:**
```
Error: NRQL Syntax Error: unexpected ':' at position 75
```

**Expected behavior:** 
- Support standard log search syntax (e.g., `level:ERROR`, `service:mystique`, `message:"error message"`)
- Properly escape special characters in queries
- Support boolean operators (AND, OR, NOT)

**Implementation notes:**
- The current implementation directly interpolates the query into NRQL: `SELECT * FROM Log WHERE %s`
- Need to parse log query syntax and convert to valid NRQL WHERE clauses
- Handle field:value pairs, quoted strings, and operators

**Tasks:**
- [ ] Implement log query parser to convert `field:value` syntax to NRQL
- [ ] Add proper escaping for special characters
- [ ] Support common log query operators (AND, OR, NOT)
- [ ] Add unit tests for various query patterns
- [ ] Update documentation with supported query syntax

---

### Issue 2: Fix `get_apm_metrics` Tool
**Status:** Broken  
**Problem:** "No warm processes" error from backend pool

**Error message:**
```
Error: Streamable HTTP error: Error POSTing to endpoint: No warm processes
```

**Analysis:**
This appears to be an infrastructure/MCP Bridge pool issue, not a code issue. However, we should ensure the tool is properly implemented and handles errors gracefully.

**Tasks:**
- [ ] Verify tool implementation is correct
- [ ] Add better error handling and user-friendly error messages
- [ ] Investigate if this is a pool configuration issue (check pool_status)
- [ ] Test with different app names and durations

---

## 🆕 New Read-Only Tools (Priority: Medium)

### Tool 1: List Applications/Entities
**Purpose:** List all APM applications and infrastructure entities

**Proposed interface:**
```go
type ListApplicationsTool struct{}

func (t *ListApplicationsTool) Name() string {
    return "list_applications"
}

func (t *ListApplicationsTool) Description() string {
    return "List all APM applications and entities in your New Relic account"
}

// Parameters:
// - entity_type: optional filter (APM, INFRA, BROWSER, etc.)
// - limit: maximum results (default 50)
```

**New Relic GraphQL Query:**
```graphql
query($accountId: Int!) {
  actor {
    account(id: $accountId) {
      apm: nrql(query: "SELECT latest(appName), latest(host) FROM Transaction FACET appName LIMIT 100") {
        results
      }
    }
  }
}
```

**Tasks:**
- [ ] Implement ListApplicationsTool
- [ ] Support filtering by entity type
- [ ] Format results with app names, hosts, and key metrics
- [ ] Add tests

---

### Tool 2: Get Application Performance Metrics
**Purpose:** Get detailed APM metrics including error rates, throughput, and response times

**Proposed interface:**
```go
type GetApplicationMetricsTool struct{}

func (t *GetApplicationMetricsTool) Name() string {
    return "get_application_metrics"
}

func (t *GetApplicationMetricsTool) Description() string {
    return "Get comprehensive APM metrics for an application"
}

// Parameters:
// - app_name: required, name of the application
// - duration: time range (default "1 hour")
// - metrics: optional list (throughput, error_rate, response_time, apdex)
```

**Metrics to include:**
- Throughput (requests per minute)
- Error rate (%)
- Average response time
- 95th percentile response time
- Apdex score
- Error breakdown by type

**Tasks:**
- [ ] Implement GetApplicationMetricsTool
- [ ] Support selectable metrics
- [ ] Format results in a readable table format
- [ ] Add tests

---

### Tool 3: View Transaction Traces
**Purpose:** Get transaction trace details for slow or error transactions

**Proposed interface:**
```go
type GetTransactionTracesTool struct{}

func (t *GetTransactionTracesTool) Name() string {
    return "get_transaction_traces"
}

func (t *GetTransactionTracesTool) Description() string {
    return "Get slowest transaction traces for an application"
}

// Parameters:
// - app_name: required
// - duration: time range (default "1 hour")
// - limit: number of traces (default 10)
// - min_duration: only traces slower than X ms
```

**Tasks:**
- [ ] Research New Relic GraphQL API for transaction traces
- [ ] Implement GetTransactionTracesTool
- [ ] Format trace details (segments, SQL queries, external calls)
- [ ] Add tests

---

### Tool 4: Get Alert Conditions
**Purpose:** View conditions for a specific alert policy

**Proposed interface:**
```go
type GetAlertConditionsTool struct{}

func (t *GetAlertConditionsTool) Name() string {
    return "get_alert_conditions"
}

func (t *GetAlertConditionsTool) Description() string {
    return "Get all conditions for a specific alert policy"
}

// Parameters:
// - policy_id: required, ID of the alert policy
```

**New Relic GraphQL Query:**
```graphql
query($accountId: Int!, $policyId: ID!) {
  actor {
    account(id: $accountId) {
      alerts {
        policy(id: $policyId) {
          id
          name
          conditions {
            id
            name
            type
            enabled
            nrql {
              query
            }
            terms {
              threshold
              duration
              operator
              priority
            }
          }
        }
      }
    }
  }
}
```

**Tasks:**
- [ ] Implement GetAlertConditionsTool
- [ ] Display condition details including NRQL queries and thresholds
- [ ] Add tests

---

### Tool 5: Get Alert Violations/History
**Purpose:** View recent alert violations and incident history

**Proposed interface:**
```go
type GetAlertViolationsTool struct{}

func (t *GetAlertViolationsTool) Name() string {
    return "get_alert_violations"
}

func (t *GetAlertViolationsTool) Description() string {
    return "Get recent alert violations and incidents"
}

// Parameters:
// - policy_id: optional, filter by policy
// - duration: time range (default "24 hours")
// - status: optional filter (open, closed)
// - limit: default 20
```

**Tasks:**
- [ ] Research New Relic incidents API
- [ ] Implement GetAlertViolationsTool
- [ ] Format violations with timestamps, severity, and duration
- [ ] Add tests

---

### Tool 6: List Dashboards
**Purpose:** List all dashboards in the account

**Proposed interface:**
```go
type ListDashboardsTool struct{}

func (t *ListDashboardsTool) Name() string {
    return "list_dashboards"
}

func (t *ListDashboardsTool) Description() string {
    return "List all dashboards in your New Relic account"
}

// Parameters:
// - limit: maximum results (default 50)
```

**New Relic GraphQL Query:**
```graphql
query($accountId: Int!) {
  actor {
    account(id: $accountId) {
      dashboard: nrql(query: "SELECT uniques(dashboardName) FROM DashboardEvent SINCE 7 days ago LIMIT 100") {
        results
      }
    }
  }
}
```

**Tasks:**
- [ ] Implement ListDashboardsTool
- [ ] Format dashboard list with names and URLs
- [ ] Add tests

---

### Tool 7: Get Dashboard Data
**Purpose:** Query data from a specific dashboard or widget

**Proposed interface:**
```go
type GetDashboardDataTool struct{}

func (t *GetDashboardDataTool) Name() string {
    return "get_dashboard_data"
}

func (t *GetDashboardDataTool) Description() string {
    return "Get data from a specific dashboard's widgets"
}

// Parameters:
// - dashboard_name: required
// - duration: time range (default "1 hour")
```

**Tasks:**
- [ ] Research New Relic dashboard widgets API
- [ ] Implement GetDashboardDataTool
- [ ] Format widget data
- [ ] Add tests

---

### Tool 8: Query Distributed Traces
**Purpose:** Search and view distributed traces

**Proposed interface:**
```go
type QueryTracesTool struct{}

func (t *QueryTracesTool) Name() string {
    return "query_traces"
}

func (t *QueryTracesTool) Description() string {
    return "Search and query distributed traces"
}

// Parameters:
// - trace_id: optional, specific trace ID to look up
// - service_name: optional, filter by service
// - duration: time range (default "1 hour")
// - limit: default 10
// - error_only: boolean, only show traces with errors
```

**New Relic GraphQL Query:**
```graphql
query($accountId: Int!) {
  actor {
    account(id: $accountId) {
      trace: nrql(query: "SELECT traceId, duration, entity.name, error FROM Span SINCE 1 hour ago LIMIT 10") {
        results
      }
    }
  }
}
```

**Tasks:**
- [ ] Implement QueryTracesTool
- [ ] Support searching by trace ID
- [ ] Support filtering by service and errors
- [ ] Format trace details with spans and duration
- [ ] Add tests

---

### Tool 9: Get Trace Details (Waterfall View)
**Purpose:** Get detailed span information for a specific trace

**Proposed interface:**
```go
type GetTraceDetailsTool struct{}

func (t *GetTraceDetailsTool) Name() string {
    return "get_trace_details"
}

func (t *GetTraceDetailsTool) Description() string {
    return "Get detailed span waterfall for a specific trace ID"
}

// Parameters:
// - trace_id: required, the trace ID to analyze
```

**Tasks:**
- [ ] Implement GetTraceDetailsTool
- [ ] Format waterfall view with spans, timing, and attributes
- [ ] Add tests

---

### Tool 10: Get Infrastructure Metrics
**Purpose:** View infrastructure metrics for hosts, containers, or Kubernetes

**Proposed interface:**
```go
type GetInfrastructureMetricsTool struct{}

func (t *GetInfrastructureMetricsTool) Name() string {
    return "get_infrastructure_metrics"
}

func (t *GetInfrastructureMetricsTool) Description() string {
    return "Get infrastructure metrics for hosts, containers, or Kubernetes"
}

// Parameters:
// - hostname: optional, specific host
// - container_name: optional, specific container
// - cluster_name: optional, Kubernetes cluster
// - metric_type: cpu, memory, disk, network (default: all)
// - duration: time range (default "1 hour")
```

**Tasks:**
- [ ] Implement GetInfrastructureMetricsTool
- [ ] Support different entity types (host, container, pod)
- [ ] Format metrics with charts/tables
- [ ] Add tests

---

## 🚀 Enhancement: Log Tailing Feature (Priority: High)

**User Request:** Simulate a `tail -f` like command for logs

**Proposed Solution:** Add `follow` parameter to NRQL queries or create new tool

### Option A: Add `follow` parameter to `nrql_query`

```go
// Add to NRQLQueryTool schema:
"follow": map[string]interface{}{
    "type":        "boolean",
    "description": "Continuously poll for new results (simulates tail -f)",
    "default":     false,
}

// Add:
"poll_interval": map[string]interface{}{
    "type":        "number",
    "description": "Polling interval in seconds when following (default 5)",
    "default":     5,
}
```

**Implementation approach:**
Since MCP is request/response based, we can't truly stream. However, we can:
1. Add `follow` mode that returns results + a "cursor" or timestamp
2. Client calls again with `since` parameter to get next batch
3. Or, we implement short-polling with multiple result batches

**Alternative - New Tool: `tail_logs`**
```go
type TailLogsTool struct{}

func (t *TailLogsTool) Name() string {
    return "tail_logs"
}

func (t *TailLogsTool) Description() string {
    return "Tail logs in real-time (returns latest logs, use with polling)"
}

// Parameters:
// - query: log filter (e.g., "service:mystique level:ERROR")
// - since: timestamp to start from (optional, for continuing tail)
// - limit: number of lines (default 50)
// - include_timestamp: boolean
```

**Tasks:**
- [ ] Decide on implementation approach (follow param vs new tool)
- [ ] Implement log tailing functionality
- [ ] Handle pagination and cursors
- [ ] Add tests
- [ ] Document recommended polling pattern for clients

---

## 📋 Implementation Order

### Phase 1: Critical Fixes
1. Fix `search_logs` tool syntax issues
2. Fix `get_apm_metrics` pool error or add better error handling
3. Add `list_applications` tool (core discovery)

### Phase 2: Core Metrics & Logs
4. Add `get_application_metrics` (comprehensive APM metrics)
5. Implement log tailing feature
6. Add `get_transaction_traces`

### Phase 3: Alerts & Monitoring
7. Add `get_alert_conditions`
8. Add `get_alert_violations`

### Phase 4: Tracing & Infrastructure
9. Add `query_traces`
10. Add `get_trace_details`
11. Add `get_infrastructure_metrics`

### Phase 5: Dashboards
12. Add `list_dashboards`
13. Add `get_dashboard_data`

---

## 🧪 Testing Strategy

For each tool:
- [ ] Unit tests with mocked New Relic API responses
- [ ] Integration tests against real API (if possible)
- [ ] Error handling tests (invalid parameters, API errors)
- [ ] Documentation with example usage

---

## 📝 Documentation Updates

- [ ] Update main README with new tools
- [ ] Create USAGE.md with examples for each tool
- [ ] Document query syntax for `search_logs`
- [ ] Add troubleshooting guide
- [ ] Document log tailing patterns

---

## 📊 Success Criteria

- All currently broken tools are functional
- At least 10 new read-only tools added
- Log tailing feature working with clear usage pattern
- Comprehensive test coverage (>80%)
- Updated documentation

---

## 🔄 Maintenance

- [ ] Monitor for New Relic API changes
- [ ] Keep dependencies updated
- [ ] Review and update based on user feedback
