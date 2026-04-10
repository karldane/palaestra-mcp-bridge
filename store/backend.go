package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Backend represents an MCP backend server configuration.
type Backend struct {
	ID                  string
	Command             string
	PoolSize            int
	ToolPrefix          string
	Env                 string // JSON object - systemwide env vars (higher priority than user tokens)
	EnvMappings         string // JSON object - maps user token keys to backend-specific keys
	ToolHints           string // Plain text - usage hints for the README tool (per-backend guidance)
	BackendInstructions string // Plain text - instructions provided by backend during initialize
	Enabled             bool
	IsSystem            bool // true for mcpbridge - system backend that can't be deleted by non-admins
	MinPoolSize         int  // Minimum warm processes to maintain
	MaxPoolSize         int  // Maximum warm processes allowed (0 = unlimited)
	SelfReporting       bool // true if the backend supports self-reporting EnforcerProfile via Meta
	NoKeysRequired      bool // true if the backend doesn't require user-level tokens (e.g., qdrant-mcp)
	SkipJustification   bool // true if tools from this backend do not require a justification field
}

// CreateBackend inserts a new backend into the database.
func (s *Store) CreateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	isSystem := 0
	if b.IsSystem {
		isSystem = 1
	}
	selfReporting := 0
	if b.SelfReporting {
		selfReporting = 1
	}
	noKeysRequired := 0
	if b.NoKeysRequired {
		noKeysRequired = 1
	}
	skipJustification := 0
	if b.SkipJustification {
		skipJustification = 1
	}
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, tool_hints, backend_instructions, enabled, is_system, self_reporting, no_keys_required, skip_justification)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, b.ToolHints, b.BackendInstructions, enabled, isSystem, selfReporting, noKeysRequired, skipJustification,
	)
	return err
}

// GetBackend retrieves a backend by ID.
func (s *Store) GetBackend(id string) (*Backend, error) {
	b := &Backend{}
	var enabled, isSystem, selfReporting, noKeysRequired, skipJustification int
	err := s.db.QueryRow(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, tool_hints, backend_instructions, enabled, is_system, self_reporting, no_keys_required, skip_justification FROM backends WHERE id = ?`, id,
	).Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &b.ToolHints, &b.BackendInstructions, &enabled, &isSystem, &selfReporting, &noKeysRequired, &skipJustification)
	if err != nil {
		return nil, err
	}
	b.Enabled = enabled != 0
	b.IsSystem = isSystem != 0
	b.SelfReporting = selfReporting != 0
	b.NoKeysRequired = noKeysRequired != 0
	b.SkipJustification = skipJustification != 0
	return b, nil
}

// ListBackends returns all backends ordered by ID.
func (s *Store) ListBackends() ([]*Backend, error) {
	rows, err := s.db.Query(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, tool_hints, backend_instructions, enabled, is_system, self_reporting, no_keys_required, skip_justification FROM backends ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []*Backend
	for rows.Next() {
		b := &Backend{}
		var enabled, isSystem, selfReporting, noKeysRequired, skipJustification int
		if err := rows.Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &b.ToolHints, &b.BackendInstructions, &enabled, &isSystem, &selfReporting, &noKeysRequired, &skipJustification); err != nil {
			return nil, err
		}
		b.Enabled = enabled != 0
		b.IsSystem = isSystem != 0
		b.SelfReporting = selfReporting != 0
		b.NoKeysRequired = noKeysRequired != 0
		b.SkipJustification = skipJustification != 0
		if b.MinPoolSize == 0 {
			b.MinPoolSize = 1
		}
		backends = append(backends, b)
	}
	return backends, rows.Err()
}

// UpdateBackend updates an existing backend.
func (s *Store) UpdateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	isSystem := 0
	if b.IsSystem {
		isSystem = 1
	}
	selfReporting := 0
	if b.SelfReporting {
		selfReporting = 1
	}
	noKeysRequired := 0
	if b.NoKeysRequired {
		noKeysRequired = 1
	}
	skipJustification := 0
	if b.SkipJustification {
		skipJustification = 1
	}
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`UPDATE backends SET command=?, pool_size=?, min_pool_size=?, max_pool_size=?, tool_prefix=?, env=?, env_mappings=?, tool_hints=?, backend_instructions=?, enabled=?, is_system=?, self_reporting=?, no_keys_required=?, skip_justification=? WHERE id=?`,
		b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, b.ToolHints, b.BackendInstructions, enabled, isSystem, selfReporting, noKeysRequired, skipJustification, b.ID,
	)
	return err
}

// DeleteBackend removes a backend by ID.
func (s *Store) DeleteBackend(id string) error {
	_, err := s.db.Exec(`DELETE FROM backends WHERE id = ?`, id)
	return err
}

// ---------- Backend Capabilities Cache ----------

// BackendCapabilities stores cached tool information for a backend.
type BackendCapabilities struct {
	BackendID string
	Tools     []map[string]interface{}
	ToolCount int
	UpdatedAt time.Time
}

// SetBackendCapabilities caches tool information for a backend.
func (s *Store) SetBackendCapabilities(backendID string, tools []map[string]interface{}) error {
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO backend_capabilities (backend_id, tools, tool_count, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET tools = excluded.tools, tool_count = excluded.tool_count, updated_at = excluded.updated_at`,
		backendID, toolsJSON, len(tools),
	)
	return err
}

