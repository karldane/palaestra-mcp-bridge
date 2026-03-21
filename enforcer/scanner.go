package enforcer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ScanBackendFn is the function signature for spawning a backend and getting tools.
type ScanBackendFn func(command string, env []string, timeout time.Duration) ([]byte, error)

// ToolProfileScanner scans MCP backends for self-reported EnforcerProfile data.
type ToolProfileScanner struct {
	scanBackend ScanBackendFn
}

// NewToolProfileScanner creates a scanner with the given spawn function.
func NewToolProfileScanner(scanBackend ScanBackendFn) *ToolProfileScanner {
	return &ToolProfileScanner{scanBackend: scanBackend}
}

// ScannedProfile represents a tool profile extracted from a backend.
type ScannedProfile struct {
	BackendID string
	ToolName  string
	Profile   EnforcerProfileFromFramework
	RawJSON   string
}

// EnforcerProfileFromFramework mirrors the mcp-framework EnforcerProfile for parsing.
type EnforcerProfileFromFramework struct {
	RiskLevel    string
	ImpactScope  string
	ResourceCost int
	PIIExposure  bool
	Idempotent   bool
	ApprovalReq  bool
}

// ScanBackend spawns a backend, calls tools/list, and returns extracted profiles.
func (s *ToolProfileScanner) ScanBackend(backendID, command string, env []string) ([]ScannedProfile, error) {
	envCopy := make([]string, len(env))
	copy(envCopy, env)

	result, err := s.scanBackend(command, envCopy, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("backend %s scan failed: %w", backendID, err)
	}

	var resp struct {
		Result struct {
			Tools []map[string]interface{} `json:"tools"`
		} `json:"result"`
		Error map[string]interface{} `json:"error"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("backend %s: failed to parse tools/list response: %w", backendID, err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("backend %s tools/list error: %v", backendID, resp.Error)
	}

	var profiles []ScannedProfile
	for _, tool := range resp.Result.Tools {
		profile := s.extractProfile(backendID, tool)
		if profile != nil {
			profiles = append(profiles, *profile)
		}
	}

	return profiles, nil
}

// extractProfile extracts EnforcerProfile from a tool's _meta field.
func (s *ToolProfileScanner) extractProfile(backendID string, tool map[string]interface{}) *ScannedProfile {
	toolName, ok := tool["name"].(string)
	if !ok || toolName == "" {
		return nil
	}

	var rawProfile map[string]interface{}
	var rawJSON string

	meta, hasMeta := tool["_meta"]
	if hasMeta {
		if metaMap, ok := meta.(map[string]interface{}); ok {
			if ep, ok := metaMap["enforcer_profile"].(map[string]interface{}); ok {
				b, _ := json.Marshal(ep)
				rawJSON = string(b)
				rawProfile = ep
			}
		}
	}

	if rawProfile == nil {
		return nil
	}

	profile := EnforcerProfileFromFramework{
		ResourceCost: 5,
	}

	if v, ok := rawProfile["risk_level"].(string); ok {
		profile.RiskLevel = normalizeRisk(v)
	}
	if v, ok := rawProfile["impact_scope"].(string); ok {
		profile.ImpactScope = v
	}
	if v, ok := rawProfile["resource_cost"].(float64); ok {
		profile.ResourceCost = int(v)
	}
	if v, ok := rawProfile["resource_cost"].(int); ok {
		profile.ResourceCost = v
	}
	if v, ok := rawProfile["pii_exposure"].(bool); ok {
		profile.PIIExposure = v
	}
	if v, ok := rawProfile["idempotent"].(bool); ok {
		profile.Idempotent = v
	}
	if v, ok := rawProfile["approval_req"].(bool); ok {
		profile.ApprovalReq = v
	}

	return &ScannedProfile{
		BackendID: backendID,
		ToolName:  toolName,
		Profile:   profile,
		RawJSON:   rawJSON,
	}
}

func normalizeRisk(s string) string {
	switch s {
	case "critical", "crit":
		return "critical"
	case "high", "hi":
		return "high"
	case "medium", "med", "mid":
		return "medium"
	case "low", "lo":
		return "low"
	default:
		return "medium"
	}
}

// ScanResultsToRows converts scanned profiles to DB rows for storage.
func ScanResultsToRows(backendID string, profiles []ScannedProfile) []ToolProfileRow {
	rows := make([]ToolProfileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, ToolProfileRow{
			ID:           uuid.New().String(),
			BackendID:    backendID,
			ToolName:     p.ToolName,
			RiskLevel:    p.Profile.RiskLevel,
			ImpactScope:  p.Profile.ImpactScope,
			ResourceCost: p.Profile.ResourceCost,
			RequiresHITL: p.Profile.ApprovalReq,
			PIIExposure:  p.Profile.PIIExposure,
			Idempotent:   p.Profile.Idempotent,
			RawProfile:   p.RawJSON,
			ScannedAt:    time.Now(),
		})
	}
	return rows
}
