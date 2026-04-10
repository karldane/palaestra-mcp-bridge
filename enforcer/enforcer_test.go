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

	t.Run("sufficient justification passes gate but inferred tool is denied", func(t *testing.T) {
		// No policies, inferred profile → DENY (no_explicit_permit)
		decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "this is definitely long enough justification", enforcer.CallOptions{})
		if err == nil {
			t.Fatal("expected error for inferred tool with no policy, got nil")
		}
		if decision.Action != enforcer.ActionDeny {
			t.Errorf("expected DENY for inferred tool with no policy, got %s", decision.Action)
		}
		if decision.PolicyID != "no_explicit_permit" {
			t.Errorf("expected PolicyID=no_explicit_permit, got %s", decision.PolicyID)
		}
	})
}

// TestJustificationGate_DisabledWhenZero ensures that setting
// MinJustificationLength=0 disables the gate entirely. An inferred tool with
// no policies is still hard-denied by the no_explicit_permit gate.
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
	// Justification gate is off, but inferred tool with no policy → DENY (no_explicit_permit)
	decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "", enforcer.CallOptions{})
	if err == nil {
		t.Fatal("expected error for inferred tool with no policy, got nil")
	}
	if decision.Action != enforcer.ActionDeny {
		t.Errorf("expected DENY (no_explicit_permit), got %s", decision.Action)
	}
	if decision.PolicyID != "no_explicit_permit" {
		t.Errorf("expected PolicyID=no_explicit_permit, got %s", decision.PolicyID)
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

// TestDenyUnlessPermitted_InferredNoPolicyIsDenied verifies that a tool with an
// inferred safety profile and no matching DB policy is hard-denied with
// PolicyID="no_explicit_permit".
func TestDenyUnlessPermitted_InferredNoPolicyIsDenied(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0 // disable justification gate for simplicity

	enf, err := enforcer.NewEnforcer(cfg, store.NewEnforcerStore(s.DB()), nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()
	// "unknown_third_party_tool" has no stored profile → inferred source
	decision, callErr := enf.HandleToolCall(ctx, "user1", "unknown_third_party_tool",
		map[string]interface{}{}, "third_party_backend", "", enforcer.CallOptions{})
	if callErr == nil {
		t.Fatal("expected error for inferred tool with no policy, got nil")
	}
	if decision.Action != enforcer.ActionDeny {
		t.Errorf("expected ActionDeny, got %s", decision.Action)
	}
	if decision.PolicyID != "no_explicit_permit" {
		t.Errorf("expected PolicyID=no_explicit_permit, got %q", decision.PolicyID)
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

// TestBackendRouting_AllNonSelfReportingBackends is a table-driven routing
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
		{"aws", "aws_configure_setting", "admin", enforcer.ActionPendingUserApproval},
		// ── github ───────────────────────────────────────────────────
		{"github", "github_list_issues", "read", enforcer.ActionAllow},
		{"github", "github_create_pull_request", "write", enforcer.ActionAllow},
		{"github", "github_delete_file", "delete", enforcer.ActionPendingUserApproval},
		{"github", "github_grant_permission", "admin", enforcer.ActionPendingUserApproval},
		// ── k8s ──────────────────────────────────────────────────────
		{"k8s", "k8s_pods_list", "read", enforcer.ActionAllow},
		{"k8s", "k8s_resources_create", "write", enforcer.ActionAllow},
		{"k8s", "k8s_pods_delete", "delete", enforcer.ActionPendingAdminApproval},
		{"k8s", "k8s_configure_setting", "admin", enforcer.ActionPendingUserApproval},
		// ── circleci ─────────────────────────────────────────────────
		{"circleci", "circleci_list_followed_projects", "read", enforcer.ActionAllow},
		{"circleci", "circleci_create_branch", "write", enforcer.ActionAllow},
		{"circleci", "circleci_delete_sprint", "delete", enforcer.ActionPendingUserApproval},
		{"circleci", "circleci_admin_config", "admin", enforcer.ActionPendingUserApproval},
		// ── atlassian ────────────────────────────────────────────────
		{"atlassian", "atlassian_confluence_list_spaces", "read", enforcer.ActionAllow},
		{"atlassian", "atlassian_jira_update_issue", "write", enforcer.ActionAllow},
		{"atlassian", "atlassian_confluence_delete_page", "delete", enforcer.ActionPendingUserApproval},
		{"atlassian", "atlassian_admin_configure", "admin", enforcer.ActionPendingUserApproval},
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
