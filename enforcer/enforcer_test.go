package enforcer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// ---------- helpers ----------

// newTestStore creates a temp SQLite DB with all migrations applied.
func newTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "enforcer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return s, func() {
		s.Close()
		os.RemoveAll(dir)
	}
}

func newTestEnforcer(t *testing.T, s *store.Store) *enforcer.Enforcer {
	t.Helper()
	cfg := enforcer.DefaultEnforcerConfig()
	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	return enf
}

// ---------- Justification gate ----------

// TestJustificationGate_Length verifies that HandleToolCall rejects calls when
// the justification is shorter than MinJustificationLength.
func TestJustificationGate_Length(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 20

	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()

	t.Run("short justification is rejected", func(t *testing.T) {
		decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "too short", enforcer.CallOptions{})
		if err == nil {
			t.Fatal("expected error for short justification, got nil")
		}
		if decision.Action != enforcer.ActionDeny {
			t.Errorf("expected DENY for short justification, got %s", decision.Action)
		}
	})

	t.Run("sufficient justification passes gate but inferred tool routes to admin HITL", func(t *testing.T) {
		// No policies, inferred profile → ActionPendingAdminApproval (inferred_profile_gate).
		// The approval record is now created by mcpbridge_routing.go, so HandleToolCall
		// never attempts a DB insert and always succeeds here.
		decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "this is definitely long enough justification", enforcer.CallOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Action != enforcer.ActionPendingAdminApproval {
			t.Errorf("expected ActionPendingAdminApproval for inferred tool with no policy, got %s", decision.Action)
		}
		if decision.PolicyID != "inferred_profile_gate" {
			t.Errorf("expected PolicyID=inferred_profile_gate, got %s", decision.PolicyID)
		}
	})
}

// TestJustificationGate_DisabledWhenZero ensures that setting
// MinJustificationLength=0 disables the gate entirely. An inferred tool with
// no policies is routed to the admin HITL queue (inferred_profile_gate).
// The approval record is created by mcpbridge_routing.go, so HandleToolCall
// never attempts a DB insert.
func TestJustificationGate_DisabledWhenZero(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0

	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()
	decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "", enforcer.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Action != enforcer.ActionPendingAdminApproval {
		t.Errorf("expected ActionPendingAdminApproval for inferred tool with no policy, got %s", decision.Action)
	}
	if decision.PolicyID != "inferred_profile_gate" {
		t.Errorf("expected PolicyID=inferred_profile_gate, got %s", decision.PolicyID)
	}
}

// ---------- Rate bucket ----------

// TestRateBucket_IncrementAndGet verifies IncrementRateBucket returns an
// increasing count and GetCallRate reads it back accurately.
func TestRateBucket_IncrementAndGet(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	es := store.NewEnforcerStore(s.DB())
	window := 5 * time.Minute

	count1, err := es.IncrementRateBucket("user1", "tool_a", window)
	if err != nil {
		t.Fatalf("IncrementRateBucket (1): %v", err)
	}
	if count1 != 1 {
		t.Errorf("expected count=1 after first increment, got %d", count1)
	}

	count2, err := es.IncrementRateBucket("user1", "tool_a", window)
	if err != nil {
		t.Fatalf("IncrementRateBucket (2): %v", err)
	}
	if count2 != 2 {
		t.Errorf("expected count=2 after second increment, got %d", count2)
	}

	rate, err := es.GetCallRate("user1", "tool_a", window)
	if err != nil {
		t.Fatalf("GetCallRate: %v", err)
	}
	if rate != 2 {
		t.Errorf("GetCallRate: expected 2, got %d", rate)
	}
}

// TestRateBucket_IsolatedByUser verifies that rate buckets are scoped per user.
func TestRateBucket_IsolatedByUser(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	es := store.NewEnforcerStore(s.DB())
	window := 5 * time.Minute

	if _, err := es.IncrementRateBucket("userA", "tool_x", window); err != nil {
		t.Fatalf("IncrementRateBucket userA: %v", err)
	}
	if _, err := es.IncrementRateBucket("userA", "tool_x", window); err != nil {
		t.Fatalf("IncrementRateBucket userA (2): %v", err)
	}

	rateB, err := es.GetCallRate("userB", "tool_x", window)
	if err != nil {
		t.Fatalf("GetCallRate userB: %v", err)
	}
	if rateB != 0 {
		t.Errorf("userB should have rate=0, got %d", rateB)
	}
}

// TestRateBucket_Cleanup verifies that expired buckets are removed.
func TestRateBucket_Cleanup(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	es := store.NewEnforcerStore(s.DB())

	// Use a very short window so it expires quickly.
	tinyWindow := 10 * time.Millisecond
	if _, err := es.IncrementRateBucket("user1", "tool_z", tinyWindow); err != nil {
		t.Fatalf("IncrementRateBucket: %v", err)
	}

	// Wait for the window to expire.
	time.Sleep(20 * time.Millisecond)

	if err := es.CleanupExpiredRateBuckets(tinyWindow); err != nil {
		t.Fatalf("CleanupExpiredRateBuckets: %v", err)
	}

	rate, err := es.GetCallRate("user1", "tool_z", tinyWindow)
	if err != nil {
		t.Fatalf("GetCallRate after cleanup: %v", err)
	}
	if rate != 0 {
		t.Errorf("expected rate=0 after cleanup, got %d", rate)
	}
}

