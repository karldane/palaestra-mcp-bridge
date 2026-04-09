package enforcer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"database/sql"

	"github.com/google/uuid"

	rl "github.com/mcp-bridge/mcp-bridge/ratelimit"
)

// EnforcerStore interface for database operations
type EnforcerStore interface {
	CreatePolicy(policy PolicyRow) error
	GetPolicy(id string) (PolicyRow, error)
	ListPolicies() ([]PolicyRow, error)
	DeletePolicy(id string) error
	UpdatePolicy(policy PolicyRow) error
	CreateApprovalRequest(req ApprovalRequestRow) error
	GetApprovalRequest(id string) (ApprovalRequestRow, error)
	ListPendingApprovals() ([]ApprovalRequestRow, error)
	ListUserPendingApprovals() ([]ApprovalRequestRow, error)
	ListAdminPendingApprovals() ([]ApprovalRequestRow, error)
	ListAllApprovals() ([]ApprovalRequestRow, error)
	ApproveRequest(id string, approverID string, comments string) error
	DenyRequest(id string, approverID string, reason string) error
	MarkExecuting(id string) error
	MarkCompleted(id string, responseStatus int, responseBody string) error
	MarkFailed(id string, errorMsg string) error
	IsKillSwitchActive(scope string) (bool, error)
	EnableKillSwitch(scope string, userID string, reason string) error
	DisableKillSwitch(scope string) error
	CleanupExpiredApprovals() error
	CleanupOldApprovals(olderThan time.Duration) error
	LogAuditEvent(requestID string, userID string, toolName string, action string, policyID string, message string, context map[string]interface{}) error
	LogAuditRejection(requestID, userID, toolName, justification, rejectionReason string) error
	GetToolProfile(backendID, toolName string) (ToolProfileRow, error)
	ListOverrides() ([]EnforcerOverrideRow, error)
	ListUserOverrides(userID string) ([]EnforcerOverrideRow, error)
	UpsertOverride(override EnforcerOverrideRow) error
	DeleteOverride(toolName, backendID string) error
	UpsertToolProfile(profile ToolProfileRow) error
	ListRateLimitBucketConfigs() ([]RateLimitBucketConfigRow, error)
	UpsertRateLimitBucketConfig(config RateLimitBucketConfigRow) error
	ListRateLimitStates() ([]RateLimitStateRow, error)
	UpsertRateLimitState(state RateLimitStateRow) error
	CountUserPendingApprovals() (int, error)
	CountAdminPendingApprovals() (int, error)
	IncrementRateBucket(userID, toolName string, windowDuration time.Duration) (int, error)
	GetCallRate(userID, toolName string, windowDuration time.Duration) (int, error)
	CleanupExpiredRateBuckets(windowDuration time.Duration) error
}