// GetBackendCapabilities retrieves cached capabilities for a specific backend.
func (s *Store) GetBackendCapabilities(backendID string) (*BackendCapabilities, error) {
	var caps BackendCapabilities
	var toolsJSON []byte
	var updatedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT backend_id, tools, tool_count, updated_at FROM backend_capabilities WHERE backend_id = ?`,
		backendID,
	).Scan(&caps.BackendID, &toolsJSON, &caps.ToolCount, &updatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(toolsJSON, &caps.Tools); err != nil {
		return nil, err
	}
	if updatedAt.Valid {
		caps.UpdatedAt = updatedAt.Time
	}
	return &caps, nil
}

// GetAllBackendCapabilities retrieves cached capabilities for all backends.
func (s *Store) GetAllBackendCapabilities() (map[string]*BackendCapabilities, error) {
	rows, err := s.db.Query(`SELECT backend_id, tools, tool_count, updated_at FROM backend_capabilities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*BackendCapabilities)
	for rows.Next() {
		var caps BackendCapabilities
		var toolsJSON []byte
		var updatedAt sql.NullTime
		if err := rows.Scan(&caps.BackendID, &toolsJSON, &caps.ToolCount, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(toolsJSON, &caps.Tools); err != nil {
			return nil, err
		}
		if updatedAt.Valid {
			caps.UpdatedAt = updatedAt.Time
		}
		result[caps.BackendID] = &caps
	}
	return result, rows.Err()
}

// DeleteBackendCapabilities removes cached capabilities for a backend.
func (s *Store) DeleteBackendCapabilities(backendID string) error {
	_, err := s.db.Exec(`DELETE FROM backend_capabilities WHERE backend_id = ?`, backendID)
	return err
}