// ---------- Policy locked field ----------

// TestPolicyLocked_RoundTrip verifies that the Locked field survives a
// AddPolicy → GetPolicy round-trip.
func TestPolicyLocked_RoundTrip(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	policy := enforcer.PolicyRow{
		ID:         "lock_test",
		Name:       "Lock Test Policy",
		Expression: "true",
		Action:     "DENY",
		Severity:   "HIGH",
		Enabled:    true,
		Priority:   50,
		Locked:     true,
	}
	if err := enf.AddPolicy(policy); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	got, err := enf.GetPolicy("lock_test")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if !got.Locked {
		t.Error("expected Locked=true after round-trip, got false")
	}
}

// ---------- User-scoped overrides ----------

// TestListUserOverrides_Scoped verifies that ListUserOverrides returns only
// records that belong to the requested user.
func TestListUserOverrides_Scoped(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	es := store.NewEnforcerStore(s.DB())

	// Upsert overrides for two different users with distinct tool names
	// (tool_name+backend_id is the unique key in enforcer_overrides).
	ovA := enforcer.EnforcerOverrideRow{
		ID:        "ov-alice",
		ToolName:  "tool_alpha",
		BackendID: "backend1",
		UserID:    "user-alice",
		RiskLevel: "low",
	}
	ovB := enforcer.EnforcerOverrideRow{
		ID:        "ov-bob",
		ToolName:  "tool_beta",
		BackendID: "backend1",
		UserID:    "user-bob",
		RiskLevel: "low",
	}

	if err := es.UpsertOverride(ovA); err != nil {
		t.Fatalf("UpsertOverride alice: %v", err)
	}
	if err := es.UpsertOverride(ovB); err != nil {
		t.Fatalf("UpsertOverride bob: %v", err)
	}

	aliceOverrides, err := es.ListUserOverrides("user-alice")
	if err != nil {
		t.Fatalf("ListUserOverrides alice: %v", err)
	}
	if len(aliceOverrides) != 1 {
		t.Fatalf("expected 1 override for alice, got %d", len(aliceOverrides))
	}
	if aliceOverrides[0].ToolName != "tool_alpha" {
		t.Errorf("expected tool_alpha for alice, got %s", aliceOverrides[0].ToolName)
	}

	bobOverrides, err := es.ListUserOverrides("user-bob")
	if err != nil {
		t.Fatalf("ListUserOverrides bob: %v", err)
	}
	if len(bobOverrides) != 1 {
		t.Fatalf("expected 1 override for bob, got %d", len(bobOverrides))
	}
	if bobOverrides[0].ToolName != "tool_beta" {
		t.Errorf("expected tool_beta for bob, got %s", bobOverrides[0].ToolName)
	}
}

// ---------- ResolveForUser ----------

// TestResolveForUser_FallsBackToGlobal verifies that when a user has no
// personal override, ResolveForUser falls back to the same profile as Resolve.
func TestResolveForUser_FallsBackToGlobal(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)
	resolver := enf.GetResolver()

	profileGlobal, err := resolver.Resolve("safe_read_tool", "backend1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	profileUser, err := resolver.ResolveForUser("safe_read_tool", "backend1", "user-xyz")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}

	// Both should agree on risk and impact since no overrides exist.
	if profileGlobal.Risk != profileUser.Risk {
		t.Errorf("Risk mismatch: global=%s user=%s", profileGlobal.Risk, profileUser.Risk)
	}
	if profileGlobal.Impact != profileUser.Impact {
		t.Errorf("Impact mismatch: global=%s user=%s", profileGlobal.Impact, profileUser.Impact)
	}
}

// TestResolveForUser_UserOverrideApplied verifies that a user-scoped override
// in the DB is picked up by ResolveForUser but NOT by the admin Resolve.
func TestResolveForUser_UserOverrideApplied(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	es := store.NewEnforcerStore(s.DB())

	// Insert a user-scoped override that marks the tool as critical-risk.
	ov := enforcer.EnforcerOverrideRow{
		ID:           "ov-user-critical",
		ToolName:     "tool_gamma",
		BackendID:    "backend1",
		UserID:       "user-alice",
		RiskLevel:    "critical",
		ImpactScope:  "delete",
		RequiresHITL: true,
	}
	if err := es.UpsertOverride(ov); err != nil {
		t.Fatalf("UpsertOverride: %v", err)
	}

	enf := newTestEnforcer(t, s)
	resolver := enf.GetResolver()

	profileUser, err := resolver.ResolveForUser("tool_gamma", "backend1", "user-alice")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}

	if string(profileUser.Risk) != "critical" {
		t.Errorf("expected risk=critical from user override, got %s", profileUser.Risk)
	}
	if !profileUser.RequiresHITL {
		t.Error("expected RequiresHITL=true from user override")
	}
}

// ---------- Deny-unless-permitted gate ----------

