# Rate Limit HITL & Dynamic Disposition Routing — Specification

**Status:** Draft  
**Date:** 2026-05-31  
**Scope:** `enforcer/`, `ratelimit/`, `store/`, admin UI, user UI, DB migrations

---

## 1. Background & Motivation

Currently, when either the risk or resource rate-limit bucket is exhausted for a user, `HandleToolCall` in `enforcer/enforcer.go` returns a hard `ActionDeny` directly from the `CheckAndConsume` failure path. This bypasses the HITL machinery that already exists for policy violations. The result is a flat refusal to the agent with no possibility of human intervention.

The desired behaviour is that **rate limit exhaustion is a routing signal, not an unconditional verdict**. Operators should be able to configure whether exhaustion results in a denial, user HITL, or admin HITL — using the same policy mechanism that already governs tool call decisions.

Additionally, the existing override system allows users and admins to tighten safety profiles for tools. This spec extends that model to cover **per-user rate limit tightening**, and ensures both the admin and user interfaces expose the new capabilities.

---

## 2. Goals

1. Rate limit exhaustion (risk bucket, resource bucket) can be routed to user HITL, admin HITL, or denial, via policy configuration.
2. The routing mechanism is **dynamic** — new match contexts and new disposition targets can be added without changes to the enforcer core logic.
3. Users may tighten their own rate limit parameters (reduce personal capacity or increase per-call cost multiplier), subject to a global ceiling constraint.
4. Admins may tighten rate limit parameters for any user.
5. Both admin and user UIs are updated to reflect these new capabilities.
6. Database schema is extended with backward-compatible migrations.
7. The approval replay path (poll-for-outcome) is unchanged — the existing `enforcer_approvals` + `request_body` + executor pattern handles HITL regardless of match context.

### Non-Goals

- Rate limit *loosening* by users is not permitted.
- Re-running `CheckAndConsume` at approval time for rate-limit-triggered HITL is not done — the bucket was already consumed at request time; re-checking would double-penalise the user.
- Changing the polling/approval endpoint contract (already implemented).

---

## 3. Concepts

### 3.1 Match Context

A `MatchContext` is a string tag describing *why* the enforcer is making a routing decision. Currently the enforcer has two implicit contexts baked into control flow: "a CEL policy matched" and "a rate limit bucket was exhausted". This spec makes them explicit typed values.

```go
type MatchContext string

const (
    MatchContextPolicyHit     MatchContext = "policy_hit"
    MatchContextRiskLimit     MatchContext = "risk_limit"
    MatchContextResourceLimit MatchContext = "resource_limit"
)
```

`MatchContext` is a plain `string` type. New contexts (e.g. `threat_signal`, `cost_ceiling`) can be added as constants without modifying any routing logic in the core.

### 3.2 Disposition

A `Disposition` maps a `MatchContext` to an `Action`. It is stored per-policy and consulted when that policy's match context fires.

`Action` is already `type Action string` in `enforcer/types.go`, so it is already open. The change here is replacing the hard-coded predicate functions (`RequiresAdminApproval`, `RequiresUserApproval`) with a **registration pattern**, so that new `Action` values can declare their own capabilities at init time.

### 3.3 Policy Disposition Map

Each `CELPolicy` gains an optional `Dispositions map[MatchContext]Action`. When a match context fires:

1. The enforcer looks for any enabled policy whose `Dispositions` contains an entry for that context.
2. The first matching policy (by `Priority ASC`) wins.
3. If no policy has a disposition for that context, the system falls back to `ActionDeny`, and the agent receives an error message indicating that no disposition policy exists.

This means the default behaviour (hard deny on rate limit exhaustion) is preserved without any policy configuration, and operators opt in to HITL by adding policies.

---

## 4. Go Interface Changes

### 4.1 `enforcer/types.go`

#### Add `MatchContext` type

```go
// MatchContext describes the trigger condition for a routing decision.
type MatchContext string

const (
    MatchContextPolicyHit     MatchContext = "policy_hit"
    MatchContextRiskLimit     MatchContext = "risk_limit"
    MatchContextResourceLimit MatchContext = "resource_limit"
)
```

