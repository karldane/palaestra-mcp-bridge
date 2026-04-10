package enforcer

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// stripNamespacePrefix removes the backend namespace prefix from toolName.
// e.g., "qdrant_list_tasks" -> "list_tasks" for backend "qdrant"
func stripNamespacePrefix(toolName, backendID string) string {
	prefix := backendID + "_"
	if strings.HasPrefix(toolName, prefix) {
		return toolName[len(prefix):]
	}
	return toolName
}

// ToolProfileStore defines the interface for looking up stored safety profiles.
type ToolProfileStore interface {
	GetToolProfile(backendID, toolName string) (ToolProfileRow, error)
	ListUserOverrides(userID string) ([]EnforcerOverrideRow, error)
}

// ToolProfileRow represents a stored safety profile for a tool.
type ToolProfileRow struct {
	ID           string
	BackendID    string
	ToolName     string
	RiskLevel    string
	ImpactScope  string
	ResourceCost int
	RequiresHITL bool
	PIIExposure  bool
	Idempotent   bool
	RawProfile   string
	ScannedAt    time.Time
}

// MetadataResolver implements the tiered resolution logic from ENFORCER_SPEC.md
// Priority order:
//  1. Explicit Override (config) - Highest
//  2. Self-Reported (stored profiles from startup scan)
//  3. Inferred (pattern matching) - Default
type MetadataResolver struct {
	mu        sync.RWMutex
	store     ToolProfileStore
	overrides map[string]SafetyProfile // Tier 1: Config overrides
}

// NewMetadataResolver creates a new resolver with empty overrides
func NewMetadataResolver(store ToolProfileStore) *MetadataResolver {
	return &MetadataResolver{
		store:     store,
		overrides: make(map[string]SafetyProfile),
	}
}

// Resolve determines the final SafetyProfile using tiered priority
func (r *MetadataResolver) Resolve(toolName string, backendID string) (SafetyProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Tier 1: Check explicit overrides (config) - keyed by toolName|backendID
	if override, ok := r.overrides[toolName+"|"+backendID]; ok {
		override.ToolName = toolName
		override.BackendID = backendID
		override.Source = "override"
		return override, nil
	}

	// Tier 2: Check stored self-reported profiles (from startup scan)
	if r.store != nil {
		// Strip namespace prefix: qdrant_list_tasks -> list_tasks for qdrant backend
		lookupName := stripNamespacePrefix(toolName, backendID)
		profile, err := r.store.GetToolProfile(backendID, lookupName)
		if err == nil {
			return SafetyProfile{
				ToolName:     toolName,
				BackendID:    backendID,
				Risk:         RiskLevel(profile.RiskLevel),
				Impact:       ImpactScope(profile.ImpactScope),
				Cost:         profile.ResourceCost,
				RequiresHITL: profile.RequiresHITL,
				PIIExposure:  profile.PIIExposure,
				Source:       "self_reported",
			}, nil
		}
	}

	// Tier 3: Infer defaults from tool name patterns
	profile := r.inferDefaults(toolName)
	profile.ToolName = toolName
	profile.BackendID = backendID
	profile.Source = "inferred"
	return profile, nil
}

// ResolveForUser determines the final SafetyProfile using a 4-tier priority chain
// that includes user-scoped personal overrides between admin overrides and self-reported profiles.
//
//  1. Admin overrides (in-memory, no user_id)
//  2. User personal overrides (store query, user_id-scoped)
//  3. Self-reported stored profiles
//  4. Inferred defaults (pattern matching)
func (r *MetadataResolver) ResolveForUser(toolName string, backendID string, userID string) (SafetyProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Tier 1: Check explicit admin overrides (in-memory, keyed by toolName|backendID)
	if override, ok := r.overrides[toolName+"|"+backendID]; ok {
		override.ToolName = toolName
		override.BackendID = backendID
		override.Source = "override"
		return override, nil
	}

	// Tier 2: Check user personal overrides (store query, user_id-scoped)
	if r.store != nil && userID != "" {
		userOverrides, err := r.store.ListUserOverrides(userID)
		if err == nil {
			for _, o := range userOverrides {
				if o.ToolName == toolName && o.BackendID == backendID {
					return SafetyProfile{
						ToolName:     toolName,
						BackendID:    backendID,
						Risk:         RiskLevel(o.RiskLevel),
						Impact:       ImpactScope(o.ImpactScope),
						Cost:         o.ResourceCost,
						RequiresHITL: o.RequiresHITL,
						PIIExposure:  o.PIIExposure,
						Source:       "user_override",
					}, nil
				}
			}
		}
	}

	// Tier 3: Check stored self-reported profiles (from startup scan)
	if r.store != nil {
		// Strip namespace prefix: qdrant_list_tasks -> list_tasks for qdrant backend
		lookupName := stripNamespacePrefix(toolName, backendID)
		profile, err := r.store.GetToolProfile(backendID, lookupName)
		if err == nil {
			return SafetyProfile{
				ToolName:     toolName,
				BackendID:    backendID,
				Risk:         RiskLevel(profile.RiskLevel),
				Impact:       ImpactScope(profile.ImpactScope),
				Cost:         profile.ResourceCost,
				RequiresHITL: profile.RequiresHITL,
				PIIExposure:  profile.PIIExposure,
				Source:       "self_reported",
			}, nil
		}
	}

	// Tier 4: Infer defaults from tool name patterns
	inferred := r.inferDefaults(toolName)
	inferred.ToolName = toolName
	inferred.BackendID = backendID
	inferred.Source = "inferred"
	return inferred, nil
}

