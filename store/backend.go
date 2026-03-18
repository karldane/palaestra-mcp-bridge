package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Backend represents an MCP backend server configuration.
type Backend struct {
	ID          string
	Command     string
	PoolSize    int
	ToolPrefix  string
	Env         string // JSON object - systemwide env vars (higher priority than user tokens)
	EnvMappings string // JSON object - maps user token keys to backend-specific keys
	Enabled     bool
	IsSystem    bool // true for mcpbridge - system backend that can't be deleted by non-admins
	MinPoolSize int  // Minimum warm processes to maintain
	MaxPoolSize int  // Maximum warm processes allowed (0 = unlimited)
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
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, enabled, isSystem,
	)
	return err
}

// GetBackend retrieves a backend by ID.
func (s *Store) GetBackend(id string) (*Backend, error) {
	b := &Backend{}
	var enabled, isSystem int
	err := s.db.QueryRow(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system FROM backends WHERE id = ?`, id,
	).Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &enabled, &isSystem)
	if err != nil {
		return nil, err
	}
	b.Enabled = enabled != 0
	b.IsSystem = isSystem != 0
	return b, nil
}

// ListBackends returns all backends ordered by ID.
func (s *Store) ListBackends() ([]*Backend, error) {
	rows, err := s.db.Query(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system FROM backends ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []*Backend
	for rows.Next() {
		b := &Backend{}
		var enabled, isSystem int
		if err := rows.Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &enabled, &isSystem); err != nil {
			return nil, err
		}
		b.Enabled = enabled != 0
		b.IsSystem = isSystem != 0
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
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`UPDATE backends SET command=?, pool_size=?, min_pool_size=?, max_pool_size=?, tool_prefix=?, env=?, env_mappings=?, enabled=?, is_system=? WHERE id=?`,
		b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, enabled, isSystem, b.ID,
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