#### Extend `CELPolicy`

```go
type CELPolicy struct {
    ID          string
    Description string
    Expression  string
    Action      Action
    Message     string
    Severity    SeverityLevel
    Enabled     bool
    Priority    int
    // Dispositions maps match contexts to actions for non-CEL trigger paths.
    // If nil, this policy does not participate in disposition routing.
    Dispositions map[MatchContext]Action
}
```

#### Extend `PolicyRow`

```go
type PolicyRow struct {
    // ... existing fields unchanged ...

    // DispositionsJSON stores the JSON-serialised map[MatchContext]Action.
    // Empty string means no dispositions configured.
    DispositionsJSON string
}
```

`ToCELPolicy()` deserialises `DispositionsJSON` into `CELPolicy.Dispositions`.

#### Extend `EnforcerDecision`

```go
type EnforcerDecision struct {
    // ... existing fields unchanged ...

    // MatchContext records what triggered this decision. Used by the UI
    // to show "rate limit exceeded" vs "policy matched" context.
    MatchContext MatchContext
}
```

#### Replace hard-coded predicate functions with a registry

```go
var (
    adminApprovalActions = map[Action]bool{}
    userApprovalActions  = map[Action]bool{}
    approvalActions      = map[Action]bool{}
    warningActions       = map[Action]bool{}
    denyActions          = map[Action]bool{}
)

func init() {
    RegisterAdminApprovalAction(ActionPendingApproval)
    RegisterAdminApprovalAction(ActionPendingAdminApproval)
    RegisterUserApprovalAction(ActionPendingUserApproval)
    RegisterDenyAction(ActionDeny)
    RegisterWarningAction(ActionWarn)
}

func RegisterAdminApprovalAction(a Action) {
    adminApprovalActions[a] = true
    approvalActions[a] = true
}

func RegisterUserApprovalAction(a Action) {
    userApprovalActions[a] = true
    approvalActions[a] = true
}

func RegisterDenyAction(a Action) { denyActions[a] = true }
func RegisterWarningAction(a Action) { warningActions[a] = true }

func IsDenyAction(a Action) bool          { return denyActions[a] }
func RequiresApproval(a Action) bool      { return approvalActions[a] }
func RequiresUserApproval(a Action) bool  { return userApprovalActions[a] }
func RequiresAdminApproval(a Action) bool { return adminApprovalActions[a] }
func RequiresWarning(a Action) bool       { return warningActions[a] }
```

All existing call sites compile unchanged; new `Action` values register themselves at `init()`.

#### Add `UserRateLimitOverride`

```go
// UserRateLimitOverride represents a user's personal rate limit tightening.
// Values here can only be ≤ the effective global values for the backend.
type UserRateLimitOverride struct {
    ID                   string
    UserID               string
    BackendID            string
    // RiskCapacity, if > 0, overrides the backend default. Must be ≤ global.
    RiskCapacity         int
    // ResourceCapacity, if > 0, overrides the backend default. Must be ≤ global.
    ResourceCapacity     int
    // CostMultiplier, if > 0, overrides the user's per-call cost multiplier.
    // Effective cap = Capacity / CostMultiplier must be ≥ 1.
    // Cannot produce an effective cap higher than global capacity.
    CostMultiplier       int
    CreatedAt            time.Time
    UpdatedAt            time.Time
}
```

The constraint is: `override.RiskCapacity * override.CostMultiplier <= globalRiskCapacity * globalCostMultiplier`, and similarly for resource. This ensures a user can never grant themselves *more* effective budget than the global default allows, regardless of how they combine the two levers.

---

### 4.2 `enforcer/enforcer.go`

#### New private method: `resolveDisposition`

```go
// resolveDisposition looks up a disposition action for the given match context
// across all loaded policies, ordered by Priority ASC.
// Returns ActionDeny and an explanatory message if no policy covers the context.
func (e *Enforcer) resolveDisposition(ctx MatchContext, backendID string) (Action, string) {
    // Walk policies in priority order; return first matching disposition.
    // Implementation queries e.engine loaded policies.
    // Falls back to ActionDeny with message:
    //   "Rate limit exceeded: no disposition policy configured for context '<ctx>'"
}
```