// MigrateDefaultBackend migrates the "default" backend to "mcpbridge" and marks it as system.
// This is called on startup to rename the legacy default backend.
func (s *Store) MigrateDefaultBackend() error {
	// Check if "default" exists
	var exists int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM backends WHERE id = 'default'`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		return nil // No default backend to migrate
	}

	// Disable foreign key checks temporarily
	_, err = s.db.Exec(`PRAGMA foreign_keys = OFF`)
	if err != nil {
		return err
	}
	defer s.db.Exec(`PRAGMA foreign_keys = ON`)

	// First update user_tokens to reference the new backend ID
	_, err = s.db.Exec(
		`UPDATE user_tokens SET backend_id = 'mcpbridge' WHERE backend_id = 'default'`,
	)
	if err != nil {
		return err
	}

	// Then migrate default -> mcpbridge and mark as system
	_, err = s.db.Exec(
		`UPDATE backends SET id = 'mcpbridge', is_system = 1 WHERE id = 'default'`,
	)
	return err
}

// ---------- Settings ----------

// GetSetting retrieves a setting value by key.
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetSetting sets a setting value by key.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// ---------- Enforcer Tool Profiles (self-reported safety metadata) ----------

// ToolProfileRow represents a self-reported safety profile for a tool.
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

// UpsertToolProfile inserts or updates a tool's self-reported profile.
func (s *Store) UpsertToolProfile(profile ToolProfileRow) error {
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

// GetToolProfile retrieves a tool's profile by backend and tool name.
func (s *Store) GetToolProfile(backendID, toolName string) (*ToolProfileRow, error) {
	var p ToolProfileRow
	var hitl, pii, idemp int
	err := s.db.QueryRow(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles WHERE backend_id = ? AND tool_name = ?`,
		backendID, toolName,
	).Scan(&p.ID, &p.BackendID, &p.ToolName, &p.RiskLevel, &p.ImpactScope, &p.ResourceCost, &hitl, &pii, &idemp, &p.RawProfile, &p.ScannedAt)
	if err != nil {
		return nil, err
	}
	p.RequiresHITL = hitl != 0
	p.PIIExposure = pii != 0
	p.Idempotent = idemp != 0
	return &p, nil
}

// ListToolProfilesByBackend returns all tool profiles for a backend.
func (s *Store) ListToolProfilesByBackend(backendID string) ([]ToolProfileRow, error) {
	rows, err := s.db.Query(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles WHERE backend_id = ? ORDER BY tool_name`,
		backendID,
	)
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

// ListAllToolProfiles returns all stored tool profiles.
func (s *Store) ListAllToolProfiles() ([]ToolProfileRow, error) {
	rows, err := s.db.Query(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles ORDER BY backend_id, tool_name`,
	)
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

// DeleteToolProfilesByBackend removes all tool profiles for a backend.
func (s *Store) DeleteToolProfilesByBackend(backendID string) error {
	_, err := s.db.Exec(`DELETE FROM enforcer_tool_profiles WHERE backend_id = ?`, backendID)
	return err
}

// BackendProfileSummary contains summary info for a backend's tool profiles.
type BackendProfileSummary struct {
	BackendID       string
	ToolCount       int
	HighRiskCount   int
	MediumRiskCount int
	LowRiskCount    int
}

// ListBackendProfileSummaries returns tool profile summaries grouped by backend.
func (s *Store) ListBackendProfileSummaries() ([]BackendProfileSummary, error) {
	rows, err := s.db.Query(`
		SELECT backend_id, 
			   COUNT(*) as tool_count,
			   SUM(CASE WHEN risk_level = 'high' OR risk_level = 'critical' THEN 1 ELSE 0 END) as high_risk_count,
			   SUM(CASE WHEN risk_level = 'medium' THEN 1 ELSE 0 END) as medium_risk_count,
			   SUM(CASE WHEN risk_level = 'low' THEN 1 ELSE 0 END) as low_risk_count
		FROM enforcer_tool_profiles 
		GROUP BY backend_id 
		ORDER BY backend_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []BackendProfileSummary
	for rows.Next() {
		var s BackendProfileSummary
		if err := rows.Scan(&s.BackendID, &s.ToolCount, &s.HighRiskCount, &s.MediumRiskCount, &s.LowRiskCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// GetToolProfilesByBackend returns tool profiles for a specific backend.
func (s *Store) GetToolProfilesByBackend(backendID string) ([]ToolProfileRow, error) {
	rows, err := s.db.Query(`
		SELECT id, backend_id, tool_name, risk_level, impact_scope, resource_cost, requires_hitl, pii_exposure, idempotent, raw_profile, scanned_at
		FROM enforcer_tool_profiles WHERE backend_id = ? ORDER BY tool_name`,
		backendID,
	)
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
