package enforcer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"database/sql"
)

// EnforcerStore interface for database operations
type EnforcerStore interface {
	CreateApprovalRequest(req ApprovalRequestRow) error
	GetApprovalRequest(id string) (ApprovalRequestRow, error)
	IsKillSwitchActive(scope string) (bool, error)
	EnableKillSwitch(scope string, userID string, reason string) error
	DisableKillSwitch(scope string) error
	ListPolicies() ([]PolicyRow, error)
	CleanupExpiredApprovals() error
	LogAuditEvent(requestID string, userID string, toolName string, action string, policyID string, message string, context map[string]interface{}) error
}

// PolicyRow represents a policy in the database
type PolicyRow struct {
	ID          string
	Name        string
	Description string
	Scope       string
	Expression  string
	Action      string
	Severity    string
	Message     string
	Enabled     bool
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ToCELPolicy converts PolicyRow to CELPolicy
func (row PolicyRow) ToCELPolicy() CELPolicy {
	return CELPolicy{
		ID:          row.ID,
		Description: row.Description,
		Expression:  row.Expression,
		Action:      Action(row.Action),
		Severity:    SeverityLevel(row.Severity),
		Message:     row.Message,
		Enabled:     row.Enabled,
	}
}

// ApprovalRequestRow represents an approval request in the database
type ApprovalRequestRow struct {
	ID            string
	UserID        string
	UserEmail     string
	UserRole      string
	TrustLevel    int
	ToolName      string
	ToolArgs      string
	BackendID     string
	SafetyProfile string
	Status        string
	RequestedAt   time.Time
	ExpiresAt     time.Time
	ApprovedBy    sql.NullString
	ApprovedAt    sql.NullTime
	DenialReason  string
	Comments      string
	PolicyID      string
	ViolationMsg  string
}

// Enforcer is the main orchestrator for policy enforcement
type Enforcer struct {
	config       EnforcerConfig
	resolver     *MetadataResolver
	engine       *CELEngine
	store        EnforcerStore
	decorator    DescriptionDecorator
	killSwitches map[string]bool
	mu           sync.RWMutex
}

// NewEnforcer creates a new enforcer instance
func NewEnforcer(config EnforcerConfig, store EnforcerStore) (*Enforcer, error) {
	engine, err := NewCELEngine()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL engine: %w", err)
	}

	e := &Enforcer{
		config:       config,
		resolver:     NewMetadataResolver(),
		engine:       engine,
		store:        store,
		decorator:    &DefaultDescriptionDecorator{},
		killSwitches: make(map[string]bool),
	}

	// Load policies from database
	if err := e.loadPolicies(); err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	// Start cleanup goroutine for expired approvals
	if config.CleanupInterval > 0 {
		go e.cleanupLoop()
	}

	return e, nil
}

// Evaluate checks if a tool call should be allowed
func (e *Enforcer) Evaluate(ctx context.Context, decisionCtx DecisionContext) (EnforcerDecision, error) {
	// Check kill switches first
	if e.isKillSwitchActive(decisionCtx.BackendID) {
		return EnforcerDecision{
			Action:   ActionDeny,
			Severity: SeverityCritical,
			Message:  "Emergency kill switch is active",
		}, ErrKillSwitchActive
	}

	// Evaluate policies
	decision, err := e.engine.Evaluate(ctx, decisionCtx)
	if err != nil {
		return EnforcerDecision{}, fmt.Errorf("policy evaluation failed: %w", err)
	}

	// Log the decision
	e.logDecision(decisionCtx, decision)

	return decision, nil
}

// HandleToolCall is the main entry point for enforcing policies on tool calls
func (e *Enforcer) HandleToolCall(ctx context.Context, userID string, toolName string, args map[string]interface{}, backendID string) (EnforcerDecision, error) {
	// Build decision context
	decisionCtx := DecisionContext{
		UserID:    userID,
		Tool:      toolName,
		Args:      args,
		BackendID: backendID,
	}

	// Resolve safety profile
	profile, err := e.resolver.Resolve(toolName, backendID, nil)
	if err != nil {
		return EnforcerDecision{}, fmt.Errorf("failed to resolve safety profile: %w", err)
	}
	decisionCtx.Safety = profile
	fmt.Printf("DEBUG: HandleToolCall resolved profile - Risk=%s Impact=%s Cost=%d\n", profile.Risk, profile.Impact, profile.Cost)

	// Evaluate
	decision, err := e.Evaluate(ctx, decisionCtx)
	if err != nil {
		return EnforcerDecision{}, err
	}
	fmt.Printf("DEBUG: HandleToolCall decision - Action=%s PolicyID=%s\n", decision.Action, decision.PolicyID)
	return decision, nil
}

