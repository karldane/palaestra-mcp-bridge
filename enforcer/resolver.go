package enforcer

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// MetadataResolver implements the tiered resolution logic from ENFORCER_SPEC.md
// Priority order:
//  1. Explicit Override (config.yaml) - Highest
//  2. Self-Reported (annotations from framework)
//  3. Inferred (pattern matching) - Default
type MetadataResolver struct {
	mu        sync.RWMutex
	overrides map[string]SafetyProfile // Tier 1: Config overrides
}

// NewMetadataResolver creates a new resolver with empty overrides
func NewMetadataResolver() *MetadataResolver {
	return &MetadataResolver{
		overrides: make(map[string]SafetyProfile),
	}
}

// Resolve determines the final SafetyProfile using tiered priority
func (r *MetadataResolver) Resolve(toolName string, backendID string, annotations map[string]interface{}) (SafetyProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Tier 1: Check explicit overrides (config.yaml)
	if override, ok := r.overrides[toolName]; ok {
		override.ToolName = toolName
		override.BackendID = backendID
		override.Source = "config"
		return override, nil
	}

	// Log resolution attempt
	fmt.Printf("DEBUG: Resolving safety profile for tool=%s, backend=%s\n", toolName, backendID)

	// Tier 2: Parse self-reported annotations from framework
	if annotations != nil && len(annotations) > 0 {
		profile := r.parseAnnotations(toolName, backendID, annotations)
		if profile.Risk != "" || profile.Impact != "" {
			profile.Source = "self_reported"
			return profile, nil
		}
	}

	// Tier 3: Infer defaults from tool name patterns
	profile := r.inferDefaults(toolName)
	profile.ToolName = toolName
	profile.BackendID = backendID
	profile.Source = "inferred"
	return profile, nil
}

// parseAnnotations extracts safety metadata from framework annotations
func (r *MetadataResolver) parseAnnotations(toolName, backendID string, annotations map[string]interface{}) SafetyProfile {
	profile := SafetyProfile{
		ToolName:  toolName,
		BackendID: backendID,
	}

	// Handle nested "enforcer" key if present
	enforcerData := annotations
	if enforcer, ok := annotations["enforcer"].(map[string]interface{}); ok {
		enforcerData = enforcer
	}

	// Extract risk level
	if risk, ok := enforcerData["risk"].(string); ok && risk != "" {
		profile.Risk = RiskLevel(strings.ToLower(risk))
	}
	if risk, ok := enforcerData["risk_level"].(string); ok && risk != "" {
		profile.Risk = RiskLevel(strings.ToLower(risk))
	}

	// Extract impact scope
	if impact, ok := enforcerData["impact"].(string); ok && impact != "" {
		profile.Impact = ImpactScope(strings.ToLower(impact))
	}
	if impact, ok := enforcerData["impact_scope"].(string); ok && impact != "" {
		profile.Impact = ImpactScope(strings.ToLower(impact))
	}

	// Extract cost
	if cost, ok := enforcerData["cost"].(int); ok {
		profile.Cost = cost
	}
	if cost, ok := enforcerData["resource_cost"].(int); ok {
		profile.Cost = cost
	}
	if cost, ok := enforcerData["cost"].(float64); ok {
		profile.Cost = int(cost)
	}

	// Extract HITL requirement
	if hitl, ok := enforcerData["hitl"].(bool); ok {
		profile.RequiresHITL = hitl
	}
	if hitl, ok := enforcerData["requires_hitl"].(bool); ok {
		profile.RequiresHITL = hitl
	}
	if hitl, ok := enforcerData["approval_req"].(bool); ok {
		profile.RequiresHITL = hitl
	}

	// Extract PII exposure
	if pii, ok := enforcerData["pii_exposure"].(bool); ok {
		profile.PIIExposure = pii
	}
	if pii, ok := enforcerData["pii"].(bool); ok {
		profile.PIIExposure = pii
	}

	// Extract metadata
	profile.Metadata = make(map[string]interface{})
	for k, v := range enforcerData {
		if k != "risk" && k != "risk_level" && k != "impact" && k != "impact_scope" &&
			k != "cost" && k != "resource_cost" && k != "hitl" && k != "requires_hitl" &&
			k != "approval_req" && k != "pii_exposure" && k != "pii" {
			profile.Metadata[k] = v
		}
	}

	return profile
}