#### Modified rate limit block in `HandleToolCall`

Replace the current hard-deny block:

```go
// BEFORE (removed):
if !riskAllowed || !resourceAllowed {
    return EnforcerDecision{Action: ActionDeny, ...}, ErrRateLimitExceeded
}
```

With:

```go
// AFTER:
if !riskAllowed || !resourceAllowed {
    matchCtx := MatchContextRiskLimit
    if !resourceAllowed {
        matchCtx = MatchContextResourceLimit
    }
    action, msg := e.resolveDisposition(matchCtx, backendID)
    decision := EnforcerDecision{
        Action:       action,
        Severity:     SeverityMedium,
        Message:      msg,
        PolicyID:     "rate_limit_disposition",
        MatchContext: matchCtx,
        Timestamp:    time.Now(),
    }
    // If routed to HITL, do NOT re-consume buckets. Budget was already spent.
    return decision, nil
}
```

The caller (`mcpbridge_routing.go`) already handles `RequiresApproval(decision.Action)` to create the approval record; no change is needed there.

#### New method: `SetUserRateLimitOverride`

```go
func (e *Enforcer) SetUserRateLimitOverride(override UserRateLimitOverride) error {
    // 1. Fetch global config for backendID.
    // 2. Validate: override.RiskCapacity * override.CostMultiplier
    //              <= globalRiskCapacity * globalCostMultiplier
    //    (and same for resource).
    //    Return ErrOverrideTooPermissive if violated.
    // 3. Persist to enforcer_user_rate_overrides.
    // 4. Apply to in-memory RateLimitManager for the user.
}
```

#### New method: `GetEffectiveBucketConfig`

```go
// GetEffectiveBucketConfig returns the capacity and refill rate that will be
// applied for a given user+backend, after applying any personal override.
func (e *Enforcer) GetEffectiveBucketConfig(userID, backendID string) (riskCap, riskRefill, resCap, resRefill, costMultiplier int)
```

This is called by `GetBucketStatus` consumers (UI, CEL context) so they reflect the user's actual effective limits.

---

### 4.3 `enforcer/cel_engine.go`

`CELEngine.loadDispositions()` — a new internal method called after `AddPolicy` that rebuilds an in-memory `map[MatchContext][]prioritisedDisposition` used by `resolveDisposition`. This avoids scanning all policies on every rate-limit event.

---

### 4.4 `enforcer/EnforcerStore` interface additions

```go
// Rate limit override CRUD
UpsertUserRateLimitOverride(override UserRateLimitOverrideRow) error
GetUserRateLimitOverride(userID, backendID string) (UserRateLimitOverrideRow, error)
ListUserRateLimitOverrides(userID string) ([]UserRateLimitOverrideRow, error)
ListAllRateLimitOverrides() ([]UserRateLimitOverrideRow, error)
DeleteUserRateLimitOverride(userID, backendID string) error
```

---

## 5. Database Migrations

All changes are additive. The migration file should be numbered sequentially after the current highest in `store/`.

### 5.1 `enforcer_policies` — add `dispositions` column

```sql
ALTER TABLE enforcer_policies
    ADD COLUMN dispositions TEXT NOT NULL DEFAULT '';
```

`dispositions` stores a JSON object of `{ "match_context": "ACTION" }` pairs, e.g.:

```json
{
  "risk_limit": "PENDING_USER_APPROVAL",
  "resource_limit": "PENDING_ADMIN_APPROVAL"
}
```

Empty string `''` means no dispositions (policy participates in CEL evaluation only).

### 5.2 New table: `enforcer_user_rate_overrides`

```sql
CREATE TABLE IF NOT EXISTS enforcer_user_rate_overrides (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL,
    backend_id        TEXT NOT NULL,
    risk_capacity     INTEGER NOT NULL DEFAULT 0,   -- 0 = use global
    resource_capacity INTEGER NOT NULL DEFAULT 0,   -- 0 = use global
    cost_multiplier   INTEGER NOT NULL DEFAULT 0,   -- 0 = use global
    created_at        DATETIME NOT NULL,
    updated_at        DATETIME NOT NULL,
    UNIQUE(user_id, backend_id)
);

CREATE INDEX IF NOT EXISTS idx_user_rate_overrides_user
    ON enforcer_user_rate_overrides(user_id);
```