// RequestApproval creates a new approval request for HITL
func (e *Enforcer) RequestApproval(ctx context.Context, decisionCtx DecisionContext, policyID string, message string) (string, error) {
	id := generateID()

	safetyJSON, _ := json.Marshal(decisionCtx.Safety)
	argsJSON, _ := json.Marshal(decisionCtx.Args)

	req := ApprovalRequestRow{
		ID:            id,
		UserID:        decisionCtx.UserID,
		UserEmail:     decisionCtx.UserEmail,
		UserRole:      decisionCtx.UserRole,
		TrustLevel:    decisionCtx.TrustLevel,
		ToolName:      decisionCtx.Tool,
		ToolArgs:      string(argsJSON),
		BackendID:     decisionCtx.BackendID,
		SafetyProfile: string(safetyJSON),
		Status:        "PENDING",
		RequestedAt:   time.Now(),
		ExpiresAt:     time.Now().Add(e.config.ApprovalTimeout),
		PolicyID:      policyID,
		ViolationMsg:  message,
	}

	if err := e.store.CreateApprovalRequest(req); err != nil {
		return "", fmt.Errorf("failed to create approval request: %w", err)
	}

	return id, nil
}

// CheckApproval checks if an approval request has been approved
func (e *Enforcer) CheckApproval(ctx context.Context, approvalID string) (bool, error) {
	req, err := e.store.GetApprovalRequest(approvalID)
	if err != nil {
		return false, err
	}

	if req.Status == "APPROVED" {
		return true, nil
	}

	if req.Status == "DENIED" {
		return false, fmt.Errorf("request was denied: %s", req.DenialReason)
	}

	if req.Status == "EXPIRED" || time.Now().After(req.ExpiresAt) {
		return false, fmt.Errorf("approval request expired")
	}

	return false, nil // Still pending
}

// DecorateDescription adds safety metadata to tool descriptions
func (e *Enforcer) DecorateDescription(description string, toolName string, backendID string) string {
	if !e.config.EnableDescriptionDecoration {
		return description
	}

	profile, err := e.resolver.Resolve(toolName, backendID, nil)
	if err != nil {
		return description
	}

	return e.decorator.Decorate(description, profile)
}

// EnableKillSwitch activates emergency kill switch
func (e *Enforcer) EnableKillSwitch(scope string, userID string, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.store.EnableKillSwitch(scope, userID, reason); err != nil {
		return err
	}

	e.killSwitches[scope] = true
	return nil
}

// DisableKillSwitch deactivates emergency kill switch
func (e *Enforcer) DisableKillSwitch(scope string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.store.DisableKillSwitch(scope); err != nil {
		return err
	}

	delete(e.killSwitches, scope)
	return nil
}

// IsKillSwitchActive checks if kill switch is enabled
func (e *Enforcer) IsKillSwitchActive(scope string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Check specific scope
	if e.killSwitches[scope] {
		return true
	}

	// Check global
	return e.killSwitches["global"]
}

// AddPolicy adds a new policy to the enforcer
func (e *Enforcer) AddPolicy(policy CELPolicy) error {
	return e.engine.AddPolicy(policy)
}

// loadPolicies loads policies from database into CEL engine
func (e *Enforcer) loadPolicies() error {
	policies, err := e.store.ListPolicies()
	if err != nil {
		return err
	}

	for _, p := range policies {
		celPolicy := p.ToCELPolicy()
		if err := e.engine.AddPolicy(celPolicy); err != nil {
			// Log error but continue loading other policies
			log.Printf("Failed to add policy %s: %v", p.ID, err)
		}
	}

	return nil
}

// logDecision records the policy decision in audit log
func (e *Enforcer) logDecision(ctx DecisionContext, decision EnforcerDecision) {
	context := map[string]interface{}{
		"risk":          ctx.Safety.Risk,
		"impact":        ctx.Safety.Impact,
		"cost":          ctx.Safety.Cost,
		"requires_hitl": ctx.Safety.RequiresHITL,
	}

	e.store.LogAuditEvent(
		generateID(),
		ctx.UserID,
		ctx.Tool,
		string(decision.Action),
		decision.PolicyID,
		decision.Message,
		context,
	)
}

// cleanupLoop periodically cleans up expired approvals
func (e *Enforcer) cleanupLoop() {
	ticker := time.NewTicker(e.config.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		if err := e.store.CleanupExpiredApprovals(); err != nil {
			log.Printf("Failed to cleanup expired approvals: %v", err)
		}
	}
}

// isKillSwitchActive checks both memory and database
func (e *Enforcer) isKillSwitchActive(scope string) bool {
	if !e.config.EnableKillSwitch {
		return false
	}

	active, _ := e.store.IsKillSwitchActive(scope)
	return active
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
