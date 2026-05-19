package enforcer

import (
	"errors"
	"testing"
	"time"
)

// mockToolProfileStore is a minimal ToolProfileStore for resolver tests.
type mockToolProfileStore struct {
	profiles map[string]ToolProfileRow // key: backendID+"|"+toolName
	prefix   map[string]string         // key: backendID
}

func newMockStore() *mockToolProfileStore {
	return &mockToolProfileStore{
		profiles: make(map[string]ToolProfileRow),
		prefix:   make(map[string]string),
	}
}

func (m *mockToolProfileStore) GetToolProfile(backendID, toolName string) (ToolProfileRow, error) {
	key := backendID + "|" + toolName
	if row, ok := m.profiles[key]; ok {
		return row, nil
	}
	return ToolProfileRow{}, errors.New("not found")
}

func (m *mockToolProfileStore) GetToolPrefix(backendID string) (string, error) {
	if p, ok := m.prefix[backendID]; ok {
		return p, nil
	}
	return "", errors.New("no prefix")
}

func (m *mockToolProfileStore) ListUserOverrides(userID string) ([]EnforcerOverrideRow, error) {
	return nil, nil
}

// TestSourceFromRawProfile verifies correct source string extraction.
func TestSourceFromRawProfile(t *testing.T) {
	tests := []struct {
		raw      string
		expected string
	}{
		{`{"source":"inferred"}`, "inferred"},
		{`{"source":"inferred","risk_level":"high"}`, "inferred"},
		{`{"risk_level":"high","idempotent":false}`, "self_reported"},
		{`{}`, "self_reported"},
		{``, "self_reported"},
	}
	for _, tt := range tests {
		got := sourceFromRawProfile(tt.raw)
		if got != tt.expected {
			t.Errorf("sourceFromRawProfile(%q) = %q, want %q", tt.raw, got, tt.expected)
		}
	}
}

// TestResolveForUser_Tier3_InferredRawProfile verifies that a DB row with
// {"source":"inferred"} in RawProfile returns Source="inferred".
func TestResolveForUser_Tier3_InferredRawProfile(t *testing.T) {
	store := newMockStore()
	store.profiles["mybackend|delete_ticket"] = ToolProfileRow{
		ID:           "abc",
		BackendID:    "mybackend",
		ToolName:     "delete_ticket",
		RiskLevel:    "high",
		ImpactScope:  "delete",
		ResourceCost: 10,
		RequiresHITL: false,
		RawProfile:   `{"source":"inferred"}`,
		ScannedAt:    time.Now(),
	}

	r := NewMetadataResolver(store)
	profile, err := r.ResolveForUser("delete_ticket", "mybackend", "user1")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}
	if profile.Source != "inferred" {
		t.Errorf("Source = %q, want inferred", profile.Source)
	}
	if profile.Risk != RiskHigh {
		t.Errorf("Risk = %q, want high", profile.Risk)
	}
}

// TestResolveForUser_Tier3_SelfReportedRawProfile verifies that a DB row without
// the inferred marker returns Source="self_reported".
func TestResolveForUser_Tier3_SelfReportedRawProfile(t *testing.T) {
	store := newMockStore()
	store.profiles["mybackend|get_ticket"] = ToolProfileRow{
		ID:           "xyz",
		BackendID:    "mybackend",
		ToolName:     "get_ticket",
		RiskLevel:    "low",
		ImpactScope:  "read",
		ResourceCost: 1,
		RequiresHITL: false,
		RawProfile:   `{"risk_level":"low","idempotent":true}`,
		ScannedAt:    time.Now(),
	}

	r := NewMetadataResolver(store)
	profile, err := r.ResolveForUser("get_ticket", "mybackend", "user1")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}
	if profile.Source != "self_reported" {
		t.Errorf("Source = %q, want self_reported", profile.Source)
	}
}

// TestResolve_Tier2_InferredRawProfile verifies the same fix in the non-user Resolve path.
func TestResolve_Tier2_InferredRawProfile(t *testing.T) {
	store := newMockStore()
	store.profiles["mybackend|run_job"] = ToolProfileRow{
		ID:           "def",
		BackendID:    "mybackend",
		ToolName:     "run_job",
		RiskLevel:    "medium",
		ImpactScope:  "write",
		ResourceCost: 7,
		RawProfile:   `{"source":"inferred"}`,
		ScannedAt:    time.Now(),
	}

	r := NewMetadataResolver(store)
	profile, err := r.Resolve("run_job", "mybackend")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if profile.Source != "inferred" {
		t.Errorf("Source = %q, want inferred", profile.Source)
	}
}

// TestInferDefaults_DelegatesToHeuristicProfile verifies the wrapper produces
// consistent results with a direct HeuristicProfile call.
func TestInferDefaults_DelegatesToHeuristicProfile(t *testing.T) {
	r := NewMetadataResolver(nil)

	toolNames := []string{"delete_ticket", "get_user", "xyzzy_frobnicate", "create_item"}
	for _, name := range toolNames {
		fromInfer := r.inferDefaults(name)
		fromHeuristic := HeuristicProfile(name, "", nil)

		if fromInfer.Risk != fromHeuristic.Risk {
			t.Errorf("inferDefaults(%q).Risk = %q, HeuristicProfile = %q", name, fromInfer.Risk, fromHeuristic.Risk)
		}
		if fromInfer.Impact != fromHeuristic.Impact {
			t.Errorf("inferDefaults(%q).Impact = %q, HeuristicProfile = %q", name, fromInfer.Impact, fromHeuristic.Impact)
		}
		if fromInfer.Cost != fromHeuristic.Cost {
			t.Errorf("inferDefaults(%q).Cost = %d, HeuristicProfile = %d", name, fromInfer.Cost, fromHeuristic.Cost)
		}
	}
}

// TestResolveForUser_Tier4_FallsBackToHeuristic verifies that a tool with no
// DB row falls through to heuristic inference with Source="inferred".
func TestResolveForUser_Tier4_FallsBackToHeuristic(t *testing.T) {
	r := NewMetadataResolver(newMockStore()) // empty store

	profile, err := r.ResolveForUser("delete_all_users", "somebackend", "user1")
	if err != nil {
		t.Fatalf("ResolveForUser: %v", err)
	}
	if profile.Source != "inferred" {
		t.Errorf("Source = %q, want inferred", profile.Source)
	}
	if profile.Risk != RiskCritical {
		t.Errorf("Risk = %q, want critical", profile.Risk)
	}
}