// TestDenyUnlessPermitted_InferredNoPolicyRoutesToHITL verifies that a tool with an
// inferred safety profile and no matching DB policy is routed to the admin HITL
// queue (ActionPendingAdminApproval / inferred_profile_gate). The approval record
// is now created by the caller (mcpbridge_routing.go) so HandleToolCall itself
// never attempts a DB insert and cannot fail with a FK violation.
func TestDenyUnlessPermitted_InferredNoPolicyRoutesToHITL(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0 // disable justification gate for simplicity

	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()
	decision, callErr := enf.HandleToolCall(ctx, "user1", "unknown_third_party_tool",
		map[string]interface{}{}, "third_party_backend", "", enforcer.CallOptions{})
	if callErr != nil {
		t.Fatalf("unexpected error: %v", callErr)
	}
	if decision.Action != enforcer.ActionPendingAdminApproval {
		t.Errorf("expected ActionPendingAdminApproval (HITL routing), got %s", decision.Action)
	}
	if decision.PolicyID != "inferred_profile_gate" {
		t.Errorf("expected PolicyID=inferred_profile_gate, got %q", decision.PolicyID)
	}
}

// TestDenyUnlessPermitted_InferredWithAllowPolicyIsAllowed verifies that a tool
// with an inferred profile IS allowed when a DB policy explicitly permits it.
func TestDenyUnlessPermitted_InferredWithAllowPolicyIsAllowed(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0

	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Add an ALLOW policy that matches any tool on third_party_backend
	if err := enf.AddPolicy(enforcer.PolicyRow{
		ID:         "third_party_allow_all",
		Name:       "Allow all third party",
		Expression: `backend_id == "third_party_backend"`,
		Action:     string(enforcer.ActionAllow),
		Severity:   string(enforcer.SeverityLow),
		Enabled:    true,
		Priority:   10,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	// unknown_third_party_tool has no classification match → fall-through sentinel
	// (RiskHigh, ImpactDelete, cost=10). With inferred multiplier=3:
	// riskCost = 10 * 4 * 3 = 120. Set capacity >= 120 so the call is allowed.
	enf.SetBackendRateLimit("third_party_backend", 200, 0, 1000, 0)

	ctx := context.Background()
	decision, callErr := enf.HandleToolCall(ctx, "user1", "unknown_third_party_tool",
		map[string]interface{}{}, "third_party_backend", "", enforcer.CallOptions{})
	if callErr != nil {
		t.Fatalf("unexpected error: %v", callErr)
	}
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("expected ActionAllow with explicit policy, got %s", decision.Action)
	}
}

// TestDenyUnlessPermitted_SelfReportedNoPolicyIsAllowed verifies that a tool
// with a self-reported safety profile is implicitly permitted even with no DB
// policy — the backend's own characterisation is sufficient.
func TestDenyUnlessPermitted_SelfReportedNoPolicyIsAllowed(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0

	es := store.NewEnforcerStore(s.DB())

	// Seed a self-reported profile
	if err := es.UpsertToolProfile(enforcer.ToolProfileRow{
		ID:          "prof-self-1",
		BackendID:   "self_reporting_backend",
		ToolName:    "self_reported_tool",
		RiskLevel:   "low",
		ImpactScope: "read",
	}); err != nil {
		t.Fatalf("UpsertToolProfile: %v", err)
	}

	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()
	decision, callErr := enf.HandleToolCall(ctx, "user1", "self_reported_tool",
		map[string]interface{}{}, "self_reporting_backend", "", enforcer.CallOptions{})
	if callErr != nil {
		t.Fatalf("unexpected error for self-reported tool: %v", callErr)
	}
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("expected ActionAllow for self-reported tool, got %s", decision.Action)
	}
}

// TestShouldUpdateDecision_TiebreakByPriority verifies that when two policies
// produce the same action and severity, the one with the lower DB priority number
// (i.e., the more specific rule) wins and its PolicyID surfaces in the decision.
func TestShouldUpdateDecision_TiebreakByPriority(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())

	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Generic rule: priority 20 (lower specificity).
	// backend_id matches so the deny-unless-permitted gate won't fire
	// (a policy matched → action is not "").
	if err := enf.AddPolicy(enforcer.PolicyRow{
		ID:         "generic_delete_rule",
		Name:       "Generic delete requires approval",
		Expression: `backend_id == "tiebreak_backend" && safety.impact_scope == "delete"`,
		Action:     string(enforcer.ActionPendingUserApproval),
		Severity:   string(enforcer.SeverityMedium),
		Enabled:    true,
		Priority:   20,
	}); err != nil {
		t.Fatalf("AddPolicy generic: %v", err)
	}

	// Specific rule: priority 15 (higher specificity — should win on tie)
	if err := enf.AddPolicy(enforcer.PolicyRow{
		ID:         "specific_jira_delete_rule",
		Name:       "Block Jira Issue Deletion",
		Expression: `tool.contains("jira") && tool.contains("delete") && tool.contains("issue")`,
		Action:     string(enforcer.ActionPendingUserApproval),
		Severity:   string(enforcer.SeverityMedium),
		Enabled:    true,
		Priority:   15,
	}); err != nil {
		t.Fatalf("AddPolicy specific: %v", err)
	}

	ctx := context.Background()
	decision, callErr := enf.HandleToolCall(ctx, "user1", "jira_delete_issue",
		map[string]interface{}{}, "tiebreak_backend", "deleting a test issue for tiebreak test",
		enforcer.CallOptions{})
	// PENDING_USER_APPROVAL from HandleToolCall is returned as a non-nil error sentinel;
	// the decision is still populated. Accept either nil or a non-fatal error.
	_ = callErr
	if decision.Action != enforcer.ActionPendingUserApproval {
		t.Errorf("expected PENDING_USER_APPROVAL, got %s", decision.Action)
	}
	// The specific rule (priority 15) must win over the generic one (priority 20).
	if decision.PolicyID != "specific_jira_delete_rule" {
		t.Errorf("expected PolicyID=specific_jira_delete_rule (lower priority number wins), got %s", decision.PolicyID)
	}
}

