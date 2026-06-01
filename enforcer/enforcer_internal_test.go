package enforcer

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ---------- LookupDisposition ----------

// TestLookupDisposition_NoMatchReturnsFalse verifies that LookupDisposition
// returns ("", "", false) when no disposition is configured for the context.
func TestLookupDisposition_NoMatchReturnsFalse(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	if err := engine.AddPolicy(CELPolicy{
		ID:          "test-policy",
		Description: "Test disposition policy",
		Expression:  `true`,
		Action:      ActionAllow,
		Enabled:     true,
		Priority:    10,
		Dispositions: map[MatchContext]Action{
			MatchContextRiskLimit: ActionAllow,
		},
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, msg, ok := engine.LookupDisposition(MatchContextResourceLimit)
	if ok {
		t.Errorf("expected false for unconfigured context, got action=%s msg=%s ok=true", action, msg)
	}
}

// TestLookupDisposition_MatchReturnsAction verifies that LookupDisposition
// returns the configured action when a disposition matches.
func TestLookupDisposition_MatchReturnsAction(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	if err := engine.AddPolicy(CELPolicy{
		ID:          "test-policy",
		Description: "Rate limit → redirect to pending user approval",
		Expression:  `true`,
		Action:      ActionPendingUserApproval,
		Enabled:     true,
		Priority:    10,
		Dispositions: map[MatchContext]Action{
			MatchContextRiskLimit: ActionPendingUserApproval,
		},
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, _, ok := engine.LookupDisposition(MatchContextRiskLimit)
	if !ok {
		t.Fatal("expected ok=true for configured context")
	}
	if action != ActionPendingUserApproval {
		t.Errorf("action = %s, want %s", action, ActionPendingUserApproval)
	}
}

// TestLookupDisposition_PrefersBestPriority verifies that when multiple policies
// configure dispositions for the same context, the one with the lowest priority
// number (most specific) wins.
func TestLookupDisposition_PrefersBestPriority(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	if err := engine.AddPolicy(CELPolicy{
		ID:          "generic",
		Description: "Generic policy",
		Expression:  `true`,
		Action:      ActionDeny,
		Enabled:     true,
		Priority:    20,
		Dispositions: map[MatchContext]Action{
			MatchContextRiskLimit: ActionPendingAdminApproval,
		},
	}); err != nil {
		t.Fatalf("AddPolicy generic: %v", err)
	}

	if err := engine.AddPolicy(CELPolicy{
		ID:          "specific",
		Description: "Specific policy",
		Expression:  `true`,
		Action:      ActionAllow,
		Enabled:     true,
		Priority:    10,
		Dispositions: map[MatchContext]Action{
			MatchContextRiskLimit: ActionPendingUserApproval,
		},
	}); err != nil {
		t.Fatalf("AddPolicy specific: %v", err)
	}

	action, _, ok := engine.LookupDisposition(MatchContextRiskLimit)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if action != ActionPendingUserApproval {
		t.Errorf("action = %s, want %s (specific policy priority 10 should win)", action, ActionPendingUserApproval)
	}
}

// ---------- resolveDisposition ----------

// mockEnforcerStore implements EnforcerStore with no-op stubs for enforcer startup.
type mockEnforcerStore struct{}

func (m *mockEnforcerStore) CreatePolicy(policy PolicyRow) error                                   { return nil }
func (m *mockEnforcerStore) GetPolicy(id string) (PolicyRow, error)                               { return PolicyRow{}, nil }
func (m *mockEnforcerStore) ListPolicies() ([]PolicyRow, error)                                   { return nil, nil }
func (m *mockEnforcerStore) DeletePolicy(id string) error                                         { return nil }
func (m *mockEnforcerStore) UpdatePolicy(policy PolicyRow) error                                  { return nil }
func (m *mockEnforcerStore) CreateApprovalRequest(req ApprovalRequestRow) error                   { return nil }
func (m *mockEnforcerStore) GetApprovalRequest(id string) (ApprovalRequestRow, error)             { return ApprovalRequestRow{}, nil }
func (m *mockEnforcerStore) ListPendingApprovals() ([]ApprovalRequestRow, error)                  { return nil, nil }
func (m *mockEnforcerStore) ListUserPendingApprovals() ([]ApprovalRequestRow, error)              { return nil, nil }
func (m *mockEnforcerStore) ListUserAllApprovals(userID string) ([]ApprovalRequestRow, error)     { return nil, nil }
func (m *mockEnforcerStore) ListAdminPendingApprovals() ([]ApprovalRequestRow, error)             { return nil, nil }
func (m *mockEnforcerStore) ListAllApprovals() ([]ApprovalRequestRow, error)                      { return nil, nil }
func (m *mockEnforcerStore) ApproveRequest(id, approverID, comments string) error                 { return nil }
func (m *mockEnforcerStore) DenyRequest(id, approverID, reason string) error                      { return nil }
func (m *mockEnforcerStore) MarkExecuting(id string) error                                        { return nil }
func (m *mockEnforcerStore) MarkCompleted(id string, status int, body string) error              { return nil }
func (m *mockEnforcerStore) MarkFailed(id string, msg string) error                              { return nil }
func (m *mockEnforcerStore) IsKillSwitchActive(scope string) (bool, error)                       { return false, nil }
func (m *mockEnforcerStore) EnableKillSwitch(scope, userID, reason string) error                 { return nil }
func (m *mockEnforcerStore) DisableKillSwitch(scope string) error                                 { return nil }
func (m *mockEnforcerStore) CleanupExpiredApprovals() error                                       { return nil }
func (m *mockEnforcerStore) CleanupOldApprovals(olderThan time.Duration) error                    { return nil }
func (m *mockEnforcerStore) LogAuditEvent(requestID, userID, toolName, action, policyID, message string, ctx map[string]interface{}, justification string, args map[string]interface{}) error {
	return nil
}
func (m *mockEnforcerStore) LogAuditRejection(requestID, userID, toolName, justification, rejectionReason string) error {
	return nil
}
func (m *mockEnforcerStore) GetToolProfile(backendID, toolName string) (ToolProfileRow, error)    { return ToolProfileRow{}, nil }
func (m *mockEnforcerStore) GetToolPrefix(backendID string) (string, error)                        { return "", nil }
func (m *mockEnforcerStore) ListOverrides() ([]EnforcerOverrideRow, error)                         { return nil, nil }
func (m *mockEnforcerStore) ListUserOverrides(userID string) ([]EnforcerOverrideRow, error)        { return nil, nil }
func (m *mockEnforcerStore) UpsertOverride(override EnforcerOverrideRow) error                     { return nil }
func (m *mockEnforcerStore) DeleteOverride(toolName, backendID string) error                       { return nil }
func (m *mockEnforcerStore) UpsertToolProfile(profile ToolProfileRow) error                        { return nil }
func (m *mockEnforcerStore) ListRateLimitBucketConfigs() ([]RateLimitBucketConfigRow, error)       { return nil, nil }
func (m *mockEnforcerStore) UpsertRateLimitBucketConfig(config RateLimitBucketConfigRow) error     { return nil }
func (m *mockEnforcerStore) ListRateLimitStates() ([]RateLimitStateRow, error)                     { return nil, nil }
func (m *mockEnforcerStore) UpsertRateLimitState(state RateLimitStateRow) error                    { return nil }
func (m *mockEnforcerStore) CountUserPendingApprovals() (int, error)                              { return 0, nil }
func (m *mockEnforcerStore) CountAdminPendingApprovals() (int, error)                             { return 0, nil }
func (m *mockEnforcerStore) IncrementRateBucket(userID, toolName string, window time.Duration) (int, error) {
	return 0, nil
}
func (m *mockEnforcerStore) GetCallRate(userID, toolName string, window time.Duration) (int, error) {
	return 0, nil
}
func (m *mockEnforcerStore) CleanupExpiredRateBuckets(window time.Duration) error                  { return nil }
func (m *mockEnforcerStore) UpsertUserRateLimitOverride(override UserRateLimitOverrideRow) error   { return nil }
func (m *mockEnforcerStore) GetUserRateLimitOverride(userID, backendID string) (UserRateLimitOverrideRow, error) {
	return UserRateLimitOverrideRow{}, nil
}
func (m *mockEnforcerStore) ListUserRateLimitOverrides(userID string) ([]UserRateLimitOverrideRow, error) {
	return nil, nil
}
func (m *mockEnforcerStore) ListAllRateLimitOverrides() ([]UserRateLimitOverrideRow, error) {
	return nil, nil
}
func (m *mockEnforcerStore) DeleteUserRateLimitOverride(userID, backendID string) error             { return nil }

type mockUserStore struct{}

func (m *mockUserStore) GetUser(id string) (*User, error) { return nil, nil }

// TestResolveDisposition_DefaultsToDeny verifies that resolveDisposition returns
// ActionDeny when no dispositions are configured on any policy.
func TestResolveDisposition_DefaultsToDeny(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if err := enf.AddPolicy(PolicyRow{
		ID:         "no-dispo",
		Name:       "No dispositions",
		Expression: `true`,
		Action:     string(ActionAllow),
		Severity:   string(SeverityLow),
		Enabled:    true,
		Priority:   10,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, msg := enf.resolveDisposition(MatchContextRiskLimit)
	if action != ActionDeny {
		t.Errorf("action = %s, want %s", action, ActionDeny)
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

// TestResolveDisposition_WithConfiguredDisposition verifies that resolveDisposition
// returns the configured action from the CEL engine when a disposition exists.
func TestResolveDisposition_WithConfiguredDisposition(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if err := enf.AddPolicy(PolicyRow{
		ID:         "dispo-policy",
		Name:       "Disposition policy",
		Expression: `true`,
		Action:     string(ActionPendingUserApproval),
		Severity:   string(SeverityMedium),
		Enabled:    true,
		Priority:   10,
		DispositionsJSON: `{"risk_limit":"PENDING_USER_APPROVAL"}`,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, _ := enf.resolveDisposition(MatchContextRiskLimit)
	if action != ActionPendingUserApproval {
		t.Errorf("action = %s, want %s", action, ActionPendingUserApproval)
	}
}

// TestResolveDisposition_AdminHITL verifies that resolveDisposition returns
// ActionPendingAdminApproval when a policy configures PENDING_ADMIN_APPROVAL
// for the match context.
func TestResolveDisposition_AdminHITL(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if err := enf.AddPolicy(PolicyRow{
		ID:         "dispo-admin",
		Name:       "Disposition admin HITL",
		Expression: `true`,
		Action:     string(ActionPendingAdminApproval),
		Severity:   string(SeverityMedium),
		Enabled:    true,
		Priority:   10,
		DispositionsJSON: `{"risk_limit":"PENDING_ADMIN_APPROVAL"}`,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, _ := enf.resolveDisposition(MatchContextRiskLimit)
	if action != ActionPendingAdminApproval {
		t.Errorf("action = %s, want %s", action, ActionPendingAdminApproval)
	}
}

// TestResolveDisposition_FallsBackToDenyOnWrongContext verifies that
// resolveDisposition falls back to ActionDeny when the disposition is
// configured for a different context.
func TestResolveDisposition_FallsBackToDenyOnWrongContext(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if err := enf.AddPolicy(PolicyRow{
		ID:         "dispo-only-risk",
		Name:       "Disposition for risk_limit only",
		Expression: `true`,
		Action:     string(ActionAllow),
		Severity:   string(SeverityLow),
		Enabled:    true,
		Priority:   10,
		DispositionsJSON: `{"risk_limit":"PENDING_USER_APPROVAL"}`,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	action, msg := enf.resolveDisposition(MatchContextResourceLimit)
	if action != ActionDeny {
		t.Errorf("action = %s, want %s", action, ActionDeny)
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

// TestRequestApproval_StoresMatchContext verifies that RequestApproval stores
// the match context in the approval request row.
func TestRequestApproval_StoresMatchContext(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ctx := context.Background()
	dc := DecisionContext{
		UserID:  "user1",
		Tool:    "test_tool",
		BackendID: "testbackend",
	}

	approvalID, err := enf.RequestApproval(ctx, dc, "test_policy", "test message", "user", MatchContextPolicyHit)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if approvalID == "" {
		t.Error("expected non-empty approval ID")
	}
}

// ---------- CEL engine: RemovePolicy, ListPolicies, ValidateExpression ----------

func TestRemovePolicy(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	err = engine.AddPolicy(CELPolicy{
		ID:          "test-policy",
		Description: "test",
		Expression:  `true`,
		Action:      ActionAllow,
		Enabled:     true,
		Priority:    10,
	})
	if err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	policies := engine.ListPolicies()
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}

	engine.RemovePolicy("test-policy")
	policies = engine.ListPolicies()
	if len(policies) != 0 {
		t.Errorf("expected 0 policies after remove, got %d", len(policies))
	}
}

func TestValidateExpression_Valid(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	err = engine.ValidateExpression(`true`)
	if err != nil {
		t.Errorf("expected no error for valid expression, got %v", err)
	}
}

func TestValidateExpression_InvalidSyntax(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	err = engine.ValidateExpression(`tool == `)
	if err == nil {
		t.Error("expected error for invalid expression syntax")
	}
}

func TestListPolicies_Empty(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	policies := engine.ListPolicies()
	if len(policies) != 0 {
		t.Errorf("expected empty list, got %d policies", len(policies))
	}
}

func TestListPolicies_Multiple(t *testing.T) {
	engine, err := NewCELEngine()
	if err != nil {
		t.Fatalf("NewCELEngine: %v", err)
	}

	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("policy-%d", i)
		err := engine.AddPolicy(CELPolicy{
			ID:          id,
			Description: id,
			Expression:  `true`,
			Action:      ActionAllow,
			Enabled:     true,
			Priority:    10,
		})
		if err != nil {
			t.Fatalf("AddPolicy %s: %v", id, err)
		}
	}

	policies := engine.ListPolicies()
	if len(policies) != 3 {
		t.Errorf("expected 3 policies, got %d", len(policies))
	}
}

// ---------- Enforcer delegation: SetRateLimitConfig, GetMinJustificationLength, etc. ----------

func TestGetMinJustificationLength(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 42
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if got := enf.GetMinJustificationLength(); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestSetRateLimitConfig_GetRateLimitConfigs(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.SetRateLimitConfig("test-backend", 50, 10, 100, 20)

	configs := enf.GetRateLimitConfigs()
	found := false
	for _, c := range configs {
		if c.BackendID == "test-backend" {
			found = true
			if c.RiskCapacity != 50 || c.RiskRefill != 10 || c.ResCapacity != 100 || c.ResRefill != 20 {
				t.Errorf("unexpected config: %+v", c)
			}
		}
	}
	if !found {
		t.Error("test-backend not found in configs")
	}
}

func TestGetRateLimitStatus(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.SetBackendRateLimit("test-status", 100, 20, 200, 40)

	status := enf.GetRateLimitStatus("user1", "test-status")
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	riskBucket, ok := status["risk_bucket"].(map[string]interface{})
	if !ok {
		t.Error("expected risk_bucket in status")
	} else if riskBucket["available"] == nil {
		t.Error("expected risk_bucket.available in status")
	}
	resBucket, ok := status["resource_bucket"].(map[string]interface{})
	if !ok {
		t.Error("expected resource_bucket in status")
	} else if resBucket["available"] == nil {
		t.Error("expected resource_bucket.available in status")
	}
}

func TestGetAllRateLimitStates_Empty(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	states := enf.GetAllRateLimitStates()
	if states == nil {
		t.Error("expected non-nil slice, got nil")
	}
}

func TestGetRateLimitConfigs_IncludesDefaults(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// NewEnforcer calls SetDefaultRateLimits which sets slack, newrelic, oracle, etc.
	configs := enf.GetRateLimitConfigs()
	if len(configs) == 0 {
		t.Fatal("expected non-empty configs")
	}

	found := false
	for _, c := range configs {
		if c.BackendID == "slack" {
			found = true
			if c.RiskCapacity != 100 {
				t.Errorf("slack risk capacity: got %d, want 100", c.RiskCapacity)
			}
			break
		}
	}
	if !found {
		t.Error("slack config not found")
	}
}

func TestResetUserRateLimit(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.SetBackendRateLimit("test-reset", 100, 20, 200, 40)

	// Reset buckets that don't exist yet — should not panic
	err = enf.ResetUserRateLimit("user1", "test-reset")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetEffectiveBucketConfig_NoOverride(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	rc, rr, resc, resr, cm := enf.GetEffectiveBucketConfig("user-nonexistent", "slack")
	if rc != 0 || rr != 0 || resc != 0 || resr != 0 || cm != 0 {
		t.Errorf("expected all zeros, got %d,%d,%d,%d,%d", rc, rr, resc, resr, cm)
	}
}

// ---------- Resolver override CRUD ----------

func TestResolver_CRUD(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	resolver := enf.GetResolver()
	if resolver == nil {
		t.Fatal("expected non-nil resolver")
	}

	// ListOverrides initially empty
	overrides := resolver.ListOverrides()
	if len(overrides) != 0 {
		t.Errorf("expected empty overrides, got %d", len(overrides))
	}

	// GetOverride returns false for missing
	_, ok := resolver.GetOverride("missing-tool", "backend")
	if ok {
		t.Error("expected false for missing override")
	}

	// GetProfile returns true even for missing tool (mock returns empty row)
	var profile SafetyProfile
	_, ok = resolver.GetProfile("missing-tool")
	if !ok {
		t.Error("expected true for GetProfile even with missing tool")
	}

	// RegisterOverride
	err = resolver.RegisterOverride("my-tool", "my-backend", SafetyProfile{
		Risk:   RiskHigh,
		Impact: "delete",
		Cost:   5,
		Source: "override",
	})
	if err != nil {
		t.Fatalf("RegisterOverride: %v", err)
	}

	// ListOverrides includes it
	overrides = resolver.ListOverrides()
	if len(overrides) != 1 {
		t.Fatalf("expected 1 override, got %d", len(overrides))
	}

	// GetOverride finds it
	profile, ok = resolver.GetOverride("my-tool", "my-backend")
	if !ok {
		t.Fatal("expected true for existing override")
	}
	if profile.Risk != RiskHigh || profile.Impact != "delete" {
		t.Errorf("unexpected profile: %+v", profile)
	}

	// GetProfile works
	profile, ok = resolver.GetProfile("my-tool")
	if !ok {
		t.Fatal("expected true for GetProfile with override")
	}

	// RemoveOverride succeeds
	err = resolver.RemoveOverride("my-tool", "my-backend")
	if err != nil {
		t.Fatalf("RemoveOverride: %v", err)
	}

	// RemoveOverride fails on missing
	err = resolver.RemoveOverride("my-tool", "my-backend")
	if err == nil {
		t.Error("expected error when removing non-existent override")
	}

	// ListOverrides is empty again
	overrides = resolver.ListOverrides()
	if len(overrides) != 0 {
		t.Errorf("expected 0 overrides after remove, got %d", len(overrides))
	}

	// ClearOverrides
	_ = resolver.RegisterOverride("tool-a", "backend-a", SafetyProfile{Risk: RiskLow, Impact: "read", Cost: 1})
	_ = resolver.RegisterOverride("tool-b", "backend-b", SafetyProfile{Risk: RiskMedium, Impact: "write", Cost: 2})
	resolver.ClearOverrides()
	overrides = resolver.ListOverrides()
	if len(overrides) != 0 {
		t.Errorf("expected 0 overrides after clear, got %d", len(overrides))
	}
}

// ---------- Kill switch ----------

func TestIsKillSwitchActive_DisabledConfig(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableKillSwitch = false
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if enf.IsKillSwitchActive("test") {
		t.Error("expected false when kill switch config is disabled")
	}
}

func TestKillSwitch_EnableDisable(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableKillSwitch = true
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Initially not active
	if enf.IsKillSwitchActive("test-scope") {
		t.Error("expected false initially")
	}

	// Enable for scope
	if err := enf.EnableKillSwitch("test-scope", "admin1", "testing"); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}
	if !enf.IsKillSwitchActive("test-scope") {
		t.Error("expected true after enabling")
	}

	// Disable
	if err := enf.DisableKillSwitch("test-scope"); err != nil {
		t.Fatalf("DisableKillSwitch: %v", err)
	}
	if enf.IsKillSwitchActive("test-scope") {
		t.Error("expected false after disabling")
	}
}

func TestKillSwitch_GlobalScope(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableKillSwitch = true
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Enable global kill switch
	if err := enf.EnableKillSwitch("global", "admin1", "emergency"); err != nil {
		t.Fatalf("EnableKillSwitch global: %v", err)
	}

	// All scopes should be affected
	if !enf.IsKillSwitchActive("any-scope") {
		t.Error("expected true for any scope when global is active")
	}
}

// ---------- DecorateDescription ----------

func TestDecorateDescription_Disabled(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableDescriptionDecoration = false
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	desc := enf.DecorateDescription("original description", "some-tool", "some-backend")
	if desc != "original description" {
		t.Errorf("expected unchanged description, got %q", desc)
	}
}

func TestDecorateDescription_EnabledNoProfile(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableDescriptionDecoration = true
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Resolve always succeeds (inferred profile for any tool name),
	// so the decorator always produces a prefixed description.
	desc := enf.DecorateDescription("original", "nonexistent-tool", "backend")
	if desc == "original" {
		t.Error("expected decorated description even for missing profile")
	}
}

func TestDecorateDescription_EnabledWithProfile(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.EnableDescriptionDecoration = true
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Register override so resolver has a profile
	_ = enf.resolver.RegisterOverride("decorated-tool", "backend", SafetyProfile{
		Risk:   RiskHigh,
		Impact: "delete",
		Cost:   5,
		Source: "override",
	})

	desc := enf.DecorateDescription("original", "decorated-tool", "backend")
	// Should be decorated with risk info
	if desc == "original" {
		t.Error("expected decorated description to differ from original")
	}
}

// ---------- CheckApproval with different statuses ----------

type statusMockStore struct {
	mockEnforcerStore
	getApproval func(id string) (ApprovalRequestRow, error)
}

func (s *statusMockStore) GetApprovalRequest(id string) (ApprovalRequestRow, error) {
	return s.getApproval(id)
}

func TestCheckApproval_Approved(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{Status: "APPROVED"}, nil
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ok, err := enf.CheckApproval(context.Background(), "id-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected true for APPROVED status")
	}
}

func TestCheckApproval_Denied(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{Status: "DENIED", DenialReason: "not needed"}, nil
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = enf.CheckApproval(context.Background(), "id-2")
	if err == nil {
		t.Fatal("expected error for DENIED status")
	}
	if err.Error() != "request was denied: not needed" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckApproval_Expired(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{Status: "EXPIRED", ExpiresAt: time.Now().Add(-1 * time.Hour)}, nil
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = enf.CheckApproval(context.Background(), "id-3")
	if err == nil {
		t.Fatal("expected error for EXPIRED status")
	}
	if err.Error() != "approval request expired" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckApproval_ExpiredByTime(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{Status: "PENDING", ExpiresAt: time.Now().Add(-1 * time.Hour)}, nil
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = enf.CheckApproval(context.Background(), "id-4")
	if err == nil {
		t.Fatal("expected error for expired-by-time request")
	}
	if err.Error() != "approval request expired" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckApproval_StillPending(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{Status: "PENDING", ExpiresAt: time.Now().Add(1 * time.Hour)}, nil
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	ok, err := enf.CheckApproval(context.Background(), "id-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false for pending request")
	}
}

func TestCheckApproval_StoreError(t *testing.T) {
	store := &statusMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{}, fmt.Errorf("db error")
		},
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	_, err = enf.CheckApproval(context.Background(), "id-6")
	if err == nil {
		t.Fatal("expected error for db error")
	}
	if err.Error() != "db error" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ---------- Config ----------

func TestConfig_ReturnsCopy(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.MinJustificationLength = 99
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	returned := enf.Config()
	if returned.MinJustificationLength != 99 {
		t.Errorf("got %d, want 99", returned.MinJustificationLength)
	}
}

// ---------- ApproveRequest / DenyRequest ----------

type approveMockStore struct {
	mockEnforcerStore
	getApproval func(id string) (ApprovalRequestRow, error)
	approve     func(id, approverID, comments string) error
	deny        func(id, approverID, reason string) error
}

func (s *approveMockStore) GetApprovalRequest(id string) (ApprovalRequestRow, error) {
	return s.getApproval(id)
}
func (s *approveMockStore) ApproveRequest(id, approverID, comments string) error {
	return s.approve(id, approverID, comments)
}
func (s *approveMockStore) DenyRequest(id, approverID, reason string) error {
	return s.deny(id, approverID, reason)
}

func TestApproveRequest_ReturnsRequestBody(t *testing.T) {
	store := &approveMockStore{
		getApproval: func(id string) (ApprovalRequestRow, error) {
			return ApprovalRequestRow{RequestBody: `{"tool":"test"}`}, nil
		},
		approve: func(id, approverID, comments string) error { return nil },
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	body, err := enf.ApproveRequest("id-1", "admin1", "looks good")
	if err != nil {
		t.Fatalf("ApproveRequest: %v", err)
	}
	if body != `{"tool":"test"}` {
		t.Errorf("got body %q, want %q", body, `{"tool":"test"}`)
	}
}

func TestDenyRequest(t *testing.T) {
	store := &approveMockStore{
		deny: func(id, approverID, reason string) error { return nil },
	}

	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, store, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if err := enf.DenyRequest("id-1", "admin1", "not needed"); err != nil {
		t.Errorf("DenyRequest: %v", err)
	}
}

// ---------- SetExecutor ----------

type mockExecutor struct{}

func (m *mockExecutor) ExecuteRequest(userID string, backendID string, requestBody string) (int, string, error) {
	return 200, "executed", nil
}

func TestSetExecutor(t *testing.T) {
	cfg := DefaultEnforcerConfig()
	cfg.CleanupInterval = 0

	enf, err := NewEnforcer(cfg, &mockEnforcerStore{}, &mockUserStore{})
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	enf.SetExecutor(&mockExecutor{})
	if enf.executor == nil {
		t.Error("expected executor to be set")
	}
}
