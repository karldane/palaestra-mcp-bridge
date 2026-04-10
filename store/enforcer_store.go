package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
)

// EnforcerStore provides database operations for the enforcer subsystem
type EnforcerStore struct {
	db *sql.DB
}

// Ensure EnforcerStore implements enforcer.EnforcerStore
var _ enforcer.EnforcerStore = (*EnforcerStore)(nil)

// NewEnforcerStore creates a new enforcer store
func NewEnforcerStore(db *sql.DB) enforcer.EnforcerStore {
	return &EnforcerStore{db: db}
}

const approvalColumns = `id, user_id, user_email, user_role, trust_level, tool_name, tool_args, backend_id, safety_profile, status, queue_type, justification, requested_at, expires_at, approved_by, approved_at, denial_reason, comments, policy_id, violation_msg, request_body, response_status, response_body, executed_at, error_msg`

// CreatePolicy inserts a new policy
func (s *EnforcerStore) CreatePolicy(policy enforcer.PolicyRow) error {
	enabledInt := 0
	if policy.Enabled {
		enabledInt = 1
	}
	lockedInt := 0
	if policy.Locked {
		lockedInt = 1
	}
	_, err := s.db.Exec(`INSERT INTO enforcer_policies (id, name, description, scope, expression, action, severity, message, enabled, priority, locked, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.Name, policy.Description, policy.Scope, policy.Expression,
		policy.Action, policy.Severity, policy.Message, enabledInt, policy.Priority, lockedInt,
		policy.CreatedAt, policy.UpdatedAt)
	return err
}

// GetPolicy retrieves a policy by ID
func (s *EnforcerStore) GetPolicy(id string) (enforcer.PolicyRow, error) {
	var p enforcer.PolicyRow
	var enabledInt, lockedInt int
	err := s.db.QueryRow(`SELECT id, name, description, scope, expression, action, severity, message, enabled, priority, locked, created_at, updated_at FROM enforcer_policies WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.Description, &p.Scope, &p.Expression, &p.Action,
		&p.Severity, &p.Message, &enabledInt, &p.Priority, &lockedInt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Enabled = enabledInt != 0
	p.Locked = lockedInt != 0
	return p, nil
}

// ListPolicies retrieves all enabled policies
func (s *EnforcerStore) ListPolicies() ([]enforcer.PolicyRow, error) {
	rows, err := s.db.Query(`SELECT id, name, description, scope, expression, action, severity, message, enabled, priority, locked, created_at, updated_at FROM enforcer_policies WHERE enabled = 1 ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []enforcer.PolicyRow
	for rows.Next() {
		var p enforcer.PolicyRow
		var enabledInt, lockedInt int
		err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Scope, &p.Expression, &p.Action,
			&p.Severity, &p.Message, &enabledInt, &p.Priority, &lockedInt, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}
		p.Enabled = enabledInt != 0
		p.Locked = lockedInt != 0
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

// DeletePolicy removes a policy
func (s *EnforcerStore) DeletePolicy(id string) error {
	_, err := s.db.Exec(`DELETE FROM enforcer_policies WHERE id = ?`, id)
	return err
}

// UpdatePolicy updates an existing policy
func (s *EnforcerStore) UpdatePolicy(policy enforcer.PolicyRow) error {
	enabledInt := 0
	if policy.Enabled {
		enabledInt = 1
	}
	lockedInt := 0
	if policy.Locked {
		lockedInt = 1
	}
	_, err := s.db.Exec(`UPDATE enforcer_policies SET name = ?, description = ?, scope = ?, expression = ?, action = ?, severity = ?, message = ?, enabled = ?, priority = ?, locked = ?, updated_at = ? WHERE id = ?`,
		policy.Name, policy.Description, policy.Scope, policy.Expression, policy.Action,
		policy.Severity, policy.Message, enabledInt, policy.Priority, lockedInt, policy.UpdatedAt, policy.ID)
	return err
}

// CreateApprovalRequest inserts a new approval request
func (s *EnforcerStore) CreateApprovalRequest(req enforcer.ApprovalRequestRow) error {
	_, err := s.db.Exec(`INSERT INTO enforcer_approvals (id, user_id, user_email, user_role, trust_level, tool_name, tool_args, backend_id, safety_profile, status, queue_type, justification, requested_at, expires_at, policy_id, violation_msg, request_body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.UserID, req.UserEmail, req.UserRole, req.TrustLevel, req.ToolName,
		req.ToolArgs, req.BackendID, req.SafetyProfile, req.Status, req.QueueType,
		req.Justification, req.RequestedAt, req.ExpiresAt, req.PolicyID, req.ViolationMsg, req.RequestBody)
	return err
}

// GetApprovalRequest retrieves an approval request by ID
func (s *EnforcerStore) GetApprovalRequest(id string) (enforcer.ApprovalRequestRow, error) {
	var req enforcer.ApprovalRequestRow
	err := s.db.QueryRow(`SELECT `+approvalColumns+` FROM enforcer_approvals WHERE id = ?`, id).Scan(
		&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel, &req.ToolName,
		&req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status, &req.QueueType,
		&req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
		&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
		&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
	return req, err
}

// ListPendingApprovals retrieves all pending approval requests
func (s *EnforcerStore) ListPendingApprovals() ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT ` + approvalColumns + ` FROM enforcer_approvals WHERE status = 'PENDING' ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.QueueType, &req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
			&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// ListAllApprovals retrieves all approval requests (not just pending)
func (s *EnforcerStore) ListAllApprovals() ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT ` + approvalColumns + ` FROM enforcer_approvals ORDER BY requested_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.QueueType, &req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
			&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// MarkExecuting marks an approval request as executing
func (s *EnforcerStore) MarkExecuting(id string) error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'EXECUTING' WHERE id = ?`, id)
	return err
}

// MarkCompleted marks an approval request as completed with response data
func (s *EnforcerStore) MarkCompleted(id string, responseStatus int, responseBody string) error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'COMPLETED', response_status = ?, response_body = ?, executed_at = ? WHERE id = ?`,
		responseStatus, responseBody, time.Now(), id)
	return err
}

// MarkFailed marks an approval request as failed with error message
func (s *EnforcerStore) MarkFailed(id string, errorMsg string) error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'FAILED', error_msg = ?, executed_at = ? WHERE id = ?`,
		errorMsg, time.Now(), id)
	return err
}

// ApproveRequest marks an approval request as approved
func (s *EnforcerStore) ApproveRequest(id string, approverID string, comments string) error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'APPROVED', approved_by = ?, approved_at = ?, comments = ? WHERE id = ?`,
		approverID, time.Now(), comments, id)
	return err
}

