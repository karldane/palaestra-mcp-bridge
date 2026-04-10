// Package enforcer provides policy enforcement and safety metadata for MCP Bridge
// following the Hybrid Interceptor architecture described in ENFORCER_SPEC.md
package enforcer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Action represents the enforcement decision from CEL policy evaluation
type Action string

const (
	// ActionAllow permits the operation immediately
	ActionAllow Action = "ALLOW"
	// ActionDeny blocks the operation with an error
	ActionDeny Action = "DENY"
	// ActionWarn requires user confirmation before proceeding
	ActionWarn Action = "WARN"
	// ActionPendingApproval queues the operation for admin human approval (legacy alias)
	ActionPendingApproval Action = "PENDING_APPROVAL"
	// ActionPendingUserApproval queues the operation for user-level human approval (new Tier 3)
	ActionPendingUserApproval Action = "PENDING_USER_APPROVAL"
	// ActionPendingAdminApproval queues the operation for admin human approval (Tier 4)
	ActionPendingAdminApproval Action = "PENDING_ADMIN_APPROVAL"
)

// SeverityLevel represents the risk severity for violations
type SeverityLevel string

const (
	SeverityCritical SeverityLevel = "CRITICAL"
	SeverityHigh     SeverityLevel = "HIGH"
	SeverityMedium   SeverityLevel = "MEDIUM"
	SeverityLow      SeverityLevel = "LOW"
)

// ImpactScope represents the operational impact category
type ImpactScope string

const (
	ImpactRead   ImpactScope = "read"
	ImpactWrite  ImpactScope = "write"
	ImpactDelete ImpactScope = "delete"
	ImpactAdmin  ImpactScope = "admin"
)

// RiskLevel represents the safety risk classification
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// SafetyProfile contains the resolved safety metadata for a tool
type SafetyProfile struct {
	// Tool identification
	ToolName    string
	BackendID   string
	BackendName string

	// Core safety attributes (from framework or inferred)
	Risk         RiskLevel
	Impact       ImpactScope
	Cost         int // 1-10 resource cost
	RequiresHITL bool
	PIIExposure  bool

	// Source tracking
	Source string // "config", "self_reported", "inferred"

	// CEL evaluation context
	Metadata map[string]interface{}
}

// String returns formatted safety profile for logging
func (sp *SafetyProfile) String() string {
	return fmt.Sprintf("SafetyProfile{tool=%s, risk=%s, impact=%s, cost=%d, hitl=%v}",
		sp.ToolName, sp.Risk, sp.Impact, sp.Cost, sp.RequiresHITL)
}

// EnforcerDecision represents the outcome of policy evaluation
type EnforcerDecision struct {
	Action     Action
	Severity   SeverityLevel
	Message    string
	PolicyID   string
	Violations []string
	Timestamp  time.Time
	// Priority mirrors the DB priority of the policy that produced this decision.
	// Lower = more specific. Used for tiebreaking in shouldUpdateDecision.
	Priority int
}

// DecisionContext provides all context for CEL evaluation
type DecisionContext struct {
	// Identity
	UserID     string
	UserRole   string
	UserEmail  string
	TrustLevel int // 0-100

	// Tool information
	Tool          string
	Args          map[string]interface{}
	Justification string // NEW - required justification for tool calls
	Safety        SafetyProfile

	// System context
	SystemLoad float64
	CallRate   int // Number of calls in the current rate window
	Timestamp  time.Time

	// Backend context
	BackendID   string
	BackendType string

	// Rate limiting context
	RateLimit RateLimitInfo

	// Original request for replay after approval
	RequestBody string
}

// RateLimitInfo contains rate limit bucket state for evaluation
type RateLimitInfo struct {
	RiskBucket     BucketStatus
	ResourceBucket BucketStatus
}

// BucketStatus represents the current state of a bucket
type BucketStatus struct {
	Available  int // Current available tokens
	Capacity   int // Max capacity
	RefillRate int // Tokens per minute
}

// NewRateLimitInfo creates a RateLimitInfo from bucket states
func NewRateLimitInfo(riskAvailable, riskCapacity, riskRefill, resourceAvailable, resourceCapacity, resourceRefill int) RateLimitInfo {
	return RateLimitInfo{
		RiskBucket: BucketStatus{
			Available:  riskAvailable,
			Capacity:   riskCapacity,
			RefillRate: riskRefill,
		},
		ResourceBucket: BucketStatus{
			Available:  resourceAvailable,
			Capacity:   resourceCapacity,
			RefillRate: resourceRefill,
		},
	}
}