// inferDefaults uses pattern matching to determine safety for 3rd-party tools
func (r *MetadataResolver) inferDefaults(toolName string) SafetyProfile {
	profile := SafetyProfile{
		ToolName: toolName,
		Cost:     5, // Default medium cost
	}

	toolLower := strings.ToLower(toolName)

	// Risk inference — evaluated in priority order (most restrictive first).
	// Using ordered slices avoids Go map iteration non-determinism.
	type riskEntry struct {
		level    RiskLevel
		patterns []string
	}
	riskOrder := []riskEntry{
		{RiskCritical, []string{
			"delete", "drop", "remove", "destroy", "wipe", "purge",
			"truncate", "kill", "terminate", "shutdown",
		}},
		{RiskHigh, []string{
			"write", "update", "modify", "change", "edit",
			"create", "insert", "add", "append",
			"exec", "execute", "run", "call",
		}},
		{RiskMedium, []string{
			"query", "search", "find", "list", "get", "fetch",
			"read", "describe", "explain", "analyze",
		}},
		{RiskLow, []string{
			"ping", "health", "status", "info", "version",
			"help", "list.*tables", "list.*databases",
		}},
	}

	for _, entry := range riskOrder {
		for _, pattern := range entry.patterns {
			if matched, _ := regexp.MatchString(pattern, toolLower); matched {
				profile.Risk = entry.level
				break
			}
		}
		if profile.Risk != "" {
			break
		}
	}

	if profile.Risk == "" {
		profile.Risk = RiskMedium
	}

	// Impact inference — evaluated in priority order: delete > admin > write > read.
	// Admin is checked before write so that tool names containing both "set" (write)
	// and "configure"/"setting"/"admin" (admin) resolve to admin as intended.
	// Using ordered slices avoids Go map iteration non-determinism.
	type impactEntry struct {
		scope    ImpactScope
		patterns []string
	}
	impactOrder := []impactEntry{
		{ImpactDelete, []string{
			"delete", "drop", "remove", "destroy", "wipe", "purge",
			"truncate", "clear", "clean",
		}},
		{ImpactAdmin, []string{
			"admin", "config", "configure", "setting",
			"permission", "grant", "revoke", "role",
			"user.*manage", "account.*manage",
		}},
		{ImpactWrite, []string{
			"write", "update", "modify", "change", "edit",
			"create", "insert", "add", "append", "put",
			"patch", "replace", "set",
		}},
		{ImpactRead, []string{
			"query", "search", "find", "list", "get", "fetch",
			"read", "describe", "explain", "analyze", "view",
		}},
	}

	for _, entry := range impactOrder {
		for _, pattern := range entry.patterns {
			if matched, _ := regexp.MatchString(pattern, toolLower); matched {
				profile.Impact = entry.scope
				break
			}
		}
		if profile.Impact != "" {
			break
		}
	}

	if profile.Impact == "" {
		profile.Impact = ImpactRead
	}

	// HITL requirement for high-risk operations
	if profile.Risk == RiskHigh || profile.Risk == RiskCritical {
		profile.RequiresHITL = true
	}

	// Cost adjustment based on impact
	if profile.Impact == ImpactWrite {
		profile.Cost = 8
	} else if profile.Impact == ImpactAdmin {
		profile.Cost = 6
	} else if profile.Impact == ImpactDelete {
		profile.Cost = 5
	}

	return profile
}

// RegisterOverride adds a manual config override for a tool
func (r *MetadataResolver) RegisterOverride(toolName, backendID string, profile SafetyProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if profile.Risk == "" {
		return fmt.Errorf("risk level is required")
	}
	if profile.Impact == "" {
		return fmt.Errorf("impact scope is required")
	}

	key := toolName + "|" + backendID
	r.overrides[key] = profile
	return nil
}

// RemoveOverride removes a manual override
func (r *MetadataResolver) RemoveOverride(toolName, backendID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := toolName + "|" + backendID
	if _, ok := r.overrides[key]; !ok {
		return fmt.Errorf("no override found for tool: %s", toolName)
	}

	delete(r.overrides, key)
	return nil
}

// GetOverride retrieves an existing override
func (r *MetadataResolver) GetOverride(toolName, backendID string) (SafetyProfile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	profile, ok := r.overrides[toolName+"|"+backendID]
	return profile, ok
}

// ListOverrides returns all registered overrides
func (r *MetadataResolver) ListOverrides() map[string]SafetyProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()

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

// Ensure MetadataResolver implements the Registry interface
var _ Registry = (*MetadataResolver)(nil)

// GetProfile implements the Registry interface by delegating to Resolve
func (r *MetadataResolver) GetProfile(toolName string) (SafetyProfile, bool) {
	profile, err := r.Resolve(toolName, "")
	return profile, err == nil
}