// DenyRequest marks an approval request as denied
func (s *EnforcerStore) DenyRequest(id string, approverID string, reason string) error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'DENIED', approved_by = ?, approved_at = ?, denial_reason = ? WHERE id = ?`,
		approverID, time.Now(), reason, id)
	return err
}

// CleanupExpiredApprovals removes or marks expired approval requests
func (s *EnforcerStore) CleanupExpiredApprovals() error {
	_, err := s.db.Exec(`UPDATE enforcer_approvals SET status = 'EXPIRED' WHERE status = 'PENDING' AND expires_at < ?`, time.Now())
	return err
}

// CleanupOldApprovals deletes completed/denied/expired requests older than the specified duration
func (s *EnforcerStore) CleanupOldApprovals(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	_, err := s.db.Exec(`DELETE FROM enforcer_approvals WHERE status IN ('COMPLETED', 'DENIED', 'EXPIRED', 'FAILED') AND requested_at < ?`, cutoff)
	return err
}

// IsApprovalPending checks if an approval request is still pending and not expired
func (s *EnforcerStore) IsApprovalPending(id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM enforcer_approvals WHERE id = ? AND status = 'PENDING' AND expires_at > ?`, id, time.Now()).Scan(&count)
	return count > 0, err
}

// KillSwitchRow represents a kill switch in the database
type KillSwitchRow struct {
	ID        string
	Name      string
	Scope     string
	Enabled   bool
	EnabledAt sql.NullTime
	EnabledBy sql.NullString
	Reason    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetKillSwitch retrieves a kill switch by scope
func (s *EnforcerStore) GetKillSwitch(scope string) (KillSwitchRow, error) {
	var ks KillSwitchRow
	var enabledInt int
	err := s.db.QueryRow(`SELECT id, name, scope, enabled, enabled_at, enabled_by, reason, created_at, updated_at FROM enforcer_kill_switches WHERE scope = ?`, scope).Scan(
		&ks.ID, &ks.Name, &ks.Scope, &enabledInt, &ks.EnabledAt, &ks.EnabledBy, &ks.Reason, &ks.CreatedAt, &ks.UpdatedAt)
	if err != nil {
		return ks, err
	}
	ks.Enabled = enabledInt != 0
	return ks, nil
}

// IsKillSwitchActive checks if a kill switch is enabled for a scope
func (s *EnforcerStore) IsKillSwitchActive(scope string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM enforcer_kill_switches WHERE scope = ? AND enabled = 1`, scope).Scan(&count)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	// Check global kill switch
	err = s.db.QueryRow(`SELECT COUNT(*) FROM enforcer_kill_switches WHERE scope = 'global' AND enabled = 1`).Scan(&count)
	return count > 0, err
}

// EnableKillSwitch activates a kill switch
func (s *EnforcerStore) EnableKillSwitch(scope string, userID string, reason string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO enforcer_kill_switches (id, name, scope, enabled, enabled_at, enabled_by, reason, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		generateID(), scope+"_kill", scope, time.Now(), userID, reason, time.Now(), time.Now())
	return err
}

// DisableKillSwitch deactivates a kill switch
func (s *EnforcerStore) DisableKillSwitch(scope string) error {
	_, err := s.db.Exec(`UPDATE enforcer_kill_switches SET enabled = 0, updated_at = ? WHERE scope = ?`, time.Now(), scope)
	return err
}

// ListKillSwitches retrieves all kill switches
func (s *EnforcerStore) ListKillSwitches() ([]KillSwitchRow, error) {
	rows, err := s.db.Query(`SELECT id, name, scope, enabled, enabled_at, enabled_by, reason, created_at, updated_at FROM enforcer_kill_switches ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var switches []KillSwitchRow
	for rows.Next() {
		var ks KillSwitchRow
		var enabledInt int
		err := rows.Scan(&ks.ID, &ks.Name, &ks.Scope, &enabledInt, &ks.EnabledAt, &ks.EnabledBy, &ks.Reason, &ks.CreatedAt, &ks.UpdatedAt)
		if err != nil {
			return nil, err
		}
		ks.Enabled = enabledInt != 0
		switches = append(switches, ks)
	}
	return switches, rows.Err()
}

// LogAuditEvent records a policy decision in the audit log
func (s *EnforcerStore) LogAuditEvent(requestID string, userID string, toolName string, action string, policyID string, message string, context map[string]interface{}, justification string, arguments map[string]interface{}) error {
	contextJSON, _ := json.Marshal(context)
	argsJSON, err := json.Marshal(arguments)
	if err != nil || arguments == nil {
		argsJSON = []byte("{}")
	}
	_, err = s.db.Exec(`INSERT INTO enforcer_audit_log (id, request_id, user_id, tool_name, action, policy_id, message, context, justification, arguments, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generateID(), requestID, userID, toolName, action, policyID, message, string(contextJSON), justification, string(argsJSON), time.Now())
	return err
}

// LogAuditRejection records a pre-policy justification rejection in the audit log.
func (s *EnforcerStore) LogAuditRejection(requestID string, userID string, toolName string, justification string, rejectionReason string) error {
	message := fmt.Sprintf("Tool call rejected before policy evaluation: %s", rejectionReason)
	_, err := s.db.Exec(`INSERT INTO enforcer_audit_log (id, request_id, user_id, tool_name, action, policy_id, message, context, justification, rejection_reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generateID(), requestID, userID, toolName, "DENY", rejectionReason, message, "{}", justification, rejectionReason, time.Now())
	return err
}

// GetToolProfile retrieves a tool's self-reported safety profile.
func (s *EnforcerStore) GetToolProfile(backendID, toolName string) (enforcer.ToolProfileRow, error) {
	var p enforcer.ToolProfileRow
	var hitl, pii, idemp int
	err := s.db.QueryRow(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles WHERE backend_id = ? AND tool_name = ?`,
		backendID, toolName,
	).Scan(&p.ID, &p.BackendID, &p.ToolName, &p.RiskLevel, &p.ImpactScope, &p.ResourceCost, &hitl, &pii, &idemp, &p.RawProfile, &p.ScannedAt)
	if err != nil {
		return enforcer.ToolProfileRow{}, err
	}
	p.RequiresHITL = hitl != 0
	p.PIIExposure = pii != 0
	p.Idempotent = idemp != 0
	return p, nil
}

// UpsertOverride inserts or updates a tool override.
func (s *EnforcerStore) UpsertOverride(override enforcer.EnforcerOverrideRow) error {
	hitl := 0
	if override.RequiresHITL {
		hitl = 1
	}
	pii := 0
	if override.PIIExposure {
		pii = 1
	}
	var userID sql.NullString
	if override.UserID != "" {
		userID = sql.NullString{String: override.UserID, Valid: true}
	}
	_, err := s.db.Exec(`
		INSERT INTO enforcer_overrides (id, tool_name, backend_id, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tool_name, backend_id) DO UPDATE SET
			risk_level=excluded.risk_level,
			impact_scope=excluded.impact_scope,
			resource_cost=excluded.resource_cost,
			requires_hitl=excluded.requires_hitl,
			pii_exposure=excluded.pii_exposure,
			user_id=excluded.user_id,
			updated_at=excluded.updated_at`,
		override.ID, override.ToolName, override.BackendID, override.RiskLevel, override.ImpactScope,
		override.ResourceCost, hitl, pii, userID, override.CreatedAt, override.UpdatedAt,
	)
	return err
}

// GetOverride retrieves an override by tool name and backend.
func (s *EnforcerStore) GetOverride(toolName, backendID string) (enforcer.EnforcerOverrideRow, error) {
	var o enforcer.EnforcerOverrideRow
	var hitl, pii int
	var userID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, tool_name, backend_id, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, user_id, created_at, updated_at
		FROM enforcer_overrides WHERE tool_name = ? AND backend_id = ?`,
		toolName, backendID,
	).Scan(&o.ID, &o.ToolName, &o.BackendID, &o.RiskLevel, &o.ImpactScope, &o.ResourceCost, &hitl, &pii, &userID, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return enforcer.EnforcerOverrideRow{}, err
	}
	o.RequiresHITL = hitl != 0
	o.PIIExposure = pii != 0
	if userID.Valid {
		o.UserID = userID.String
	}
	return o, nil
}

// ListOverrides returns all overrides.
func (s *EnforcerStore) ListOverrides() ([]enforcer.EnforcerOverrideRow, error) {
	rows, err := s.db.Query(`
		SELECT id, tool_name, backend_id, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, user_id, created_at, updated_at
		FROM enforcer_overrides ORDER BY tool_name, backend_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var overrides []enforcer.EnforcerOverrideRow
	for rows.Next() {
		var o enforcer.EnforcerOverrideRow
		var hitl, pii int
		var userID sql.NullString
		if err := rows.Scan(&o.ID, &o.ToolName, &o.BackendID, &o.RiskLevel, &o.ImpactScope, &o.ResourceCost, &hitl, &pii, &userID, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.RequiresHITL = hitl != 0
		o.PIIExposure = pii != 0
		if userID.Valid {
			o.UserID = userID.String
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// ListUserOverrides returns overrides scoped to a specific user (personal overrides).
func (s *EnforcerStore) ListUserOverrides(userID string) ([]enforcer.EnforcerOverrideRow, error) {
	rows, err := s.db.Query(`
		SELECT id, tool_name, backend_id, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, user_id, created_at, updated_at
		FROM enforcer_overrides WHERE user_id = ? ORDER BY tool_name, backend_id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var overrides []enforcer.EnforcerOverrideRow
	for rows.Next() {
		var o enforcer.EnforcerOverrideRow
		var hitl, pii int
		var uid sql.NullString
		if err := rows.Scan(&o.ID, &o.ToolName, &o.BackendID, &o.RiskLevel, &o.ImpactScope, &o.ResourceCost, &hitl, &pii, &uid, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.RequiresHITL = hitl != 0
		o.PIIExposure = pii != 0
		if uid.Valid {
			o.UserID = uid.String
		}
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}

// DeleteOverride removes an override.
func (s *EnforcerStore) DeleteOverride(toolName, backendID string) error {
	_, err := s.db.Exec(`DELETE FROM enforcer_overrides WHERE tool_name = ? AND backend_id = ?`, toolName, backendID)
	return err
}

// ListAllToolProfiles returns all stored tool profiles.
func (s *EnforcerStore) ListAllToolProfiles() ([]ToolProfileRow, error) {
	rows, err := s.db.Query(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles ORDER BY backend_id, tool_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []ToolProfileRow
	for rows.Next() {
		var p ToolProfileRow
		var hitl, pii, idemp int
		if err := rows.Scan(&p.ID, &p.BackendID, &p.ToolName, &p.RiskLevel, &p.ImpactScope, &p.ResourceCost, &hitl, &pii, &idemp, &p.RawProfile, &p.ScannedAt); err != nil {
			return nil, err
		}
		p.RequiresHITL = hitl != 0
		p.PIIExposure = pii != 0
		p.Idempotent = idemp != 0
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// UpsertToolProfile inserts or updates a tool's self-reported profile.
func (s *EnforcerStore) UpsertToolProfile(profile enforcer.ToolProfileRow) error {
	hitl := 0
	if profile.RequiresHITL {
		hitl = 1
	}
	pii := 0
	if profile.PIIExposure {
		pii = 1
	}
	idemp := 0
	if profile.Idempotent {
		idemp = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO enforcer_tool_profiles (id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(backend_id, tool_name) DO UPDATE SET
			risk_level=excluded.risk_level,
			impact_scope=excluded.impact_scope,
			resource_cost=excluded.resource_cost,
			requires_hitl=excluded.requires_hitl,
			pii_exposure=excluded.pii_exposure,
			idempotent=excluded.idempotent,
			raw_profile=excluded.raw_profile,
			scanned_at=excluded.scanned_at`,
		profile.ID, profile.BackendID, profile.ToolName, profile.RiskLevel, profile.ImpactScope,
		profile.ResourceCost, hitl, pii, idemp, profile.RawProfile, profile.ScannedAt,
	)
	return err
}

// ListUserPendingApprovals retrieves all pending user approval requests
func (s *EnforcerStore) ListUserPendingApprovals() ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT ` + approvalColumns + ` FROM enforcer_approvals WHERE status = 'PENDING' AND queue_type = 'user' ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.QueueType, &req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
			&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// ListUserAllApprovals retrieves all approval requests for a specific user
// across all statuses (PENDING, COMPLETED, DENIED, FAILED, EXPIRED, EXECUTING).
// Used for the "All Requests" tab in the user queue UI.
func (s *EnforcerStore) ListUserAllApprovals(userID string) ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT `+approvalColumns+` FROM enforcer_approvals WHERE user_id = ? AND queue_type = 'user' ORDER BY requested_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.QueueType, &req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
			&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// ListAdminPendingApprovals retrieves all pending admin approval requests
func (s *EnforcerStore) ListAdminPendingApprovals() ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT ` + approvalColumns + ` FROM enforcer_approvals WHERE status = 'PENDING' AND queue_type = 'admin' ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.QueueType, &req.Justification, &req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody,
			&req.ResponseStatus, &req.ResponseBody, &req.ExecutedAt, &req.ErrorMsg)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// CountUserPendingApprovals returns the count of pending user approval requests
func (s *EnforcerStore) CountUserPendingApprovals() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM enforcer_approvals WHERE status = 'PENDING' AND queue_type = 'user'`).Scan(&count)
	return count, err
}

// CountAdminPendingApprovals returns the count of pending admin approval requests
func (s *EnforcerStore) CountAdminPendingApprovals() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM enforcer_approvals WHERE status = 'PENDING' AND queue_type = 'admin'`).Scan(&count)
	return count, err
}

// IncrementRateBucket increments the call count for (userID, toolName) in the current window.
// Returns the new count. Creates or updates the window bucket atomically.
func (s *EnforcerStore) IncrementRateBucket(userID, toolName string, windowDuration time.Duration) (int, error) {
	windowStart := time.Now().Truncate(windowDuration)
	id := userID + "|" + toolName + "|" + windowStart.Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO enforcer_rate_buckets (id, user_id, tool_name, window_start, count)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(user_id, tool_name, window_start) DO UPDATE SET count = count + 1`,
		id, userID, toolName, windowStart)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.db.QueryRow(`SELECT count FROM enforcer_rate_buckets WHERE user_id = ? AND tool_name = ? AND window_start = ?`,
		userID, toolName, windowStart).Scan(&count)
	return count, err
}

// GetCallRate returns the current call count for (userID, toolName) in the active window.
// Returns 0 if no bucket exists for the current window.
func (s *EnforcerStore) GetCallRate(userID, toolName string, windowDuration time.Duration) (int, error) {
	windowStart := time.Now().Truncate(windowDuration)
	var count int
	err := s.db.QueryRow(`SELECT count FROM enforcer_rate_buckets WHERE user_id = ? AND tool_name = ? AND window_start = ?`,
		userID, toolName, windowStart).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// CleanupExpiredRateBuckets deletes buckets whose window has expired.
func (s *EnforcerStore) CleanupExpiredRateBuckets(windowDuration time.Duration) error {
	cutoff := time.Now().Add(-windowDuration)
	_, err := s.db.Exec(`DELETE FROM enforcer_rate_buckets WHERE window_start < ?`, cutoff)
	return err
}