// CELPolicy represents a single policy rule for CEL evaluation
type CELPolicy struct {
	ID          string
	Description string
	Expression  string
	Action      Action
	Message     string
	Severity    SeverityLevel
	Enabled     bool
	// Priority mirrors the DB priority column. Lower number = more specific rule.
	// Used as a tiebreaker when action and severity are equal: lower priority wins.
	Priority int
}

// PolicySet represents a collection of policies for a specific scope
type PolicySet struct {
	ID          string
	Name        string
	Description string
	Scope       PolicyScope
	Rules       []CELPolicy
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PolicyScope defines the applicability of a policy set
type PolicyScope string

const (
	PolicyScopeGlobal  PolicyScope = "global"
	PolicyScopeBackend PolicyScope = "backend"
	PolicyScopeTool    PolicyScope = "tool"
)

// ApprovalRequest represents a pending operation requiring human approval
type ApprovalRequest struct {
	ID              string
	DecisionContext DecisionContext
	RequestedAt     time.Time
	ExpiresAt       time.Time
	Status          ApprovalStatus
	ApprovedBy      string
	ApprovedAt      *time.Time
	DenialReason    string
	Comments        string
}

// ApprovalStatus represents the state of an approval request
type ApprovalStatus string

const (
	ApprovalStatusPending   ApprovalStatus = "PENDING"
	ApprovalStatusApproved  ApprovalStatus = "APPROVED"
	ApprovalStatusDenied    ApprovalStatus = "DENIED"
	ApprovalStatusExpired   ApprovalStatus = "EXPIRED"
	ApprovalStatusCancelled ApprovalStatus = "CANCELLED"
)

// IsExpired checks if the approval request has timed out
func (ar *ApprovalRequest) IsExpired() bool {
	return time.Now().After(ar.ExpiresAt)
}

// KillSwitch represents emergency controls
type KillSwitch struct {
	ID        string
	Name      string
	Scope     string // "global" or backend ID
	Enabled   bool
	EnabledAt *time.Time
	EnabledBy string
	Reason    string
}

// Registry stores and resolves safety profiles for tools
type Registry interface {
	// Resolve determines the safety profile for a tool using tiered priority
	Resolve(toolName string, backendID string) (SafetyProfile, error)

	// RegisterOverride adds a manual override for a tool
	RegisterOverride(toolName, backendID string, profile SafetyProfile) error

	// RemoveOverride removes a manual override
	RemoveOverride(toolName, backendID string) error

	// GetProfile retrieves the current safety profile for a tool
	GetProfile(toolName string) (SafetyProfile, bool)
}

// PolicyEngine evaluates CEL expressions against decision context
type PolicyEngine interface {
	// Evaluate runs all applicable policies and returns the highest-severity decision
	Evaluate(ctx context.Context, context DecisionContext) (EnforcerDecision, error)

	// ValidateExpression checks if a CEL expression is valid
	ValidateExpression(expression string) error
}

// ApprovalQueue manages pending approval requests
type ApprovalQueue interface {
	// Create adds a new approval request to the queue
	Create(ctx context.Context, req ApprovalRequest) (string, error)

	// Get retrieves an approval request by ID
	Get(ctx context.Context, id string) (ApprovalRequest, error)

	// List returns all pending approval requests
	List(ctx context.Context, status ApprovalStatus) ([]ApprovalRequest, error)

	// Approve marks a request as approved
	Approve(ctx context.Context, id string, approverID string, comments string) error

	// Deny marks a request as denied
	Deny(ctx context.Context, id string, approverID string, reason string) error

	// CleanupExpired removes expired requests
	CleanupExpired(ctx context.Context) error
}

// DescriptionDecorator modifies tool descriptions to include safety metadata
type DescriptionDecorator interface {
	// Decorate adds safety prefixes to tool descriptions
	Decorate(description string, profile SafetyProfile) string

	// DecorateWithDecision adds decision information to responses
	DecorateWithDecision(description string, decision EnforcerDecision) string
}

// DefaultDescriptionDecorator implements the standard decoration logic
type DefaultDescriptionDecorator struct{}

// Decorate implements the [POLICY: ...] [RISK: ...] prefixing from the spec
func (d *DefaultDescriptionDecorator) Decorate(description string, profile SafetyProfile) string {
	prefix := fmt.Sprintf("[POLICY: %s | RISK: %s]",
		strings.ToUpper(string(profile.Impact)),
		strings.ToUpper(string(profile.Risk)))

	if profile.RequiresHITL {
		prefix += " [REQUIRES HUMAN APPROVAL]"
	}

	return prefix + " " + description
}

// DecorateWithDecision adds decision context for warning scenarios
func (d *DefaultDescriptionDecorator) DecorateWithDecision(description string, decision EnforcerDecision) string {
	if decision.Action == ActionWarn {
		return fmt.Sprintf("[WARNING: %s] %s - %s",
			decision.Severity,
			description,
			decision.Message)
	}
	return description
}

// Ensure DefaultDescriptionDecorator implements the interface
var _ DescriptionDecorator = (*DefaultDescriptionDecorator)(nil)

// EnforcerConfig represents the complete configuration for the enforcer
type EnforcerConfig struct {
	Enabled                     bool
	DefaultAction               Action
	PolicyFile                  string
	ApprovalTimeout             time.Duration // Default: 24 hours
	AdminApprovalTimeout        time.Duration // Default: 0 (no timeout for admin queue)
	UserApprovalTimeout         time.Duration // Default: 10 minutes for user queue
	CleanupInterval             time.Duration // How often to cleanup expired requests
	RetentionPeriod             time.Duration // How long to keep completed/denied requests
	EnableDescriptionDecoration bool
	EnableKillSwitch            bool
	MinJustificationLength      int           // Default: 40; set to 0 to disable length check
	RateWindowDuration          time.Duration // Default: 60 seconds
	RateDefaultThreshold        int           // Default: 10 calls per window
}

// DefaultEnforcerConfig returns sensible defaults
func DefaultEnforcerConfig() EnforcerConfig {
	return EnforcerConfig{
		Enabled:                     true,
		DefaultAction:               ActionAllow,
		ApprovalTimeout:             24 * time.Hour,
		AdminApprovalTimeout:        0,                // No timeout for admin
		UserApprovalTimeout:         10 * time.Minute, // 10 min for user
		CleanupInterval:             1 * time.Minute,
		RetentionPeriod:             7 * 24 * time.Hour, // 7 days
		EnableDescriptionDecoration: true,
		EnableKillSwitch:            true,
		MinJustificationLength:      40,
		RateWindowDuration:          60 * time.Second,
		RateDefaultThreshold:        10,
	}
}

// ErrPolicyViolation is returned when a policy denies an operation
var ErrPolicyViolation = fmt.Errorf("policy violation")

// ErrApprovalRequired is returned when an operation requires human approval
var ErrApprovalRequired = fmt.Errorf("human approval required")

// ErrKillSwitchActive is returned when a kill switch is active
var ErrKillSwitchActive = fmt.Errorf("emergency kill switch is active")

// ErrRateLimitExceeded is returned when a rate limit bucket is exhausted
var ErrRateLimitExceeded = fmt.Errorf("rate limit exceeded")

// CallOptions carries per-call flags into the enforcer.
// Extend this struct for future per-call requirements without signature churn.
type CallOptions struct {
	SkipJustification bool // if true, justification gate is bypassed for this call
}

// IsDenyAction checks if the action represents a hard block
func IsDenyAction(action Action) bool {
	return action == ActionDeny
}

// RequiresApproval checks if the action requires human intervention
func RequiresApproval(action Action) bool {
	return action == ActionPendingApproval || action == ActionPendingUserApproval || action == ActionPendingAdminApproval
}

// RequiresUserApproval checks if the action requires user-level approval
func RequiresUserApproval(action Action) bool {
	return action == ActionPendingUserApproval
}

// RequiresAdminApproval checks if the action requires admin-level approval
func RequiresAdminApproval(action Action) bool {
	return action == ActionPendingApproval || action == ActionPendingAdminApproval
}

// RequiresWarning checks if the action requires user confirmation
func RequiresWarning(action Action) bool {
	return action == ActionWarn
}