// inferDefaults uses pattern matching to determine safety for 3rd-party tools
func (r *MetadataResolver) inferDefaults(toolName string) SafetyProfile {
	profile := SafetyProfile{
		ToolName: toolName,
		Cost:     5, // Default medium cost
	}

	toolLower := strings.ToLower(toolName)
	fmt.Printf("DEBUG: inferDefaults for tool=%s\n", toolName)

	// Risk inference based on naming patterns
	riskPatterns := map[RiskLevel][]string{
		RiskCritical: {
			"delete", "drop", "remove", "destroy", "wipe", "purge",
			"truncate", "kill", "terminate", "shutdown",
		},
		RiskHigh: {
			"write", "update", "modify", "change", "edit",
			"create", "insert", "add", "append",
			"exec", "execute", "run", "call",
		},
		RiskMedium: {
			"query", "search", "find", "list", "get", "fetch",
			"read", "describe", "explain", "analyze",
		},
		RiskLow: {
			"ping", "health", "status", "info", "version",
			"help", "list.*tables", "list.*databases",
		},
	}

	for risk, patterns := range riskPatterns {
		for _, pattern := range patterns {
			if matched, _ := regexp.MatchString(pattern, toolLower); matched {
				profile.Risk = risk
				break
			}
		}
		if profile.Risk != "" {
			break
		}
	}

	// If no risk matched, default to medium
	if profile.Risk == "" {
		profile.Risk = RiskMedium
	}

	// Impact inference
	impactPatterns := map[ImpactScope][]string{
		ImpactDelete: {
			"delete", "drop", "remove", "destroy", "wipe", "purge",
			"truncate", "clear", "clean",
		},
		ImpactWrite: {
			"write", "update", "modify", "change", "edit",
			"create", "insert", "add", "append", "put",
			"patch", "replace", "set",
		},
		ImpactAdmin: {
			"admin", "config", "configure", "setting",
			"permission", "grant", "revoke", "role",
			"user.*manage", "account.*manage",
		},
		ImpactRead: {
			"query", "search", "find", "list", "get", "fetch",
			"read", "describe", "explain", "analyze", "view",
		},
	}

	for impact, patterns := range impactPatterns {
		for _, pattern := range patterns {
			if matched, _ := regexp.MatchString(pattern, toolLower); matched {
				profile.Impact = impact
				break
			}
		}
		if profile.Impact != "" {
			break
		}
	}

	// If no impact matched, default to read
	if profile.Impact == "" {
		profile.Impact = ImpactRead
	}

	// HITL requirement for high-risk operations
	if profile.Risk == RiskHigh || profile.Risk == RiskCritical {
		profile.RequiresHITL = true
	}

	// Cost adjustment based on impact (lower = less resource intensive)
	// Note: Cost reflects actual system resource consumption, not safety risk
	// Safety risk is handled by the impact_scope and risk_level fields
	if profile.Impact == ImpactWrite {
		profile.Cost = 8
	} else if profile.Impact == ImpactAdmin {
		profile.Cost = 6
	} else if profile.Impact == ImpactDelete {
		profile.Cost = 5 // Not resource-intensive, just dangerous - handled by impact_scope policy
	}

	fmt.Printf("DEBUG: Inferred profile for %s - Risk=%s Impact=%s Cost=%d\n", toolName, profile.Risk, profile.Impact, profile.Cost)
	return profile
}

// RegisterOverride adds a manual config override for a tool
func (r *MetadataResolver) RegisterOverride(toolName string, profile SafetyProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Validate the profile
	if profile.Risk == "" {
		return fmt.Errorf("risk level is required")
	}
	if profile.Impact == "" {
		return fmt.Errorf("impact scope is required")
	}

	r.overrides[toolName] = profile
	return nil
}

// RemoveOverride removes a manual override
func (r *MetadataResolver) RemoveOverride(toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.overrides[toolName]; !ok {
		return fmt.Errorf("no override found for tool: %s", toolName)
	}

	delete(r.overrides, toolName)
	return nil
}

// GetOverride retrieves an existing override (for testing/management)
func (r *MetadataResolver) GetOverride(toolName string) (SafetyProfile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	profile, ok := r.overrides[toolName]
	return profile, ok
}

// ListOverrides returns all registered overrides
func (r *MetadataResolver) ListOverrides() map[string]SafetyProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Return a copy to avoid external mutation
	result := make(map[string]SafetyProfile, len(r.overrides))
	for k, v := range r.overrides {
		result[k] = v
	}
	return result
}

// ClearOverrides removes all overrides (useful for testing)
func (r *MetadataResolver) ClearOverrides() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.overrides = make(map[string]SafetyProfile)
}

// Ensure MetadataResolver implements the Resolver interface
var _ Registry = (*MetadataResolver)(nil)

// GetProfile implements the Registry interface by delegating to Resolve
func (r *MetadataResolver) GetProfile(toolName string) (SafetyProfile, bool) {
	profile, err := r.Resolve(toolName, "", nil)
	return profile, err == nil
}
