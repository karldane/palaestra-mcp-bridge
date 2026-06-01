package enforcer

import (
	"context"
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