**Constraint enforcement:** The Go layer validates the ceiling constraint before writing. The DB stores raw values; validation is not at the SQL level (SQLite does not support cross-table CHECK constraints cleanly).

### 5.3 `enforcer_approvals` — add `match_context` column

```sql
ALTER TABLE enforcer_approvals
    ADD COLUMN match_context TEXT NOT NULL DEFAULT 'policy_hit';
```

This is stored when the approval record is created so that the UI can display "This request is queued because your risk budget was exhausted" vs. "This request is queued because a policy requires approval".

### 5.4 `store/enforcer_store.go` changes

- `CreatePolicy` / `UpdatePolicy` / `GetPolicy` / `ListPolicies`: include `dispositions` in all SQL and scanning.
- `CreateApprovalRequest`: include `match_context` in INSERT.
- `GetApprovalRequest` / list methods: include `match_context` in SELECT and scanning. Update `approvalColumns` constant.
- New methods for `enforcer_user_rate_overrides` CRUD.

---

## 6. `ratelimit/ratelimit.go` Changes

### 6.1 Per-user config in `BucketManager`

`BucketManager` currently uses a single config per `backendID`. Extend to support a per-`(userID, backendID)` config override that takes precedence over the backend default:

```go
type UserBucketConfig struct {
    UserID       string
    BackendID    string
    RiskCapacity    int
    RiskRefill      int
    ResCapacity     int
    ResRefill       int
    CostMultiplier  int // applied multiplicatively at consume time
}
```

Add to `BucketManager`:
```go
userConfigs map[string]*UserBucketConfig // key: userID:backendID
```

`GetOrCreate` checks `userConfigs[userID+":"+backendID]` before falling back to `config[backendID+":"+bucketType]`.

`CalculateCost` gains an optional per-user multiplier parameter:
```go
func CalculateCostWithMultiplier(resourceCost int, riskLevel, impactScope string, multiplier int) (riskCost, resourceCost int)
```

`HandleToolCall` calls `GetEffectiveBucketConfig` to retrieve the multiplier before computing costs.

### 6.2 `SetUserConfig` / `GetUserConfig`

```go
func (m *RateLimitManager) SetUserConfig(cfg UserBucketConfig)
func (m *RateLimitManager) GetUserConfig(userID, backendID string) (UserBucketConfig, bool)
func (m *RateLimitManager) DeleteUserConfig(userID, backendID string)
```

---

## 7. Admin Interface Changes

The admin interface should be updated in the following areas.

### 7.1 Policy Editor

The policy create/edit form gains a new **Dispositions** section, shown only when the operator wants this policy to participate in rate-limit routing (it is optional; CEL-only policies are unaffected).

**Fields:**
- `Risk Limit Action` — dropdown: `(none)`, `DENY`, `PENDING_USER_APPROVAL`, `PENDING_ADMIN_APPROVAL`
- `Resource Limit Action` — dropdown: same options

When saved, these are serialised to `dispositions` JSON on the `enforcer_policies` row.

**Validation:** At least one disposition must be set if the Dispositions section is expanded. An empty dispositions object is rejected.

**Policy list:** Add a `Dispositions` badge column showing configured contexts (e.g. `risk→user` `resource→admin`) as small pills next to the policy name.

### 7.2 Rate Limit Management Page (new or extended)

Extend the existing rate limit status page (currently read-only) with:

**Per-user override panel:**
- Table of all `enforcer_user_rate_overrides` rows with columns: User, Backend, Risk Cap, Resource Cap, Cost Multiplier, Actions (Edit / Delete).
- "Add Override" button opens a form with user picker, backend picker, and the three numeric fields.
- On save, the Go handler calls `enforcer.SetUserRateLimitOverride`, which validates the ceiling constraint and returns a user-visible error if violated (e.g. "Risk capacity × cost multiplier (300) exceeds global budget (200)").
- Admin may delete any override, which resets the user to global defaults.