// EnforcerOverrideRow represents a manual override for a tool's safety profile.
type EnforcerOverrideRow struct {
	ID           string
	ToolName     string
	BackendID    string
	RiskLevel    string
	ImpactScope  string
	ResourceCost int
	RequiresHITL bool
	PIIExposure  bool
	UserID       string // empty = admin-scoped override; non-empty = personal user override
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RateLimitBucketConfigRow represents a bucket configuration in the database
type RateLimitBucketConfigRow struct {
	ID         string
	BackendID  string
	BucketType string // "risk" or "resource"
	Capacity   int
	RefillRate int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// RateLimitStateRow represents a user's bucket state in the database
type RateLimitStateRow struct {
	ID           string
	UserID       string
	BackendID    string
	BucketType   string // "risk" or "resource"
	CurrentLevel int
	LastRefillAt time.Time
	CreatedAt    time.Time
}

// ApprovalExecutor executes approved requests
type ApprovalExecutor interface {
	ExecuteRequest(userID string, backendID string, requestBody string) (int, string, error)
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
	Locked      bool // if true, user personal overrides are blocked for tools resolved by this policy
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
	ID             string
	UserID         string
	UserEmail      string
	UserRole       string
	TrustLevel     int
	ToolName       string
	ToolArgs       string
	BackendID      string
	SafetyProfile  string
	Status         string
	QueueType      string // "user" or "admin"
	Justification  string // The justification provided by the agent
	RequestedAt    time.Time
	ExpiresAt      time.Time
	ApprovedBy     sql.NullString
	ApprovedAt     sql.NullTime
	DenialReason   string
	Comments       string
	PolicyID       string
	ViolationMsg   string
	RequestBody    string       // Original JSON-RPC request body for replay after approval
	ResponseStatus int          // HTTP status code from execution
	ResponseBody   string       // Full response JSON from execution
	ExecutedAt     sql.NullTime // When execution completed
	ErrorMsg       string       // Error message if failed
}

// User represents a user for policy evaluation
type User struct {
	ID    string
	Email string
	Role  string
}

// UserStore defines the interface for fetching user info
type UserStore interface {
	GetUser(id string) (*User, error)
}

// Enforcer is the main orchestrator for policy enforcement
type Enforcer struct {
	config       EnforcerConfig
	resolver     *MetadataResolver
	engine       *CELEngine
	store        EnforcerStore
	userStore    UserStore
	decorator    DescriptionDecorator
	executor     ApprovalExecutor
	killSwitches map[string]bool
	rateLimit    *rl.RateLimitManager
	mu           sync.RWMutex
}

// NewEnforcer creates a new enforcer instance
func NewEnforcer(config EnforcerConfig, store EnforcerStore, userStore UserStore) (*Enforcer, error) {
	engine, err := NewCELEngine()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL engine: %w", err)
	}

	e := &Enforcer{
		config:       config,
		resolver:     NewMetadataResolver(store),
		engine:       engine,
		store:        store,
		userStore:    userStore,
		decorator:    &DefaultDescriptionDecorator{},
		killSwitches: make(map[string]bool),
		rateLimit:    rl.NewRateLimitManager(),
	}

	// Load policies from database
	if err := e.loadPolicies(); err != nil {
		return nil, fmt.Errorf("failed to load policies: %w", err)
	}

	// Start cleanup goroutine for expired approvals
	if config.CleanupInterval > 0 {
		go e.cleanupLoop()
	}

	// Set default rate limits for common backends
	e.SetDefaultRateLimits()

	// Load persisted rate limit states from database
	if err := e.LoadRateLimitStates(); err != nil {
		log.Printf("Warning: failed to load rate limit states: %v", err)
	}

	return e, nil
}

// SetDefaultRateLimits sets the default rate limit configuration for common backends
func (e *Enforcer) SetDefaultRateLimits() {
	// Slack: moderate usage, mostly reads with some writes
	e.rateLimit.SetDefaultConfig("slack", 100, 20, 200, 40)

	// New Relic: read-heavy, queries and logs
	e.rateLimit.SetDefaultConfig("newrelic", 150, 30, 300, 60)

	// Oracle: conservative, database operations
	e.rateLimit.SetDefaultConfig("oracle", 50, 10, 100, 20)

	// GitHub: moderate, CI/CD integration, PRs, issues
	e.rateLimit.SetDefaultConfig("github", 100, 20, 200, 40)

	// CircleCI: moderate, pipeline operations
	e.rateLimit.SetDefaultConfig("circleci", 80, 16, 160, 32)

	// K8s: moderate, cluster operations
	e.rateLimit.SetDefaultConfig("k8s", 100, 20, 200, 40)

	// AWS: moderate, cloud operations
	e.rateLimit.SetDefaultConfig("aws", 100, 20, 200, 40)

	// Atlassian: moderate, Jira/Confluence operations
	e.rateLimit.SetDefaultConfig("atlassian", 100, 20, 200, 40)

	// Qdrant: agent memory, high write volume expected (remember, save_progress, log_event, etc.)
	e.rateLimit.SetDefaultConfig("qdrant", 80, 15, 160, 30)

	// MongoDB: disabled by default, conservative if enabled
	e.rateLimit.SetDefaultConfig("mongodb", 60, 12, 120, 24)

	// AppScan ASoC: very conservative, scans are expensive (cost 10) and slow
	e.rateLimit.SetDefaultConfig("appscan_asoc", 40, 8, 80, 16)

	// MCP Bridge built-in: generous, system operations
	e.rateLimit.SetDefaultConfig("mcpbridge", 200, 40, 400, 80)
}

// SetRateLimitConfig sets the rate limit configuration for a specific backend
func (e *Enforcer) SetRateLimitConfig(backendID string, riskCapacity, riskRefill, resourceCapacity, resourceRefill int) {
	e.rateLimit.SetDefaultConfig(backendID, riskCapacity, riskRefill, resourceCapacity, resourceRefill)
}

// GetRateLimitStatus returns the current rate limit status for a user/backend
func (e *Enforcer) GetRateLimitStatus(userID, backendID string) map[string]interface{} {
	return e.rateLimit.GetStatusMap(userID, backendID)
}

// LoadRateLimitStates loads rate limit states from the database into the in-memory manager
func (e *Enforcer) LoadRateLimitStates() error {
	states, err := e.store.ListRateLimitStates()
	if err != nil {
		return err
	}

	dbStates := make([]rl.BucketStateForDB, 0, len(states))
	for _, s := range states {
		dbStates = append(dbStates, rl.BucketStateForDB{
			UserID:       s.UserID,
			BackendID:    s.BackendID,
			BucketType:   s.BucketType,
			CurrentLevel: s.CurrentLevel,
			LastRefillAt: s.LastRefillAt,
		})
	}
	e.rateLimit.LoadStates(dbStates)
	return nil
}

// SaveRateLimitStates persists current in-memory rate limit states to the database
func (e *Enforcer) SaveRateLimitStates() error {
	states := e.rateLimit.GetAllStates()
	for _, s := range states {
		row := RateLimitStateRow{
			ID:           generateID(),
			UserID:       s.UserID,
			BackendID:    s.BackendID,
			BucketType:   s.BucketType,
			CurrentLevel: s.CurrentLevel,
			LastRefillAt: s.LastRefillAt,
			CreatedAt:    time.Now(),
		}
		if err := e.store.UpsertRateLimitState(row); err != nil {
			return err
		}
	}
	return nil
}

// StartRateLimitRefill starts background goroutines that refill and persist rate limit buckets.
// Call StopRateLimitRefill to terminate.
func (e *Enforcer) StartRateLimitRefill(ctx context.Context, interval time.Duration) {
	e.rateLimit.StartRefillTicker(ctx, interval)
	e.rateLimit.SetPersistFunc(func() error {
		return e.SaveRateLimitStates()
	})
	e.rateLimit.StartPersistTicker(ctx, 30*time.Second)
}

// StopRateLimitRefill terminates the rate limit refill and persistence goroutines
func (e *Enforcer) StopRateLimitRefill() {
	e.rateLimit.Stop()
	e.rateLimit.StopPersist()
	e.SaveRateLimitStates()
}

// RateLimitStateDisplay represents a user's bucket state for display
type RateLimitStateDisplay struct {
	UserID       string
	BackendID    string
	BucketType   string
	Capacity     int
	CurrentLevel int
	RefillRate   int
}

// RateLimitConfigDisplay represents a backend's rate limit configuration
type RateLimitConfigDisplay struct {
	BackendID    string
	RiskCapacity int
	RiskRefill   int
	ResCapacity  int
	ResRefill    int
}

// GetAllRateLimitStates returns all bucket states with capacity info
func (e *Enforcer) GetAllRateLimitStates() []RateLimitStateDisplay {
	states := e.rateLimit.GetAllStates()
	result := make([]RateLimitStateDisplay, 0, len(states))
	for _, s := range states {
		result = append(result, RateLimitStateDisplay{
			UserID:       s.UserID,
			BackendID:    s.BackendID,
			BucketType:   s.BucketType,
			Capacity:     s.Capacity,
			CurrentLevel: s.CurrentLevel,
			RefillRate:   s.RefillRate,
		})
	}
	return result
}

// GetRateLimitConfigs returns configured backend rate limits
func (e *Enforcer) GetRateLimitConfigs() []RateLimitConfigDisplay {
	configs := e.rateLimit.GetAllConfigs()
	result := make([]RateLimitConfigDisplay, 0, len(configs))
	for _, c := range configs {
		result = append(result, RateLimitConfigDisplay{
			BackendID:    c.BackendID,
			RiskCapacity: c.RiskCapacity,
			RiskRefill:   c.RiskRefill,
			ResCapacity:  c.ResCapacity,
			ResRefill:    c.ResRefill,
		})
	}
	return result
}

// ResetUserRateLimit resets a user's rate limit buckets for a specific backend
func (e *Enforcer) ResetUserRateLimit(userID, backendID string) error {
	e.rateLimit.ResetUserBuckets(userID, backendID)
	return nil
}

// RegisterOverride registers a manual override for a tool's safety profile.
func (e *Enforcer) RegisterOverride(toolName, backendID string, profile SafetyProfile) error {
	return e.resolver.RegisterOverride(toolName, backendID, profile)
}

// RemoveOverride removes a manual override.
func (e *Enforcer) RemoveOverride(toolName, backendID string) error {
	return e.resolver.RemoveOverride(toolName, backendID)
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
func (e *Enforcer) HandleToolCall(ctx context.Context, userID string, toolName string, args map[string]interface{}, backendID string, justification string) (EnforcerDecision, error) {
	// Build decision context
	decisionCtx := DecisionContext{
		UserID:        userID,
		Tool:          toolName,
		Args:          args,
		BackendID:     backendID,
		Justification: justification,
	}

	// Fetch user info for policy evaluation
	if e.userStore != nil && userID != "" {
		if user, err := e.userStore.GetUser(userID); err == nil {
			// Access fields directly from *User
			decisionCtx.UserEmail = user.Email
			decisionCtx.UserRole = user.Role
			// Trust level: admins get 100, otherwise default to 50
			if decisionCtx.UserRole == "admin" {
				decisionCtx.TrustLevel = 100
			} else {
				decisionCtx.TrustLevel = 50
			}
		}
	}

	// Resolve safety profile
	profile, err := e.resolver.ResolveForUser(toolName, backendID, userID)
	if err != nil {
		return EnforcerDecision{}, fmt.Errorf("failed to resolve safety profile: %w", err)
	}
	decisionCtx.Safety = profile
	fmt.Printf("DEBUG: HandleToolCall resolved profile - Risk=%s Impact=%s Cost=%d\n", profile.Risk, profile.Impact, profile.Cost)

	// Pre-policy justification validation gate
	if e.config.MinJustificationLength > 0 {
		if justification == "" {
			_ = e.store.LogAuditRejection(generateID(), userID, toolName, justification, "missing_justification")
			return EnforcerDecision{
				Action:   ActionDeny,
				Severity: SeverityMedium,
				Message:  "Tool call rejected: justification is required.",
				PolicyID: "justification_required",
			}, ErrPolicyViolation
		}
		if len(justification) < e.config.MinJustificationLength {
			_ = e.store.LogAuditRejection(generateID(), userID, toolName, justification, "justification_too_short")
			return EnforcerDecision{
				Action:   ActionDeny,
				Severity: SeverityMedium,
				Message:  fmt.Sprintf("Tool call rejected: justification must be at least %d characters. Provided: %d characters.", e.config.MinJustificationLength, len(justification)),
				PolicyID: "justification_too_short",
			}, ErrPolicyViolation
		}
	}

	// Calculate cost
	riskCost, resourceCost := rl.CalculateCost(profile.Cost, string(profile.Risk), string(profile.Impact))

	// Get bucket status for CEL context
	riskAvail, riskCap, riskRefill, resAvail, resCap, resRefill := e.rateLimit.GetBucketStatus(userID, backendID)
	decisionCtx.RateLimit = NewRateLimitInfo(riskAvail, riskCap, riskRefill, resAvail, resCap, resRefill)

	// Check rate limits before evaluating policies
	riskAllowed, resourceAllowed := e.rateLimit.CheckAndConsume(userID, backendID, riskCost, resourceCost)

	// If either bucket is exhausted, deny the call
	if !riskAllowed || !resourceAllowed {
		bucketType := "risk"
		available := riskAvail
		if !resourceAllowed {
			bucketType = "resource"
			available = resAvail
		}
		return EnforcerDecision{
			Action:   ActionDeny,
			Severity: SeverityMedium,
			Message:  fmt.Sprintf("Rate limit exceeded: %s bucket exhausted (%d available)", bucketType, available),
			PolicyID: "rate_limit",
		}, ErrRateLimitExceeded
	}

	// Increment the per-tool call rate bucket and wire the count into the decision context
	if e.config.RateWindowDuration > 0 {
		callRate, rateErr := e.store.IncrementRateBucket(userID, toolName, e.config.RateWindowDuration)
		if rateErr != nil {
			log.Printf("Warning: failed to increment rate bucket for user=%s tool=%s: %v", userID, toolName, rateErr)
		} else {
			decisionCtx.CallRate = callRate
		}
	}

	// Evaluate
	decision, err := e.Evaluate(ctx, decisionCtx)
	if err != nil {
		return EnforcerDecision{}, err
	}
	fmt.Printf("DEBUG: HandleToolCall decision - Action=%s PolicyID=%s\n", decision.Action, decision.PolicyID)
	return decision, nil
}

// RequestApproval creates a new approval request for HITL
func (e *Enforcer) RequestApproval(ctx context.Context, decisionCtx DecisionContext, policyID string, message string, queueType string) (string, error) {
	id := generateID()

	safetyJSON, _ := json.Marshal(decisionCtx.Safety)
	argsJSON, _ := json.Marshal(decisionCtx.Args)

	// Determine timeout based on queue type
	var timeout time.Duration
	if queueType == "user" {
		timeout = e.config.UserApprovalTimeout
		if timeout == 0 {
			timeout = 10 * time.Minute // Default
		}
	} else {
		timeout = e.config.AdminApprovalTimeout
		if timeout == 0 {
			timeout = e.config.ApprovalTimeout // Default to 24h
		}
	}

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
		QueueType:     queueType,
		Justification: decisionCtx.Justification,
		RequestedAt:   time.Now(),
		ExpiresAt:     time.Now().Add(timeout),
		PolicyID:      policyID,
		ViolationMsg:  message,
		RequestBody:   decisionCtx.RequestBody,
	}

	if err := e.store.CreateApprovalRequest(req); err != nil {
		return "", fmt.Errorf("failed to create approval request: %w", err)
	}

	return id, nil
}

// ListPendingApprovals returns all pending approval requests
func (e *Enforcer) ListPendingApprovals() ([]ApprovalRequestRow, error) {
	return e.store.ListPendingApprovals()
}

// ListUserPendingApprovals returns all pending user approval requests
func (e *Enforcer) ListUserPendingApprovals() ([]ApprovalRequestRow, error) {
	return e.store.ListUserPendingApprovals()
}

// ListAdminPendingApprovals returns all pending admin approval requests
func (e *Enforcer) ListAdminPendingApprovals() ([]ApprovalRequestRow, error) {
	return e.store.ListAdminPendingApprovals()
}

// CountUserPendingApprovals returns the count of pending user approval requests
func (e *Enforcer) CountUserPendingApprovals() (int, error) {
	return e.store.CountUserPendingApprovals()
}

// CountAdminPendingApprovals returns the count of pending admin approval requests
func (e *Enforcer) CountAdminPendingApprovals() (int, error) {
	return e.store.CountAdminPendingApprovals()
}

// GetApprovalRequest retrieves an approval request by ID
func (e *Enforcer) GetApprovalRequest(id string) (ApprovalRequestRow, error) {
	return e.store.GetApprovalRequest(id)
}

// GetResolver returns the metadata resolver
func (e *Enforcer) GetResolver() *MetadataResolver {
	return e.resolver
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

// ListAllApprovals returns all approval requests (not just pending)
func (e *Enforcer) ListAllApprovals() ([]ApprovalRequestRow, error) {
	return e.store.ListAllApprovals()
}

// DecorateDescription adds safety metadata to tool descriptions
func (e *Enforcer) DecorateDescription(description string, toolName string, backendID string) string {
	if !e.config.EnableDescriptionDecoration {
		return description
	}

	profile, err := e.resolver.Resolve(toolName, backendID)
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

// ApproveRequest approves an approval request and returns the original request body for replay
func (e *Enforcer) ApproveRequest(approvalID string, approverID string, comments string) (string, error) {
	if err := e.store.ApproveRequest(approvalID, approverID, comments); err != nil {
		return "", err
	}
	// Fetch the original request to replay
	req, err := e.store.GetApprovalRequest(approvalID)
	if err != nil {
		return "", err
	}
	return req.RequestBody, nil
}

// DenyRequest denies an approval request
func (e *Enforcer) DenyRequest(approvalID string, approverID string, reason string) error {
	return e.store.DenyRequest(approvalID, approverID, reason)
}

// SetExecutor sets the approval executor for executing approved requests
func (e *Enforcer) SetExecutor(executor ApprovalExecutor) {
	e.executor = executor
}

// ExecuteApprovedRequest executes an approved request and captures the result
func (e *Enforcer) ExecuteApprovedRequest(approvalID string, approverID string, comments string) (*ApprovalRequestRow, error) {
	log.Printf("ExecuteApprovedRequest: approvalID=%s approver=%s", approvalID, approverID)

	// Mark as approved
	if err := e.store.ApproveRequest(approvalID, approverID, comments); err != nil {
		return nil, fmt.Errorf("failed to approve request: %w", err)
	}

	// Get the request details
	req, err := e.store.GetApprovalRequest(approvalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get request: %w", err)
	}
	log.Printf("ExecuteApprovedRequest: got request, userID=%s backendID=%s status=%s", req.UserID, req.BackendID, req.Status)

	// If no executor, just return the approved request
	if e.executor == nil {
		log.Printf("ExecuteApprovedRequest: no executor configured, returning approved request")
		return &req, nil
	}

	// Mark as executing
	if err := e.store.MarkExecuting(approvalID); err != nil {
		return nil, fmt.Errorf("failed to mark executing: %w", err)
	}

	// Execute the request
	statusCode, responseBody, execErr := e.executor.ExecuteRequest(req.UserID, req.BackendID, req.RequestBody)
	log.Printf("ExecuteApprovedRequest: execution complete statusCode=%d bodyLen=%d err=%v", statusCode, len(responseBody), execErr)

	// Mark as completed or failed
	if execErr != nil {
		errMsg := execErr.Error()
		log.Printf("ExecuteApprovedRequest: marking as FAILED: %s", errMsg)
		if markErr := e.store.MarkFailed(approvalID, errMsg); markErr != nil {
			return nil, fmt.Errorf("failed to mark failed: %w", markErr)
		}
	} else {
		log.Printf("ExecuteApprovedRequest: marking as COMPLETED")
		if markErr := e.store.MarkCompleted(approvalID, statusCode, responseBody); markErr != nil {
			return nil, fmt.Errorf("failed to mark completed: %w", markErr)
		}
	}

	// Return updated request
	updatedReq, err := e.store.GetApprovalRequest(approvalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated request: %w", err)
	}

	return &updatedReq, nil
}

// AddPolicy adds a new policy to the enforcer (saves to DB and adds to engine)
func (e *Enforcer) AddPolicy(policy PolicyRow) error {
	if err := e.store.CreatePolicy(policy); err != nil {
		return err
	}
	celPolicy := policy.ToCELPolicy()
	return e.engine.AddPolicy(celPolicy)
}

// ListPolicies returns all policies from the store
func (e *Enforcer) ListPolicies() ([]PolicyRow, error) {
	if e.store == nil {
		return []PolicyRow{}, nil
	}
	return e.store.ListPolicies()
}

// GetPolicy returns a single policy by ID
func (e *Enforcer) GetPolicy(id string) (PolicyRow, error) {
	return e.store.GetPolicy(id)
}

// UpdatePolicy updates a policy in the store and reloads the engine
func (e *Enforcer) UpdatePolicy(policy PolicyRow) error {
	if err := e.store.UpdatePolicy(policy); err != nil {
		return err
	}
	return e.loadPolicies()
}

// DeletePolicy removes a policy from the store and engine
func (e *Enforcer) DeletePolicy(id string) error {
	if err := e.store.DeletePolicy(id); err != nil {
		return err
	}
	// Reload policies to update engine
	return e.loadPolicies()
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

// cleanupLoop periodically cleans up expired approvals and old completed requests
func (e *Enforcer) cleanupLoop() {
	ticker := time.NewTicker(e.config.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		// Mark expired pending requests
		if err := e.store.CleanupExpiredApprovals(); err != nil {
			log.Printf("Failed to cleanup expired approvals: %v", err)
		}
		// Delete old completed/denied requests
		if e.config.RetentionPeriod > 0 {
			if err := e.store.CleanupOldApprovals(e.config.RetentionPeriod); err != nil {
				log.Printf("Failed to cleanup old approvals: %v", err)
			}
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
	return uuid.New().String()
}
