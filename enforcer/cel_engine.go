package enforcer

import (
	"context"
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/interpreter"
)

// CELEngine implements policy evaluation using Google's CEL
type CELEngine struct {
	env      *cel.Env
	programs map[string]cel.Program
	policies map[string]CELPolicy
}

// NewCELEngine creates a new CEL policy engine with predefined declarations
func NewCELEngine() (*CELEngine, error) {
	// Create CEL environment with safety-related declarations
	env, err := cel.NewEnv(
		// Variable declarations for decision context
		cel.Declarations(
			// User context
			decls.NewVar("user", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("user_id", decls.String),
			decls.NewVar("user_role", decls.String),
			decls.NewVar("user_email", decls.String),
			decls.NewVar("trust_level", decls.Int),

			// Tool context
			decls.NewVar("tool", decls.String),
			decls.NewVar("tool_name", decls.String),
			decls.NewVar("args", decls.NewMapType(decls.String, decls.Dyn)),

			// Safety profile
			decls.NewVar("safety", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("risk_level", decls.String),
			decls.NewVar("impact_scope", decls.String),
			decls.NewVar("resource_cost", decls.Int),
			decls.NewVar("requires_hitl", decls.Bool),
			decls.NewVar("pii_exposure", decls.Bool),

			// System context
			decls.NewVar("system", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("system_load", decls.Double),
			decls.NewVar("timestamp", decls.Timestamp),

			// Backend context
			decls.NewVar("backend_id", decls.String),
			decls.NewVar("backend_type", decls.String),

			// Rate limiting context
			decls.NewVar("risk_bucket", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("risk_available", decls.Int),
			decls.NewVar("risk_capacity", decls.Int),
			decls.NewVar("risk_refill_rate", decls.Int),
			decls.NewVar("resource_bucket", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("resource_available", decls.Int),
			decls.NewVar("resource_capacity", decls.Int),
			decls.NewVar("resource_refill_rate", decls.Int),
		),

		// Add custom functions
		cel.Lib(celEnforcerLib{}),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	return &CELEngine{
		env:      env,
		programs: make(map[string]cel.Program),
		policies: make(map[string]CELPolicy),
	}, nil
}

// AddPolicy adds a policy to the engine and compiles it
func (e *CELEngine) AddPolicy(policy CELPolicy) error {
	if !policy.Enabled {
		return nil
	}

	// Parse the expression
	ast, issues := e.env.Parse(policy.Expression)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("policy %s parse error: %w", policy.ID, issues.Err())
	}

	// Type-check the expression
	checked, issues := e.env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("policy %s type check error: %w", policy.ID, issues.Err())
	}

	// Compile to program
	program, err := e.env.Program(checked)
	if err != nil {
		return fmt.Errorf("policy %s compile error: %w", policy.ID, err)
	}

	e.programs[policy.ID] = program
	e.policies[policy.ID] = policy
	return nil
}

// RemovePolicy removes a policy from the engine
func (e *CELEngine) RemovePolicy(policyID string) {
	delete(e.programs, policyID)
	delete(e.policies, policyID)
}

// Evaluate runs all policies against the decision context and returns the most restrictive decision
func (e *CELEngine) Evaluate(ctx context.Context, context DecisionContext) (EnforcerDecision, error) {
	// Build activation with context values
	activation, err := e.buildActivation(context)
	if err != nil {
		return EnforcerDecision{}, fmt.Errorf("failed to build activation: %w", err)
	}

	// Track the most restrictive decision
	var finalDecision EnforcerDecision
	finalDecision.Action = ActionAllow // Default
	finalDecision.Timestamp = time.Now()
	finalDecision.Violations = []string{}

	// Severity priority: CRITICAL > HIGH > MEDIUM > LOW
	severityPriority := map[SeverityLevel]int{
		SeverityCritical: 4,
		SeverityHigh:     3,
		SeverityMedium:   2,
		SeverityLow:      1,
	}

	// Evaluate all policies
	for policyID, program := range e.programs {
		policy := e.policies[policyID]

		// Evaluate the CEL expression
		out, details, err := program.Eval(activation)
		if err != nil {
			// Log error but continue with other policies
			finalDecision.Violations = append(finalDecision.Violations,
				fmt.Sprintf("policy %s evaluation error: %v", policyID, err))
			continue
		}

		// Check if the expression returned true (policy matched)
		if boolValue, ok := out.Value().(bool); ok && boolValue {
			// This policy matched - apply its action
			decision := EnforcerDecision{
				Action:    policy.Action,
				Severity:  policy.Severity,
				Message:   policy.Message,
				PolicyID:  policyID,
				Timestamp: time.Now(),
			}

			// Update final decision if this is more restrictive
			if shouldUpdateDecision(finalDecision, decision, severityPriority) {
				finalDecision = decision
			}

			// Record the violation
			finalDecision.Violations = append(finalDecision.Violations,
				fmt.Sprintf("%s: %s", policyID, policy.Description))
		}

		_ = details // Could be used for detailed logging
	}

	return finalDecision, nil
}

// buildActivation creates a CEL activation from the decision context
func (e *CELEngine) buildActivation(ctx DecisionContext) (interpreter.Activation, error) {
	// Build nested maps for complex structures
	userMap := map[string]interface{}{
		"id":          ctx.UserID,
		"role":        ctx.UserRole,
		"email":       ctx.UserEmail,
		"trust_level": ctx.TrustLevel,
	}

	safetyMap := map[string]interface{}{
		"risk_level":    string(ctx.Safety.Risk),
		"impact_scope":  string(ctx.Safety.Impact),
		"resource_cost": ctx.Safety.Cost,
		"requires_hitl": ctx.Safety.RequiresHITL,
		"pii_exposure":  ctx.Safety.PIIExposure,
	}

	systemMap := map[string]interface{}{
		"load_avg": ctx.SystemLoad,
	}

	riskBucketMap := map[string]interface{}{
		"available":   ctx.RateLimit.RiskBucket.Available,
		"capacity":    ctx.RateLimit.RiskBucket.Capacity,
		"refill_rate": ctx.RateLimit.RiskBucket.RefillRate,
	}

	resourceBucketMap := map[string]interface{}{
		"available":   ctx.RateLimit.ResourceBucket.Available,
		"capacity":    ctx.RateLimit.ResourceBucket.Capacity,
		"refill_rate": ctx.RateLimit.ResourceBucket.RefillRate,
	}

	// Create activation with all variables
	return interpreter.NewActivation(map[string]interface{}{
		"user":        userMap,
		"user_id":     ctx.UserID,
		"user_role":   ctx.UserRole,
		"user_email":  ctx.UserEmail,
		"trust_level": ctx.TrustLevel,

		"tool":      ctx.Tool,
		"tool_name": ctx.Tool,
		"args":      ctx.Args,

		"safety":        safetyMap,
		"risk_level":    string(ctx.Safety.Risk),
		"impact_scope":  string(ctx.Safety.Impact),
		"resource_cost": ctx.Safety.Cost,
		"requires_hitl": ctx.Safety.RequiresHITL,
		"pii_exposure":  ctx.Safety.PIIExposure,

		"system":      systemMap,
		"system_load": ctx.SystemLoad,
		"timestamp":   ctx.Timestamp,

		"backend_id":   ctx.BackendID,
		"backend_type": ctx.BackendType,

		"risk_bucket":          riskBucketMap,
		"risk_available":       ctx.RateLimit.RiskBucket.Available,
		"risk_capacity":        ctx.RateLimit.RiskBucket.Capacity,
		"risk_refill_rate":     ctx.RateLimit.RiskBucket.RefillRate,
		"resource_bucket":      resourceBucketMap,
		"resource_available":   ctx.RateLimit.ResourceBucket.Available,
		"resource_capacity":    ctx.RateLimit.ResourceBucket.Capacity,
		"resource_refill_rate": ctx.RateLimit.ResourceBucket.RefillRate,
	})
}

// shouldUpdateDecision determines if the new decision should override the current one
func shouldUpdateDecision(current, new EnforcerDecision, severityPriority map[SeverityLevel]int) bool {
	// Action priority: DENY > PENDING_APPROVAL > WARN > ALLOW
	actionPriority := map[Action]int{
		ActionDeny:            4,
		ActionPendingApproval: 3,
		ActionWarn:            2,
		ActionAllow:           1,
	}

	// Compare action priority
	newPriority := actionPriority[new.Action]
	currentPriority := actionPriority[current.Action]

	if newPriority > currentPriority {
		return true
	}

	// If same action, compare severity
	if newPriority == currentPriority {
		newSev := severityPriority[new.Severity]
		currentSev := severityPriority[current.Severity]
		return newSev > currentSev
	}

	return false
}

// ValidateExpression checks if a CEL expression is valid
func (e *CELEngine) ValidateExpression(expression string) error {
	ast, issues := e.env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("parse error: %w", issues.Err())
	}

	_, issues = e.env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("type check error: %w", issues.Err())
	}

	return nil
}

// ListPolicies returns all registered policies
func (e *CELEngine) ListPolicies() []CELPolicy {
	policies := make([]CELPolicy, 0, len(e.policies))
	for _, policy := range e.policies {
		policies = append(policies, policy)
	}
	return policies
}

// celEnforcerLib provides custom CEL functions for the enforcer
type celEnforcerLib struct{}

func (celEnforcerLib) CompileOptions() []cel.EnvOption {
	// CEL provides contains(), matches(), and in operators natively
	// No custom functions needed
	return []cel.EnvOption{}
}

func (celEnforcerLib) ProgramOptions() []cel.ProgramOption {
	// CEL provides contains(), matches(), and in operators natively
	// No custom functions needed
	return []cel.ProgramOption{}
}

// Ensure CELEngine implements PolicyEngine
var _ PolicyEngine = (*CELEngine)(nil)
