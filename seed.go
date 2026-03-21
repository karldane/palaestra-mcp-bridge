package main

import (
	"encoding/json"

	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// seedDefaultUser creates a test user (admin@localhost / admin) if no users
// exist in the database. This is for local development and testing only.
func seedDefaultUser(st *store.Store) {
	// Check if the user already exists by trying to look up by email.
	if existing, err := st.GetUserByEmail("admin@localhost"); err == nil {
		if existing.Role != "admin" {
			existing.Role = "admin"
			st.UpdateUser(existing)
			shared.Info("seed: upgraded admin@localhost to role=admin")
		} else {
			shared.Info("seed: user admin@localhost already exists, skipping")
		}
		return
	}

	user := &store.User{
		Name:     "Admin",
		Email:    "admin@localhost",
		Password: "admin",
		Role:     "admin",
	}
	if err := st.CreateUser(user); err != nil {
		shared.Errorf("seed: failed to create user: %v", err)
		return
	}
	shared.Infof("seed: created user admin@localhost (id=%s, password=admin)", user.ID)
}

// seedBackendsFromConfig imports backends from the config file into the SQLite
// database if the DB has no backends yet. This is a one-time migration: once
// backends exist in the DB, the config file is no longer consulted for backend
// definitions (the DB is authoritative).
func seedBackendsFromConfig(st *store.Store, cfg *config.InternalConfig) {
	existing, err := st.ListBackends()
	if err != nil {
		shared.Errorf("seed-backends: list: %v", err)
		return
	}
	if len(existing) > 0 {
		return // DB already has backends; don't overwrite.
	}

	// Create a built-in mcpbridge system backend that speaks MCP
	// This is used as a fallback when no other backends are available
	mcpbridgeBackend := &store.Backend{
		ID:            "mcpbridge",
		Command:       "mcp-bridge-builtin",
		PoolSize:      1,
		ToolPrefix:    "",
		Enabled:       true,
		IsSystem:      true,
		SelfReporting: true,
	}
	if err := st.CreateBackend(mcpbridgeBackend); err != nil {
		shared.Errorf("seed-backends: create mcpbridge: %v", err)
	} else {
		shared.Info("seed-backends: created built-in mcpbridge backend")
	}

	count := 0
	for id, bc := range cfg.Backends {
		envJSON := "{}"
		if len(bc.Env) > 0 {
			if data, err := json.Marshal(bc.Env); err == nil {
				envJSON = string(data)
			}
		}
		b := &store.Backend{
			ID:            id,
			Command:       bc.Command,
			PoolSize:      bc.PoolSize,
			ToolPrefix:    bc.ToolPrefix,
			Env:           envJSON,
			Enabled:       true,
			SelfReporting: bc.SelfReporting,
		}
		if err := st.CreateBackend(b); err != nil {
			shared.Errorf("seed-backends: create %s: %v", id, err)
			continue
		}
		count++
	}
	if count > 0 {
		shared.Infof("seed-backends: imported %d backends from config into DB", count)
	}
}

// seedDefaultPolicies creates default safety policies if none exist
func seedDefaultPolicies(st *store.Store) {
	enforcerStore := store.NewEnforcerStore(st.DB())

	// Check if policies already exist
	policies, err := enforcerStore.ListPolicies()
	if err != nil {
		shared.Errorf("seed-policies: failed to list policies: %v", err)
		return
	}
	if len(policies) > 0 {
		shared.Info("seed-policies: policies already exist, skipping")
		return
	}

	// Default policies from policies.yaml
	defaultPolicies := []enforcer.PolicyRow{
		{
			ID:          "prevent_resource_exhaustion",
			Name:        "Prevent Resource Exhaustion",
			Description: "Block extremely resource-intensive operations (bulk exports, full table scans, etc.)",
			Scope:       "global",
			Expression:  "safety.resource_cost >= 10",
			Action:      "DENY",
			Severity:    "HIGH",
			Message:     "This operation is extremely resource-intensive. Try a more targeted approach.",
			Enabled:     true,
			Priority:    100,
		},
		{
			ID:          "global_block_destructive",
			Name:        "Block Destructive Operations",
			Description: "Force human approval for any 'delete' or 'admin' impact",
			Scope:       "global",
			Expression:  "safety.impact_scope in ['delete', 'admin']",
			Action:      "PENDING_APPROVAL",
			Severity:    "CRITICAL",
			Message:     "Destructive operations require human approval. An administrator must approve this request before it can proceed.",
			Enabled:     true,
			Priority:    50,
		},
		{
			ID:          "block_dangerous_oracle_ops",
			Name:        "Block Dangerous Oracle Operations",
			Description: "Prevent DROP, TRUNCATE operations via AI",
			Scope:       "backend",
			Expression:  "tool.matches('(?i).*(drop|truncate).*') && safety.impact_scope == 'delete'",
			Action:      "DENY",
			Severity:    "CRITICAL",
			Message:     "DROP/TRUNCATE operations are prohibited via AI interface",
			Enabled:     true,
			Priority:    25,
		},
	}

	for _, policy := range defaultPolicies {
		if err := enforcerStore.CreatePolicy(policy); err != nil {
			shared.Errorf("seed-policies: failed to create policy %s: %v", policy.ID, err)
		} else {
			shared.Infof("seed-policies: created policy %s", policy.ID)
		}
	}

	shared.Infof("seed-policies: created %d default policies", len(defaultPolicies))
}
