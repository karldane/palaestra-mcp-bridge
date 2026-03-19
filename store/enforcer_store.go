package store

import (
	"database/sql"
	"encoding/json"
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

// CreatePolicy inserts a new policy
func (s *EnforcerStore) CreatePolicy(policy enforcer.PolicyRow) error {
	enabledInt := 0
	if policy.Enabled {
		enabledInt = 1
	}
	_, err := s.db.Exec(`INSERT INTO enforcer_policies (id, name, description, scope, expression, action, severity, message, enabled, priority, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.Name, policy.Description, policy.Scope, policy.Expression,
		policy.Action, policy.Severity, policy.Message, enabledInt, policy.Priority,
		policy.CreatedAt, policy.UpdatedAt)
	return err
}

// GetPolicy retrieves a policy by ID
func (s *EnforcerStore) GetPolicy(id string) (enforcer.PolicyRow, error) {
	var p enforcer.PolicyRow
	var enabledInt int
	err := s.db.QueryRow(`SELECT id, name, description, scope, expression, action, severity, message, enabled, priority, created_at, updated_at FROM enforcer_policies WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.Description, &p.Scope, &p.Expression, &p.Action,
		&p.Severity, &p.Message, &enabledInt, &p.Priority, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Enabled = enabledInt != 0
	return p, nil
}

// ListPolicies retrieves all enabled policies
func (s *EnforcerStore) ListPolicies() ([]enforcer.PolicyRow, error) {
	rows, err := s.db.Query(`SELECT id, name, description, scope, expression, action, severity, message, enabled, priority, created_at, updated_at FROM enforcer_policies WHERE enabled = 1 ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []enforcer.PolicyRow
	for rows.Next() {
		var p enforcer.PolicyRow
		var enabledInt int
		err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Scope, &p.Expression, &p.Action,
			&p.Severity, &p.Message, &enabledInt, &p.Priority, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}
		p.Enabled = enabledInt != 0
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

// DeletePolicy removes a policy
func (s *EnforcerStore) DeletePolicy(id string) error {
	_, err := s.db.Exec(`DELETE FROM enforcer_policies WHERE id = ?`, id)
	return err
}

// CreateApprovalRequest inserts a new approval request
func (s *EnforcerStore) CreateApprovalRequest(req enforcer.ApprovalRequestRow) error {
	_, err := s.db.Exec(`INSERT INTO enforcer_approvals (id, user_id, user_email, user_role, trust_level, tool_name, tool_args, backend_id, safety_profile, status, requested_at, expires_at, policy_id, violation_msg, request_body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.UserID, req.UserEmail, req.UserRole, req.TrustLevel, req.ToolName,
		req.ToolArgs, req.BackendID, req.SafetyProfile, req.Status, req.RequestedAt,
		req.ExpiresAt, req.PolicyID, req.ViolationMsg, req.RequestBody)
	return err
}

// GetApprovalRequest retrieves an approval request by ID
func (s *EnforcerStore) GetApprovalRequest(id string) (enforcer.ApprovalRequestRow, error) {
	var req enforcer.ApprovalRequestRow
	err := s.db.QueryRow(`SELECT id, user_id, user_email, user_role, trust_level, tool_name, tool_args, backend_id, safety_profile, status, requested_at, expires_at, approved_by, approved_at, denial_reason, comments, policy_id, violation_msg, COALESCE(request_body, '') FROM enforcer_approvals WHERE id = ?`, id).Scan(
		&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel, &req.ToolName,
		&req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status, &req.RequestedAt,
		&req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt, &req.DenialReason, &req.Comments,
		&req.PolicyID, &req.ViolationMsg, &req.RequestBody)
	return req, err
}

// ListPendingApprovals retrieves all pending approval requests
func (s *EnforcerStore) ListPendingApprovals() ([]enforcer.ApprovalRequestRow, error) {
	rows, err := s.db.Query(`SELECT id, user_id, user_email, user_role, trust_level, tool_name, tool_args, backend_id, safety_profile, status, requested_at, expires_at, approved_by, approved_at, denial_reason, comments, policy_id, violation_msg, COALESCE(request_body, '') FROM enforcer_approvals WHERE status = 'PENDING' ORDER BY requested_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []enforcer.ApprovalRequestRow
	for rows.Next() {
		var req enforcer.ApprovalRequestRow
		err := rows.Scan(&req.ID, &req.UserID, &req.UserEmail, &req.UserRole, &req.TrustLevel,
			&req.ToolName, &req.ToolArgs, &req.BackendID, &req.SafetyProfile, &req.Status,
			&req.RequestedAt, &req.ExpiresAt, &req.ApprovedBy, &req.ApprovedAt,
			&req.DenialReason, &req.Comments, &req.PolicyID, &req.ViolationMsg, &req.RequestBody)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
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
func (s *EnforcerStore) LogAuditEvent(requestID string, userID string, toolName string, action string, policyID string, message string, context map[string]interface{}) error {
	contextJSON, _ := json.Marshal(context)
	_, err := s.db.Exec(`INSERT INTO enforcer_audit_log (id, request_id, user_id, tool_name, action, policy_id, message, context, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generateID(), requestID, userID, toolName, action, policyID, message, string(contextJSON), time.Now())
	return err
}