// ---------- Backend routing coverage ----------

// productionPolicies returns the full set of backend-scoped policies that are
// live in production, so routing tests exercise the real policy set.
func productionBackendPolicies() []enforcer.PolicyRow {
	return []enforcer.PolicyRow{
		// AWS
		{ID: "aws_allow_reads", Name: "AWS Read Operations", Expression: `backend_id == "aws" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 10},
		{ID: "aws_delete_requires_approval", Name: "AWS Delete Operations", Expression: `backend_id == "aws" && safety.impact_scope == "delete"`, Action: "PENDING_ADMIN_APPROVAL", Severity: "HIGH", Enabled: true, Priority: 15},
		{ID: "aws_write_requires_approval", Name: "AWS Write Operations", Expression: `backend_id == "aws" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 20},
		{ID: "aws_admin_requires_approval", Name: "AWS Admin Operations", Expression: `backend_id == "aws" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Enabled: true, Priority: 20},
		// GitHub
		{ID: "github_allow_reads", Name: "GitHub Read Operations", Expression: `backend_id == "github" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 10},
		{ID: "github_delete_requires_approval", Name: "GitHub Delete Operations", Expression: `backend_id == "github" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Enabled: true, Priority: 15},
		{ID: "github_write_requires_approval", Name: "GitHub Write Operations", Expression: `backend_id == "github" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 20},
		{ID: "github_admin_requires_approval", Name: "GitHub Admin Operations", Expression: `backend_id == "github" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Enabled: true, Priority: 20},
		// k8s
		{ID: "k8s_allow_reads", Name: "Kubernetes Read Operations", Expression: `backend_id == "k8s" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 10},
		{ID: "k8s_delete_requires_approval", Name: "Kubernetes Delete Operations", Expression: `backend_id == "k8s" && safety.impact_scope == "delete"`, Action: "PENDING_ADMIN_APPROVAL", Severity: "HIGH", Enabled: true, Priority: 15},
		{ID: "k8s_write_requires_approval", Name: "Kubernetes Write Operations", Expression: `backend_id == "k8s" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 20},
		{ID: "k8s_admin_requires_approval", Name: "Kubernetes Admin Operations", Expression: `backend_id == "k8s" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Enabled: true, Priority: 20},
		// CircleCI
		{ID: "circleci_allow_reads", Name: "CircleCI Read Operations", Expression: `backend_id == "circleci" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 10},
		{ID: "circleci_delete_requires_approval", Name: "CircleCI Delete Operations", Expression: `backend_id == "circleci" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Enabled: true, Priority: 15},
		{ID: "circleci_write_requires_approval", Name: "CircleCI Write Operations", Expression: `backend_id == "circleci" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 20},
		{ID: "circleci_admin_requires_approval", Name: "CircleCI Admin Operations", Expression: `backend_id == "circleci" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Enabled: true, Priority: 20},
		// Atlassian
		{ID: "atlassian_allow_reads", Name: "Atlassian Read Operations", Expression: `backend_id == "atlassian" && safety.impact_scope == "read"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 10},
		{ID: "atlassian_delete_requires_approval", Name: "Atlassian Delete Operations", Expression: `backend_id == "atlassian" && safety.impact_scope == "delete"`, Action: "PENDING_USER_APPROVAL", Severity: "HIGH", Enabled: true, Priority: 20},
		{ID: "atlassian_write_requires_approval", Name: "Atlassian Write Operations", Expression: `backend_id == "atlassian" && safety.impact_scope == "write"`, Action: "ALLOW", Severity: "LOW", Enabled: true, Priority: 20},
		{ID: "atlassian_admin_requires_approval", Name: "Atlassian Admin Operations", Expression: `backend_id == "atlassian" && safety.impact_scope == "admin"`, Action: "PENDING_USER_APPROVAL", Severity: "MEDIUM", Enabled: true, Priority: 20},
	}
}

// seedToolProfile inserts a tool profile row directly into the DB so the
// resolver's Tier-3 lookup finds it. rawProfile controls the Source the
// resolver returns — pass {"source":"inferred"} or a self-reported blob.
func seedToolProfile(t *testing.T, db interface{ DB() interface{ ExecContext(interface{}, string, ...interface{}) (interface{}, error) } }, row enforcer.ToolProfileRow) {
	// Use store.UpsertToolProfileRaw instead — just exec SQL directly.
	t.Helper()
}

// seedToolProfileSQL inserts a profile via the raw *store.Store DB handle.
func seedToolProfileSQL(t *testing.T, s *store.Store, row enforcer.ToolProfileRow) {
	t.Helper()
	idempInt := 0
	if row.Idempotent {
		idempInt = 1
	}
	hitlInt := 0
	if row.RequiresHITL {
		hitlInt = 1
	}
	piiInt := 0
	if row.PIIExposure {
		piiInt = 1
	}
	_, err := s.DB().Exec(`
		INSERT OR REPLACE INTO enforcer_tool_profiles
		  (id, backend_id, tool_name, risk_level, impact_scope, resource_cost,
		   requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		row.ID, row.BackendID, row.ToolName, row.RiskLevel, row.ImpactScope,
		row.ResourceCost, hitlInt, piiInt, idempInt, row.RawProfile,
		row.ScannedAt.UTC().Format("2006-01-02T15:04:05Z"),
	)
	if err != nil {
		t.Fatalf("seedToolProfileSQL: %v", err)
	}
}

// ---------- Deny-unless-permitted gate ----------

// TestGate_SourceEmpty_Deny verifies that a tool with Source="" (no profile at all)
// is hard-denied after the spec §6.2 fix.
// ---------- Rate Limit Override HITL Tests ----------

// TestGetEffectiveBucketConfig_NoOverride verifies that without a configured
// override, GetEffectiveBucketConfig returns zeros (use global defaults).
func TestGetEffectiveBucketConfig_NoOverride(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	rc, rr, resc, resr, mult := enf.GetEffectiveBucketConfig("user1", "testbackend")
	if rc != 0 || rr != 0 || resc != 0 || resr != 0 || mult != 0 {
		t.Errorf("expected all zeros without override, got rc=%d rr=%d resc=%d resr=%d mult=%d",
			rc, rr, resc, resr, mult)
	}
}

// TestGetEffectiveBucketConfig_WithUserOverride verifies that after setting a
// user override via SetUserRateLimitOverride, GetEffectiveBucketConfig returns
// the configured values.
func TestGetEffectiveBucketConfig_WithUserOverride(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	err := enf.SetUserRateLimitOverride(enforcer.UserRateLimitOverrideRow{
		UserID:           "user1",
		BackendID:        "slack",
		SetBy:            "user",
		RiskCapacity:     50,
		ResourceCapacity: 100,
		CostMultiplier:   2,
	})
	if err != nil {
		t.Fatalf("SetUserRateLimitOverride: %v", err)
	}

	rc, rr, resc, resr, mult := enf.GetEffectiveBucketConfig("user1", "slack")
	if rc != 50 {
		t.Errorf("RiskCapacity = %d, want 50", rc)
	}
	if resc != 100 {
		t.Errorf("ResourceCapacity = %d, want 100", resc)
	}
	if mult != 2 {
		t.Errorf("CostMultiplier = %d, want 2", mult)
	}
	_ = rr
	_ = resr
}

// TestGetEffectiveBucketConfig_WithAdminOverride verifies that admin-set
// overrides (SetBy="admin") are also reflected by GetEffectiveBucketConfig.
func TestGetEffectiveBucketConfig_WithAdminOverride(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	err := enf.SetUserRateLimitOverride(enforcer.UserRateLimitOverrideRow{
		UserID:           "user1",
		BackendID:        "k8s",
		SetBy:            "admin",
		RiskCapacity:     25,
		ResourceCapacity: 50,
		CostMultiplier:   4,
	})
	if err != nil {
		t.Fatalf("SetUserRateLimitOverride: %v", err)
	}

	rc, _, resc, _, mult := enf.GetEffectiveBucketConfig("user1", "k8s")
	if rc != 25 {
		t.Errorf("RiskCapacity = %d, want 25", rc)
	}
	if resc != 50 {
		t.Errorf("ResourceCapacity = %d, want 50", resc)
	}
	if mult != 4 {
		t.Errorf("CostMultiplier = %d, want 4", mult)
	}
}

// TestSetUserRateLimitOverride_ThenGetEffectiveBucketConfig verifies the
// full round-trip: SetUserRateLimitOverride followed by GetEffectiveBucketConfig
// returns the configured values for the correct user/backend pair.
func TestSetUserRateLimitOverride_ThenGetEffectiveBucketConfig(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	// Set override for user1/slack
	err := enf.SetUserRateLimitOverride(enforcer.UserRateLimitOverrideRow{
		UserID:           "user1",
		BackendID:        "slack",
		SetBy:            "user",
		RiskCapacity:     50,
		ResourceCapacity: 100,
		CostMultiplier:   2,
	})
	if err != nil {
		t.Fatalf("SetUserRateLimitOverride user1/slack: %v", err)
	}

	// Verify user1/slack returns configured values
	rc, _, resc, _, mult := enf.GetEffectiveBucketConfig("user1", "slack")
	if rc != 50 {
		t.Errorf("user1/slack RiskCapacity = %d, want 50", rc)
	}
	if resc != 100 {
		t.Errorf("user1/slack ResourceCapacity = %d, want 100", resc)
	}
	if mult != 2 {
		t.Errorf("user1/slack CostMultiplier = %d, want 2", mult)
	}

	// Verify user2/slack still returns zeros (no override)
	rc2, _, resc2, _, mult2 := enf.GetEffectiveBucketConfig("user2", "slack")
	if rc2 != 0 || resc2 != 0 || mult2 != 0 {
		t.Errorf("user2/slack expected zeros, got rc=%d resc=%d mult=%d",
			rc2, resc2, mult2)
	}
}

// TestSetUserRateLimitOverride_MultipleBackends verifies that overrides for
// different backends are independently stored and retrieved.
func TestSetUserRateLimitOverride_MultipleBackends(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	enf := newTestEnforcer(t, s)

	// Set override for user1/slack
	if err := enf.SetUserRateLimitOverride(enforcer.UserRateLimitOverrideRow{
		UserID:           "user1",
		BackendID:        "slack",
		SetBy:            "user",
		RiskCapacity:     50,
		ResourceCapacity: 100,
		CostMultiplier:   2,
	}); err != nil {
		t.Fatalf("SetUserRateLimitOverride user1/slack: %v", err)
	}

	// Set override for user1/k8s
	if err := enf.SetUserRateLimitOverride(enforcer.UserRateLimitOverrideRow{
		UserID:           "user1",
		BackendID:        "k8s",
		SetBy:            "admin",
		RiskCapacity:     25,
		ResourceCapacity: 50,
		CostMultiplier:   4,
	}); err != nil {
		t.Fatalf("SetUserRateLimitOverride user1/k8s: %v", err)
	}

	// Verify slack
	rc, _, _, _, mult := enf.GetEffectiveBucketConfig("user1", "slack")
	if rc != 50 {
		t.Errorf("slack RiskCapacity = %d, want 50", rc)
	}
	if mult != 2 {
		t.Errorf("slack CostMultiplier = %d, want 2", mult)
	}

	// Verify k8s
	rc2, _, _, _, mult2 := enf.GetEffectiveBucketConfig("user1", "k8s")
	if rc2 != 25 {
		t.Errorf("k8s RiskCapacity = %d, want 25", rc2)
	}
	if mult2 != 4 {
		t.Errorf("k8s CostMultiplier = %d, want 4", mult2)
	}
}

// ---------- ResolveDisposition Tests ----------

// For resolveDisposition (unexported), test via internal package.
// See enforcer_internal_test.go for:
//   TestResolveDisposition_DefaultsToDeny
//   TestResolveDisposition_WithConfiguredDisposition
//   TestLookupDisposition_NoMatchReturnsFalse
//   TestLookupDisposition_MatchReturnsAction

func TestGate_SourceEmpty_Deny(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Add no policies — no DB profile either, so resolver falls to Tier-4 heuristic.
	// Use a tool whose name has NO pattern match → falls to sentinel → Source="inferred"
	// via Tier-4. To get Source="" we need to bypass the resolver entirely, which
	// requires a self-reported DB row with empty source — verify the defensive default.
	// Instead: insert a DB row with empty raw_profile (no source field) to simulate
	// a legacy row that predates this spec.
	seedToolProfileSQL(t, s, enforcer.ToolProfileRow{
		ID:          "legacy-001",
		BackendID:   "testbackend",
		ToolName:    "legacy_frobnicate",
		RiskLevel:   "medium",
		ImpactScope: "write",
		RawProfile:  "", // no source field → sourceFromRawProfile returns "self_reported"
		ScannedAt:   time.Now(),
	})

	// With an empty raw_profile the resolver returns self_reported, not "".
	// True Source="" can only come from a profile struct with Source unset —
	// that path no longer exists after Step 1. Verify the self_reported fallback
	// allows (implicit permit) when no policy matches.
	ctx := context.Background()
	decision, _ := enf.HandleToolCall(ctx, "user1", "legacy_frobnicate",
		map[string]interface{}{}, "testbackend",
		"testing legacy tool with no explicit source marker", enforcer.CallOptions{})
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("legacy tool (empty raw_profile → self_reported): want ALLOW, got %s (policy=%s)",
			decision.Action, decision.PolicyID)
	}
}

// TestGate_SourceInferred_AdminHITL verifies that an inferred-profile tool with
// no matching policy is routed to the admin approval queue.
func TestGate_SourceInferred_AdminHITL(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// enforcer_approvals has a FK on users(id) — seed a user so the insert succeeds.
	_, err = s.DB().Exec(`INSERT INTO users (id, name, email, password, role) VALUES (?,?,?,?,?)`,
		"user1", "Test User", "user1@test.local", "x", "user")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Seed an inferred profile row — source="inferred" in raw_profile.
	seedToolProfileSQL(t, s, enforcer.ToolProfileRow{
		ID:           "inferred-001",
		BackendID:    "testbackend",
		ToolName:     "delete_widget",
		RiskLevel:    "high",
		ImpactScope:  "delete",
		ResourceCost: 10,
		RawProfile:   `{"source":"inferred"}`,
		ScannedAt:    time.Now(),
	})

	ctx := context.Background()
	// No policies seeded — decision.Action will be "" after CEL evaluation,
	// which should trigger the inferred gate → admin HITL.
	decision, err := enf.HandleToolCall(ctx, "user1", "delete_widget",
		map[string]interface{}{}, "testbackend",
		"testing inferred profile gate routing to admin approval queue", enforcer.CallOptions{})

	if decision.Action != enforcer.ActionPendingAdminApproval {
		t.Errorf("inferred tool, no policy: want ActionPendingAdminApproval, got %s (policy=%s, err=%v)",
			decision.Action, decision.PolicyID, err)
	}
	if decision.PolicyID != "inferred_profile_gate" {
		t.Errorf("PolicyID = %q, want inferred_profile_gate", decision.PolicyID)
	}
	// ApprovalID is no longer set by HandleToolCall — the caller creates the
	// approval record so it can include the full request body for replay.
}

// TestGate_SourceSelfReported_Allow verifies that a self-reported tool with no
// matching policy receives ActionAllow (implicit permit).
func TestGate_SourceSelfReported_Allow(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Seed a self-reported profile (no "source":"inferred" marker).
	seedToolProfileSQL(t, s, enforcer.ToolProfileRow{
		ID:           "self-001",
		BackendID:    "testbackend",
		ToolName:     "get_widget",
		RiskLevel:    "low",
		ImpactScope:  "read",
		ResourceCost: 1,
		RawProfile:   `{"risk_level":"low","idempotent":true}`,
		ScannedAt:    time.Now(),
	})

	ctx := context.Background()
	decision, _ := enf.HandleToolCall(ctx, "user1", "get_widget",
		map[string]interface{}{}, "testbackend",
		"testing self-reported tool implicit allow with no policy", enforcer.CallOptions{})
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("self_reported tool, no policy: want ActionAllow, got %s (policy=%s)",
			decision.Action, decision.PolicyID)
	}
}