**Global config display:** Show the effective `(capacity, refillRate)` for each backend × bucket type, same as today.

### 7.3 Approval Queue — Match Context Column

Both the admin pending approvals table and the approval detail view should display the `match_context` value with a human-readable label:

| `match_context` value | Display label |
|---|---|
| `policy_hit` | Policy Match |
| `risk_limit` | Risk Budget Exhausted |
| `resource_limit` | Resource Budget Exhausted |

The detail view shows the bucket state at the time of the request (already present in `safety_profile` JSON; the rate limit info is in `DecisionContext.RateLimit`, which should be serialised into `safety_profile` or a new `rate_limit_snapshot` field — see §8).

---

## 8. User Interface Changes

### 8.1 User Approval Queue

The existing user queue UI shows pending requests for the user to approve/deny. No structural change is needed, but:

- **Match context label** should appear on each request row and detail view (same labels as §7.3).
- The detail view copy for rate-limit contexts should read:  
  *"Your [risk/resource] budget was exhausted when this request was made. Approving will allow the tool to run immediately using the budget that was already spent."*
- No re-consumption of budget on approval (enforced in the executor path — no change needed).

### 8.2 User Rate Limit Self-Service Panel

Add a new section to the user settings / profile page: **"My Rate Limits"**.

**Display:**
- Table with one row per backend the user has accessed, showing:
  - Backend name
  - Effective Risk Cap (global or personal override, labelled)
  - Effective Resource Cap
  - Cost Multiplier
  - Current bucket levels (risk available / capacity, resource available / capacity)
  - "Edit" button (if the user has set an override) or "Set Personal Limit" button

**Edit / Set form:**
- Risk capacity field: number input, max = global risk capacity for backend, placeholder = global default.
- Resource capacity field: same.
- Cost multiplier field: number input ≥ 1, with live validation showing "Effective max calls ≈ N" computed as `floor(capacity / (baseCost × multiplier))`.
- Server-side validation applies the ceiling constraint. If violated, a field-level error is shown: *"This combination exceeds your allowed budget ceiling."*

**Restrictions:**
- Users cannot increase capacity above global.
- Users cannot set a cost multiplier below 1.
- The form is read-only for backends where the admin has set an override that is at or below global — the user sees the admin-set values with a note: *"Set by administrator."*
- A user's personal override is superseded by any admin override for the same `(userID, backendID)` pair that is more restrictive. The effective value is `min(adminOverride, userOverride, global)` for capacity, and `max(adminOverride, userOverride, global_multiplier)` for the multiplier.

---

## 9. Enforcer Flow — Revised `HandleToolCall` Sequence

```
 1. Kill switch check            → hard DENY if active
 2. Justification gate           → hard DENY if missing/short
 3. Resolve safety profile       → get profile.Source, profile.Cost etc.
 4. Apply cost multiplier        → riskCost, resourceCost
    (inferred multiplier × user cost multiplier)
 5. Populate RateLimit in ctx    → read-only bucket snapshot for CEL
 6. Increment call-rate bucket   → store.IncrementRateBucket
 7. CEL evaluate                 → decision (MatchContext = policy_hit)
 8. Deny-unless-permitted gate   → inferred profile → admin HITL; no profile → deny
 9. CheckAndConsume              → if allowed:
      a. riskAllowed, resourceAllowed = rateLimit.CheckAndConsume(...)
      b. if !riskAllowed:
           action, msg = resolveDisposition(risk_limit, backendID)
           return EnforcerDecision{Action: action, MatchContext: risk_limit, ...}
      c. if !resourceAllowed:
           action, msg = resolveDisposition(resource_limit, backendID)
           return EnforcerDecision{Action: action, MatchContext: resource_limit, ...}
10. Return decision
```

**Caller (`mcpbridge_routing.go`) — no change required:** The existing `RequiresApproval(decision.Action)` branch already handles all HITL actions by creating an approval record and returning the poll endpoint to the agent. The `match_context` field on `EnforcerDecision` is passed through to `RequestApproval` and stored on the `enforcer_approvals` row.

