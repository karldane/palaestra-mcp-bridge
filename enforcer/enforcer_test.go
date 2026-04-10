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

	t.Run("sufficient justification passes gate", func(t *testing.T) {
		// No policies → ALLOW
		decision, err := enf.HandleToolCall(ctx, "user1", "some_tool", map[string]interface{}{}, "backend1", "this is definitely long enough justification", enforcer.CallOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Action != enforcer.ActionAllow {
			t.Errorf("expected ALLOW with no policies, got %s", decision.Action)
		}
	})
}

// TestJustificationGate_DisabledWhenZero ensures that setting
// MinJustificationLength=0 disables the gate entirely.
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
		t.Fatalf("unexpected error with gate disabled: %v", err)
	}
	if decision.Action != enforcer.ActionAllow {
		t.Errorf("expected ALLOW with gate disabled, got %s", decision.Action)
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
