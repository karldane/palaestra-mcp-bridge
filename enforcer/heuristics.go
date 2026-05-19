package enforcer

import "strings"

// PromoteRisk advances a risk level by one tier: low→medium→high→critical.
// Already at critical: no-op.
func PromoteRisk(current RiskLevel) RiskLevel {
	switch current {
	case RiskLow:
		return RiskMedium
	case RiskMedium:
		return RiskHigh
	case RiskHigh:
		return RiskCritical
	default:
		return RiskCritical
	}
}

// heuristicRule describes a single pattern-match entry in the classification table.
type heuristicRule struct {
	patterns     []string
	risk         RiskLevel
	impact       ImpactScope
	idempotent   bool
	requiresHITL bool
	cost         int
}

// classificationRules are evaluated in order; first match wins.
var classificationRules = []heuristicRule{
	// Priority 1 — Critical destructive
	{
		patterns:     []string{"delete_all", "purge", "drop_database", "drop_table", "wipe", "terminate_cluster", "decommission", "factory_reset", "hard_delete"},
		risk:         RiskCritical,
		impact:       ImpactDelete,
		idempotent:   false,
		requiresHITL: true,
		cost:         20,
	},
	// Priority 2 — High destructive
	{
		patterns:     []string{"delete", "remove", "destroy", "revoke", "deactivate", "disable", "ban", "block", "archive", "unsubscribe", "cancel", "terminate", "kill", "stop_service", "deprovision", "offboard"},
		risk:         RiskHigh,
		impact:       ImpactDelete,
		idempotent:   false,
		requiresHITL: false,
		cost:         10,
	},
	// Priority 3 — High auth/access
	{
		patterns:     []string{"grant", "assign_role", "elevate", "promote", "impersonate", "sudo", "reset_password", "rotate_key", "generate_token", "create_api_key", "set_permission"},
		risk:         RiskHigh,
		impact:       ImpactAdmin,
		idempotent:   false,
		requiresHITL: false,
		cost:         10,
	},
	// Priority 4 — High financial
	{
		patterns:     []string{"charge", "invoice", "refund", "transfer", "withdraw", "purchase", "pay", "billing", "subscribe", "checkout"},
		risk:         RiskHigh,
		impact:       ImpactAdmin,
		idempotent:   false,
		requiresHITL: true,
		cost:         10,
	},
	// Priority 5 — Medium write
	{
		patterns:     []string{"create", "add", "insert", "write", "post", "publish", "send", "submit", "upload", "import", "deploy", "provision", "onboard", "register", "invite", "put"},
		risk:         RiskMedium,
		impact:       ImpactWrite,
		idempotent:   false,
		requiresHITL: false,
		cost:         5,
	},
	// Priority 6 — Medium update
	{
		patterns:     []string{"update", "edit", "modify", "patch", "set", "change", "rename", "move", "migrate", "replace", "upsert", "merge"},
		risk:         RiskMedium,
		impact:       ImpactWrite,
		idempotent:   false,
		requiresHITL: false,
		cost:         5,
	},
	// Priority 7 — Medium trigger
	{
		patterns:     []string{"run", "execute", "trigger", "start", "launch", "fire", "invoke", "call", "process", "enqueue", "schedule"},
		risk:         RiskMedium,
		impact:       ImpactWrite,
		idempotent:   false,
		requiresHITL: false,
		cost:         7,
	},
	// Priority 8 — Low read
	{
		patterns:     []string{"get", "fetch", "read", "list", "search", "find", "query", "lookup", "retrieve", "describe", "explain", "show", "view", "inspect", "check", "status", "health", "ping", "count", "summarize", "report", "export", "download"},
		risk:         RiskLow,
		impact:       ImpactRead,
		idempotent:   true,
		requiresHITL: false,
		cost:         1,
	},
}

// piiSchemaPatterns are substring patterns matched against input schema property keys.
var piiSchemaPatterns = []string{
	"email", "phone", "mobile", "address", "postcode", "zip", "ssn",
	"national_id", "passport", "dob", "birth",
	"credit_card", "card_number", "iban", "bank_account", "salary", "income", "tax",
	"password", "secret", "token", "api_key", "access_key", "private_key",
}

// piiDescriptionSignals are substrings matched against the tool description.
var piiDescriptionSignals = []string{
	"personal data", "pii", "gdpr", "email address", "phone number",
	"credit card", "social security", "passport", "date of birth",
}