---

## 10. `RequestApproval` Signature Change

```go
// BEFORE:
func (e *Enforcer) RequestApproval(ctx context.Context, decisionCtx DecisionContext, policyID string, message string, queueType string) (string, error)

// AFTER:
func (e *Enforcer) RequestApproval(ctx context.Context, decisionCtx DecisionContext, policyID string, message string, queueType string, matchCtx MatchContext) (string, error)
```

`matchCtx` is stored on `ApprovalRequestRow.MatchContext` (maps to `enforcer_approvals.match_context`). Existing callers pass `MatchContextPolicyHit` for backward compatibility.

---

## 11. Error Messages to Agent

When `resolveDisposition` falls back to `ActionDeny` (no policy configured):

```
Rate limit exceeded: risk bucket exhausted (N available).
No disposition policy is configured for context 'risk_limit' — request denied.
To enable HITL routing for rate limit events, create a policy with a disposition for this context.
```

When routed to HITL:

```
Rate limit exceeded: risk bucket exhausted.
Your request has been queued for [user/admin] approval.
Poll for outcome: tool=mcp_bridge_check_approval, id=<approvalID>
```

---

## 12. Validation & Testing

### New unit tests required

| Package | Test | Description |
|---|---|---|
| `enforcer` | `TestResolveDisposition_NoPolicy` | Falls back to deny with correct message |
| `enforcer` | `TestResolveDisposition_UserHITL` | Routes to `PENDING_USER_APPROVAL` when policy configured |
| `enforcer` | `TestResolveDisposition_AdminHITL` | Routes to `PENDING_ADMIN_APPROVAL` when policy configured |
| `enforcer` | `TestResolveDisposition_Priority` | Lower-priority policy wins when two policies cover same context |
| `enforcer` | `TestSetUserRateLimitOverride_Valid` | Override within ceiling accepted |
| `enforcer` | `TestSetUserRateLimitOverride_CeilingViolation` | Override exceeding ceiling rejected |
| `enforcer` | `TestHandleToolCall_RiskLimitHITL` | Full flow: exhausted risk bucket → HITL action returned |
| `enforcer` | `TestHandleToolCall_ResourceLimitHITL` | Full flow: exhausted resource bucket → HITL action returned |
| `ratelimit` | `TestUserConfigOverride` | Per-user config takes precedence over backend default |
| `ratelimit` | `TestCostMultiplier` | Cost multiplier correctly scales bucket consumption |
| `store` | `TestUpsertUserRateLimitOverride` | Round-trips correctly |
| `store` | `TestPolicyDispositionsRoundTrip` | JSON deserialises correctly on `ToCELPolicy()` |

### Integration tests (existing `main_test.go` / `enforcer_test.go`)

- Add scenarios for rate-limit HITL in `enforcer_test.go`, mirroring existing approval flow tests but triggered via bucket exhaustion rather than a CEL policy match.

---

## 13. Migration Path & Backward Compatibility

- All DB changes are `ALTER TABLE ... ADD COLUMN ... DEFAULT ''` or new tables — no existing rows are broken.
- All new columns have sensible defaults (`dispositions = ''`, `match_context = 'policy_hit'`) so existing approval records and policies read correctly without re-migration.
- The `resolveDisposition` fallback is `ActionDeny` — the **existing behaviour is preserved** for deployments with no disposition policies configured.
- The action predicate registry is initialised via `init()` with all existing actions, so all existing call sites continue to compile and behave identically.
- `RequestApproval` gains a parameter — update all four call sites in `mcpbridge_routing.go` to pass `MatchContextPolicyHit`.

---

## 14. Open Questions

None at time of writing. All design decisions have been resolved:

- Rate limit HITL does **not** re-consume budget on approval (§2 Non-Goals).
- Global fallback is `ActionDeny` with an explanatory error (§3.3).
- User override constraint: `capacity × costMultiplier ≤ global capacity × global multiplier` (§4.1).
- Effective limit is `min(admin override, user override, global)` for capacity; `max` for multiplier (§8.2).
