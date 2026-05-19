package enforcer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// buildToolsListResponse wraps a slice of tool maps into a tools/list JSON response.
func buildToolsListResponse(tools []map[string]interface{}) []byte {
	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"tools": tools,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// mockScanFn returns a ScanBackendFn that always returns the given bytes.
func mockScanFn(data []byte) ScanBackendFn {
	return func(command string, env []string, timeout time.Duration) ([]byte, error) {
		return data, nil
	}
}

// ── extractProfile ────────────────────────────────────────────────────────────

// TestExtractProfile_SelfReporting verifies that a tool with _meta.enforcer_profile
// returns a ScannedProfile with the correct field values and no "source":"inferred" marker.
func TestExtractProfile_SelfReporting(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name":        "delete_ticket",
		"description": "Deletes a ticket",
		"_meta": map[string]interface{}{
			"enforcer_profile": map[string]interface{}{
				"risk_level":   "high",
				"impact_scope": "delete",
				"resource_cost": 10,
				"pii_exposure": false,
				"idempotent":   false,
				"approval_req": false,
			},
		},
	}
	p := s.extractProfile("mybackend", tool)
	if p == nil {
		t.Fatal("extractProfile returned nil for self-reporting tool")
	}
	if p.Profile.RiskLevel != "high" {
		t.Errorf("RiskLevel = %q, want high", p.Profile.RiskLevel)
	}
	if p.Profile.ImpactScope != "delete" {
		t.Errorf("ImpactScope = %q, want delete", p.Profile.ImpactScope)
	}
	if strings.Contains(p.RawJSON, `"source":"inferred"`) {
		t.Errorf("self-reported RawJSON contains inferred marker: %s", p.RawJSON)
	}
	if p.ToolName != "delete_ticket" {
		t.Errorf("ToolName = %q, want delete_ticket", p.ToolName)
	}
}

// TestExtractProfile_NoMeta verifies nil is returned for tools with no _meta.
func TestExtractProfile_NoMeta(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name":        "get_user",
		"description": "Gets a user",
	}
	p := s.extractProfile("mybackend", tool)
	if p != nil {
		t.Errorf("expected nil for no-_meta tool, got %+v", p)
	}
}

// TestExtractProfile_MetaNoEnforcerProfile verifies nil is returned when
// _meta exists but has no enforcer_profile key.
func TestExtractProfile_MetaNoEnforcerProfile(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name": "list_items",
		"_meta": map[string]interface{}{
			"some_other_key": "value",
		},
	}
	p := s.extractProfile("mybackend", tool)
	if p != nil {
		t.Errorf("expected nil, got %+v", p)
	}
}

// TestExtractProfile_EmptyName verifies nil is returned for unnamed tools.
func TestExtractProfile_EmptyName(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name": "",
		"_meta": map[string]interface{}{
			"enforcer_profile": map[string]interface{}{
				"risk_level": "low",
			},
		},
	}
	p := s.extractProfile("mybackend", tool)
	if p != nil {
		t.Errorf("expected nil for empty tool name, got %+v", p)
	}
}

// ── inferProfile ──────────────────────────────────────────────────────────────

// TestInferProfile_NonSelfReporting verifies that a tool without _meta produces
// a ScannedProfile with RawJSON={"source":"inferred"} and correct heuristic values.
func TestInferProfile_NonSelfReporting(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name":        "delete_ticket",
		"description": "Removes a ticket from the system",
	}
	p := s.inferProfile("mybackend", tool)
	if p == nil {
		t.Fatal("inferProfile returned nil for valid tool")
	}
	if p.RawJSON != `{"source":"inferred"}` {
		t.Errorf("RawJSON = %q, want {\"source\":\"inferred\"}", p.RawJSON)
	}
	if p.Profile.RiskLevel != "high" {
		t.Errorf("RiskLevel = %q, want high", p.Profile.RiskLevel)
	}
	if p.Profile.ImpactScope != "delete" {
		t.Errorf("ImpactScope = %q, want delete", p.Profile.ImpactScope)
	}
	if p.ToolName != "delete_ticket" {
		t.Errorf("ToolName = %q, want delete_ticket", p.ToolName)
	}
	if p.BackendID != "mybackend" {
		t.Errorf("BackendID = %q, want mybackend", p.BackendID)
	}
}

// TestInferProfile_WithSchema verifies that schema PII signals are passed to HeuristicProfile.
func TestInferProfile_WithSchema(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name":        "get_user_profile",
		"description": "Retrieves a user profile",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"email":  map[string]interface{}{"type": "string"},
				"user_id": map[string]interface{}{"type": "string"},
			},
		},
	}
	p := s.inferProfile("mybackend", tool)
	if p == nil {
		t.Fatal("inferProfile returned nil")
	}
	if !p.Profile.PIIExposure {
		t.Error("PIIExposure = false, want true (email field in schema)")
	}
	if p.Profile.RiskLevel != "medium" {
		t.Errorf("RiskLevel = %q, want medium (low promoted by PII)", p.Profile.RiskLevel)
	}
}

// TestInferProfile_EmptyName verifies nil is returned for unnamed tools.
func TestInferProfile_EmptyName(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"description": "some tool",
	}
	p := s.inferProfile("mybackend", tool)
	if p != nil {
		t.Errorf("expected nil for missing name, got %+v", p)
	}
}

