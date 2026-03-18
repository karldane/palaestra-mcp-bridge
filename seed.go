package main

import (
	"encoding/json"

	"github.com/mcp-bridge/mcp-bridge/config"
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

	count := 0
	for id, bc := range cfg.Backends {
		envJSON := "{}"
		if len(bc.Env) > 0 {
			if data, err := json.Marshal(bc.Env); err == nil {
				envJSON = string(data)
			}
		}
		b := &store.Backend{
			ID:         id,
			Command:    bc.Command,
			PoolSize:   bc.PoolSize,
			ToolPrefix: bc.ToolPrefix,
			Env:        envJSON,
			Enabled:    true,
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