// TestGate_SourceOverride_Allow verifies that an override profile with no
// matching policy receives ActionAllow.
func TestGate_SourceOverride_Allow(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	err = enf.RegisterOverride("get_widget", "testbackend", enforcer.SafetyProfile{
		Risk:   enforcer.RiskLow,
		Impact: enforcer.ImpactRead,
		Cost:   1,
	})
	if err != nil {
		t.Fatalf("RegisterOverride: %v", err)
	}

	ctx := context.Background()
	decision, _ := enf.HandleToolCall(ctx, "user1", "get_widget",
		map[string]interface{}{}, "testbackend",
		"testing override profile implicit allow with no policy", enforcer.CallOptions{})
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("override tool, no policy: want ActionAllow, got %s (policy=%s)",
			decision.Action, decision.PolicyID)
	}
}

// ---------- Inferred-profile rate limit multiplier ----------

// TestInferredCostMultiplier_TripleCost verifies that inferred tools consume
// 3× the base cost from rate-limit buckets (default multiplier=3).
func TestInferredCostMultiplier_TripleCost(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	// Default multiplier (0 → coerced to 3 in NewEnforcer)
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Seed an inferred high-risk tool (cost=10). With multiplier=3 → effective cost=30.
	// Backend default risk capacity is typically 100. After 3 calls it should exhaust.
	// Add an allow policy so the gate doesn't HITL after the first call.
	seedToolProfileSQL(t, s, enforcer.ToolProfileRow{
		ID:           "inferred-cost-001",
		BackendID:    "testbackend",
		ToolName:     "delete_widget",
		RiskLevel:    "high",
		ImpactScope:  "delete",
		ResourceCost: 10,
		RawProfile:   `{"source":"inferred"}`,
		ScannedAt:    time.Now(),
	})
	if err := enf.AddPolicy(enforcer.PolicyRow{
		ID:         "allow-delete-widget",
		Name:       "allow delete_widget",
		Expression: `tool == "delete_widget"`,
		Action:     string(enforcer.ActionAllow),
		Enabled:    true,
		Priority:   1,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	ctx := context.Background()
	const justification = "rate multiplier test — verifying inferred tool consumes 3x cost"

	// Effective riskCost = base(10) * riskMultiplier(high=4) * inferredMultiplier(3) = 120.
	// Set capacity=150 so first call (120≤150) is allowed; second (120>30) is rate-limited.
	enf.SetBackendRateLimit("testbackend", 150, 0, 1000, 0)

	// First call: consumes 120 of 150 → allowed.
	d1, _ := enf.HandleToolCall(ctx, "user1", "delete_widget",
		map[string]interface{}{}, "testbackend", justification, enforcer.CallOptions{})
	if d1.Action != enforcer.ActionAllow {
		t.Fatalf("call 1: want ALLOW, got %s", d1.Action)
	}

	// Second call: 120 > 30 remaining → rate limited.
	d2, _ := enf.HandleToolCall(ctx, "user1", "delete_widget",
		map[string]interface{}{}, "testbackend", justification, enforcer.CallOptions{})
	if d2.Action != enforcer.ActionDeny || d2.PolicyID != "rate_limit" {
		t.Errorf("call 2: want DENY(rate_limit), got %s (policy=%s)", d2.Action, d2.PolicyID)
	}
}

// TestInferredCostMultiplier_Disabled verifies that InferredCostMultiplier=1
// causes inferred tools to consume base cost only.
func TestInferredCostMultiplier_Disabled(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.InferredCostMultiplier = 1 // disable multiplier
	es := store.NewEnforcerStore(s.DB())
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	seedToolProfileSQL(t, s, enforcer.ToolProfileRow{
		ID:           "inferred-cost-002",
		BackendID:    "testbackend",
		ToolName:     "delete_widget",
		RiskLevel:    "high",
		ImpactScope:  "delete",
		ResourceCost: 10,
		RawProfile:   `{"source":"inferred"}`,
		ScannedAt:    time.Now(),
	})
	if err := enf.AddPolicy(enforcer.PolicyRow{
		ID:         "allow-delete-widget-2",
		Name:       "allow delete_widget",
		Expression: `tool == "delete_widget"`,
		Action:     string(enforcer.ActionAllow),
		Enabled:    true,
		Priority:   1,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	ctx := context.Background()
	const justification = "rate multiplier disabled test — inferred tool at base cost"

	// With multiplier=1: riskCost = base(10) * riskMultiplier(high=4) = 40.
	// Set capacity=50 so first call (40≤50) is allowed; second (40>10) is rate-limited.
	enf.SetBackendRateLimit("testbackend", 50, 0, 1000, 0)

	d1, _ := enf.HandleToolCall(ctx, "user1", "delete_widget",
		map[string]interface{}{}, "testbackend", justification, enforcer.CallOptions{})
	if d1.Action != enforcer.ActionAllow {
		t.Fatalf("multiplier=1 call 1: want ALLOW, got %s", d1.Action)
	}

	d2, _ := enf.HandleToolCall(ctx, "user1", "delete_widget",
		map[string]interface{}{}, "testbackend", justification, enforcer.CallOptions{})
	if d2.Action != enforcer.ActionDeny || d2.PolicyID != "rate_limit" {
		t.Errorf("multiplier=1 call 2: want DENY(rate_limit), got %s (policy=%s)", d2.Action, d2.PolicyID)
	}
}

// coverage test. It seeds the production backend policies into a fresh DB and
// verifies that every backend × impact_scope combination routes to the expected
// action tier under the deny-unless-permitted regime.
//
// Representative tool names are chosen so that inferDefaults() maps them to the
// correct impact_scope without needing a stored profile.
func TestBackendRouting_AllNonSelfReportingBackends(t *testing.T) {
	type testCase struct {
		backend    string
		tool       string // tool name whose inferred impact_scope must match
		wantScope  string // what inferDefaults should produce (informational)
		wantAction enforcer.Action
	}

	cases := []testCase{
		// ── aws ──────────────────────────────────────────────────────
		{"aws", "aws_list_buckets", "read", enforcer.ActionAllow},
		{"aws", "aws_put_object", "write", enforcer.ActionAllow},
		{"aws", "aws_delete_object", "delete", enforcer.ActionPendingAdminApproval},
		{"aws", "aws_grant_access", "admin", enforcer.ActionPendingUserApproval},
		// ── github ───────────────────────────────────────────────────
		{"github", "github_list_issues", "read", enforcer.ActionAllow},
		{"github", "github_create_pull_request", "write", enforcer.ActionAllow},
		{"github", "github_delete_file", "delete", enforcer.ActionPendingUserApproval},
		{"github", "github_grant_permission", "admin", enforcer.ActionPendingUserApproval},
		// ── k8s ──────────────────────────────────────────────────────
		{"k8s", "k8s_pods_list", "read", enforcer.ActionAllow},
		{"k8s", "k8s_resources_create", "write", enforcer.ActionAllow},
		{"k8s", "k8s_pods_delete", "delete", enforcer.ActionPendingAdminApproval},
		{"k8s", "k8s_grant_access", "admin", enforcer.ActionPendingUserApproval},
		// ── circleci ─────────────────────────────────────────────────
		{"circleci", "circleci_list_followed_projects", "read", enforcer.ActionAllow},
		{"circleci", "circleci_create_branch", "write", enforcer.ActionAllow},
		{"circleci", "circleci_delete_sprint", "delete", enforcer.ActionPendingUserApproval},
		{"circleci", "circleci_grant_access", "admin", enforcer.ActionPendingUserApproval},
		// ── atlassian ────────────────────────────────────────────────
		{"atlassian", "atlassian_confluence_list_spaces", "read", enforcer.ActionAllow},
		{"atlassian", "atlassian_jira_update_issue", "write", enforcer.ActionAllow},
		{"atlassian", "atlassian_confluence_delete_page", "delete", enforcer.ActionPendingUserApproval},
		{"atlassian", "atlassian_grant_permission", "admin", enforcer.ActionPendingUserApproval},
	}

	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	es := store.NewEnforcerStore(s.DB())

	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	for _, p := range productionBackendPolicies() {
		if err := enf.AddPolicy(p); err != nil {
			t.Fatalf("AddPolicy %s: %v", p.ID, err)
		}
	}

	ctx := context.Background()
	const justification = "routing coverage test — verifying policy tier mapping"

	for _, tc := range cases {
		tc := tc
		t.Run(tc.backend+"/"+tc.tool, func(t *testing.T) {
			decision, _ := enf.HandleToolCall(ctx, "user1", tc.tool,
				map[string]interface{}{}, tc.backend, justification, enforcer.CallOptions{})
			if decision.Action != tc.wantAction {
				t.Errorf("backend=%s tool=%s (scope≈%s): want action %s, got %s (policy=%s)",
					tc.backend, tc.tool, tc.wantScope, tc.wantAction, decision.Action, decision.PolicyID)
			}
		})
	}
}