// TestInferProfile_FallThroughSentinel verifies that an unrecognised tool name
// gets the sentinel profile with RequiresHITL=true.
func TestInferProfile_FallThroughSentinel(t *testing.T) {
	s := NewToolProfileScanner(nil)
	tool := map[string]interface{}{
		"name": "xyzzy_frobnicate",
	}
	p := s.inferProfile("mybackend", tool)
	if p == nil {
		t.Fatal("inferProfile returned nil")
	}
	if !p.Profile.ApprovalReq {
		t.Error("ApprovalReq = false, want true (sentinel)")
	}
	if p.Profile.RiskLevel != "high" {
		t.Errorf("RiskLevel = %q, want high (sentinel)", p.Profile.RiskLevel)
	}
}

// ── ScanBackend loop ──────────────────────────────────────────────────────────

// TestScanBackend_SelfReportingToolUnchanged verifies that self-reporting tools
// are processed via extractProfile and their RawJSON does not contain the inferred marker.
func TestScanBackend_SelfReportingToolUnchanged(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name": "get_ticket",
			"_meta": map[string]interface{}{
				"enforcer_profile": map[string]interface{}{
					"risk_level":   "low",
					"impact_scope": "read",
					"resource_cost": 1,
					"idempotent":   true,
					"approval_req": false,
				},
			},
		},
	}
	data := buildToolsListResponse(tools)
	s := NewToolProfileScanner(mockScanFn(data))
	profiles, err := s.ScanBackend("mybackend", "fake-cmd", nil)
	if err != nil {
		t.Fatalf("ScanBackend: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	p := profiles[0]
	if strings.Contains(p.RawJSON, `"source":"inferred"`) {
		t.Errorf("self-reporting tool has inferred marker in RawJSON: %s", p.RawJSON)
	}
	if p.Profile.RiskLevel != "low" {
		t.Errorf("RiskLevel = %q, want low", p.Profile.RiskLevel)
	}
}

// TestScanBackend_NonSelfReportingToolInferred verifies that a tool without
// _meta.enforcer_profile is inferred and produces RawJSON={"source":"inferred"}.
func TestScanBackend_NonSelfReportingToolInferred(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name":        "delete_record",
			"description": "Deletes a record from the database",
		},
	}
	data := buildToolsListResponse(tools)
	s := NewToolProfileScanner(mockScanFn(data))
	profiles, err := s.ScanBackend("mybackend", "fake-cmd", nil)
	if err != nil {
		t.Fatalf("ScanBackend: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	p := profiles[0]
	if p.RawJSON != `{"source":"inferred"}` {
		t.Errorf("RawJSON = %q, want {\"source\":\"inferred\"}", p.RawJSON)
	}
	if p.ToolName != "delete_record" {
		t.Errorf("ToolName = %q, want delete_record", p.ToolName)
	}
}

// TestScanBackend_MixedTools verifies that a backend with both self-reporting
// and non-self-reporting tools produces profiles for all tools.
func TestScanBackend_MixedTools(t *testing.T) {
	tools := []map[string]interface{}{
		{
			"name": "get_ticket",
			"_meta": map[string]interface{}{
				"enforcer_profile": map[string]interface{}{
					"risk_level":   "low",
					"impact_scope": "read",
					"resource_cost": 1,
				},
			},
		},
		{
			"name":        "bulk_delete_tickets",
			"description": "Deletes all tickets matching the filter",
		},
	}
	data := buildToolsListResponse(tools)
	s := NewToolProfileScanner(mockScanFn(data))
	profiles, err := s.ScanBackend("mybackend", "fake-cmd", nil)
	if err != nil {
		t.Fatalf("ScanBackend: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}

	byName := make(map[string]ScannedProfile)
	for _, p := range profiles {
		byName[p.ToolName] = p
	}

	// Self-reporting tool — no inferred marker
	self := byName["get_ticket"]
	if strings.Contains(self.RawJSON, `"source":"inferred"`) {
		t.Error("get_ticket: unexpected inferred marker")
	}

	// Inferred tool — must have marker and critical risk (bulk + delete)
	inferred := byName["bulk_delete_tickets"]
	if inferred.RawJSON != `{"source":"inferred"}` {
		t.Errorf("bulk_delete_tickets: RawJSON = %q, want inferred marker", inferred.RawJSON)
	}
	if inferred.Profile.RiskLevel != "critical" {
		t.Errorf("bulk_delete_tickets: RiskLevel = %q, want critical", inferred.Profile.RiskLevel)
	}
}

// TestScanBackend_ToolWithNoName verifies that tools with no name are skipped.
func TestScanBackend_ToolWithNoName(t *testing.T) {
	tools := []map[string]interface{}{
		{"description": "a nameless tool"},
	}
	data := buildToolsListResponse(tools)
	s := NewToolProfileScanner(mockScanFn(data))
	profiles, err := s.ScanBackend("mybackend", "fake-cmd", nil)
	if err != nil {
		t.Fatalf("ScanBackend: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("got %d profiles, want 0 (nameless tools should be skipped)", len(profiles))
	}
}