// irreversibilitySignals are substrings matched against the tool description.
var irreversibilitySignals = []string{
	"permanent", "irreversible", "cannot be undone", "cannot be recovered",
	"destroy", "wipe", "purge", "terminates all", "drops all", "truncates",
	"hard delete", "force delete",
}

// bulkSignals are substrings matched against the tool name or description.
var bulkSignals = []string{
	"bulk", "batch", "all records", "entire", "mass ", "fleet",
	"all users", "all items", "all tickets", "all issues",
}

// containsWordOrPrefix returns true if s contains pat as a whole underscore-delimited
// token, or if any token starts with pat (to handle e.g. "deletes" matching "delete").
// Both s and pat must already be lowercased.
func containsWordOrPrefix(s, pat string) bool {
	// Fast path: exact containment with delimiter check
	idx := strings.Index(s, pat)
	for idx >= 0 {
		end := idx + len(pat)
		// Check left boundary: must be start of string or preceded by '_'
		leftOK := idx == 0 || s[idx-1] == '_'
		// Check right boundary: must be end of string, followed by '_', or followed by a digit
		rightOK := end == len(s) || s[end] == '_'
		if leftOK && rightOK {
			return true
		}
		// Advance past this occurrence
		next := strings.Index(s[idx+1:], pat)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return false
}

// HeuristicProfile infers a SafetyProfile from a tool name, optional description,
// and optional input schema properties. It is called by:
//   - scanner.go inferProfile (scan time, full context available)
//   - resolver.go inferDefaults wrapper (runtime fallback, name only)
//
// The returned profile has Source = "inferred". ToolName and BackendID are not
// set — callers are responsible for populating those fields.
func HeuristicProfile(toolName, description string, inputSchema map[string]interface{}) SafetyProfile {
	lower := strings.ToLower(toolName)
	lowerDesc := strings.ToLower(description)

	// ── 1. PII detection ─────────────────────────────────────────────────────
	piiExposure := false

	if inputSchema != nil {
		for key := range inputSchema {
			lk := strings.ToLower(key)
			for _, pat := range piiSchemaPatterns {
				if strings.Contains(lk, pat) {
					piiExposure = true
					break
				}
			}
			if piiExposure {
				break
			}
		}
	}

	if !piiExposure && description != "" {
		for _, sig := range piiDescriptionSignals {
			if strings.Contains(lowerDesc, sig) {
				piiExposure = true
				break
			}
		}
	}

	// ── 2. Irreversibility detection ─────────────────────────────────────────
	isIrreversible := false
	if description != "" {
		for _, sig := range irreversibilitySignals {
			if strings.Contains(lowerDesc, sig) {
				isIrreversible = true
				break
			}
		}
	}

	// ── 3. Bulk-operation detection ───────────────────────────────────────────
	isBulk := false
	for _, sig := range bulkSignals {
		if containsWordOrPrefix(lower, sig) || (description != "" && strings.Contains(lowerDesc, sig)) {
			isBulk = true
			break
		}
	}

	// ── 4. Risk/impact classification (first match wins) ─────────────────────
	var matched *heuristicRule
	for i := range classificationRules {
		rule := &classificationRules[i]
		for _, pat := range rule.patterns {
			if containsWordOrPrefix(lower, pat) {
				matched = rule
				break
			}
		}
		if matched != nil {
			break
		}
	}

	var p SafetyProfile
	p.Source = "inferred"

	if matched != nil {
		p.Risk = matched.risk
		p.Impact = matched.impact
		p.Idempotent = matched.idempotent
		p.RequiresHITL = matched.requiresHITL
		p.Cost = matched.cost
	} else {
		// ── 5. Fall-through sentinel ──────────────────────────────────────────
		p.Risk = RiskHigh
		p.Impact = ImpactDelete
		p.Idempotent = false
		p.RequiresHITL = true
		p.Cost = 10
	}

	// ── 6. Post-match promotions ──────────────────────────────────────────────
	if isIrreversible {
		p.Risk = PromoteRisk(p.Risk)
		p.RequiresHITL = true
		p.Idempotent = false
	}
	if isBulk {
		p.Risk = PromoteRisk(p.Risk)
		p.RequiresHITL = true
		p.Cost *= 3
	}
	if piiExposure && p.Risk == RiskLow {
		p.Risk = RiskMedium
	}
	p.PIIExposure = piiExposure

	return p
}
