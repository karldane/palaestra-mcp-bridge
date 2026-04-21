package main

import (
	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// app holds the shared dependencies wired up in main() and used by handlers.
type app struct {
	store       *store.Store
	auth        *auth.Handler
	poolManager *poolmgr.PoolManager
	toolMuxer   *muxer.ToolMuxer
	config      *config.InternalConfig
	enforcer    *enforcer.Enforcer
}

// getPoolForUser returns a per-user pool for the given backend. It builds an
// explicit environment from the bridge env + backend static env + per-user
// tokens, then gets or creates a dedicated pool keyed by backendID:userID.
// Returns nil and logs an error if the environment cannot be built (e.g. a
// template expression fails to resolve).
func (a *app) getPoolForUser(userID, backendID string) *poolmgr.Pool {
	// Look up backend from DB first, fall back to config.
	var command string
	var poolSize, minPoolSize, maxPoolSize int

	if b, err := a.store.GetBackend(backendID); err == nil {
		command = b.Command
		poolSize = b.PoolSize
		minPoolSize = b.MinPoolSize
		maxPoolSize = b.MaxPoolSize
		// Set defaults
		if minPoolSize == 0 {
			minPoolSize = 1
		}
		if maxPoolSize == 0 {
			maxPoolSize = minPoolSize
		}
		shared.Debugf("getPoolForUser: backend %s found in DB: command=%q, minPoolSize=%d, maxPoolSize=%d", backendID, command, minPoolSize, maxPoolSize)
	} else if bc, ok := a.config.Backends[backendID]; ok {
		command = bc.Command
		poolSize = bc.PoolSize
		minPoolSize = bc.PoolSize
		maxPoolSize = bc.PoolSize
		shared.Debugf("getPoolForUser: backend %s found in config: command=%q, poolSize=%d", backendID, command, poolSize)
	} else {
		// Shouldn't happen, but fall back to defaults.
		command = "echo"
		poolSize = 1
		minPoolSize = 1
		maxPoolSize = 1
		shared.Debugf("getPoolForUser: backend %s not found, using default echo", backendID)
	}

	env, err := a.toolMuxer.BuildEnvForUser(userID, backendID)
	if err != nil {
		shared.Debugf("getPoolForUser: failed to build env for user %s, backend %s: %v", userID, backendID, err)
		return nil
	}
	shared.Debugf("getPoolForUser: get-or-create pool for backendID=%s, userID=%s, command=%q, min=%d, max=%d, envCount=%d", backendID, userID, command, minPoolSize, maxPoolSize, len(env))
	return a.poolManager.GetOrCreateUserPool(
		backendID, userID, command, minPoolSize, maxPoolSize, env,
	)
}

// defaultBackendID returns the ID of the first enabled backend from the DB,
// falling back to the first config backend.
func (a *app) defaultBackendID() string {
	if backends, err := a.store.ListBackends(); err == nil {
		for _, b := range backends {
			if b.Enabled {
				return b.ID
			}
		}
	}
	for id := range a.config.Backends {
		return id
	}
	return "default"
}

// getBackendIDForRequest returns the backend ID to use for a request.
// If the requested backend ID is "default" and the mcpbridge backend exists,
// it returns "mcpbridge" instead.
func (a *app) getBackendIDForRequest(requestedBackendID string) string {
	if requestedBackendID == "default" {
		// Check if mcpbridge backend exists in DB
		if mcpbridge, err := a.store.GetBackend("mcpbridge"); err == nil && mcpbridge.Enabled {
			return "mcpbridge"
		}
		// Check if mcpbridge backend exists in config
		if _, ok := a.config.Backends["mcpbridge"]; ok {
			return "mcpbridge"
		}
		// If no backends exist, default to mcpbridge
		backends, err := a.store.ListBackends()
		if err == nil && len(backends) == 0 {
			return "mcpbridge"
		}
	}
	return requestedBackendID
}
