package muxer

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/credential"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
)

type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolsCallRequest represents a tools/call request from the MCP client.
// The params contain "name" and "arguments" directly (not a "tools" array).
type ToolsCallRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		// Tools field is for backward compatibility with older implementations
		Tools []ToolCall `json:"tools,omitempty"`
	} `json:"params,omitempty"`
}

type ToolMuxer struct {
	mu              sync.RWMutex
	backendPrefixes map[string]string
	poolManager     *poolmgr.PoolManager
	secrets         credential.SecretStore // legacy; used only if store is nil
	store           *store.Store           // SQLite store for per-user tokens
	config          *config.InternalConfig
}

func NewToolMuxer(pm *poolmgr.PoolManager, secrets credential.SecretStore, cfg *config.InternalConfig) *ToolMuxer {
	tm := &ToolMuxer{
		backendPrefixes: make(map[string]string),
		poolManager:     pm,
		secrets:         secrets,
		config:          cfg,
	}

	for backendID, backendCfg := range cfg.Backends {
		if backendCfg.ToolPrefix != "" {
			tm.backendPrefixes[backendCfg.ToolPrefix] = backendID
		}
	}

	return tm
}

// NewToolMuxerWithStore creates a ToolMuxer backed by a SQLite store for
// per-user credential lookup (replaces the broken os.Setenv approach).
// When store is non-nil, backend metadata is read from the DB; the config
// is only used as a fallback for tests or when the DB has no backends.
func NewToolMuxerWithStore(pm *poolmgr.PoolManager, st *store.Store, cfg *config.InternalConfig) *ToolMuxer {
	tm := &ToolMuxer{
		backendPrefixes: make(map[string]string),
		poolManager:     pm,
		store:           st,
		config:          cfg,
	}

	// Populate prefix map from DB first, fall back to config.
	if st != nil {
		if backends, err := st.ListBackends(); err == nil && len(backends) > 0 {
			for _, b := range backends {
				if b.ToolPrefix != "" {
					tm.backendPrefixes[b.ToolPrefix] = b.ID
				}
			}
			return tm
		}
	}

	// Fallback: populate from config.
	for backendID, backendCfg := range cfg.Backends {
		if backendCfg.ToolPrefix != "" {
			tm.backendPrefixes[backendCfg.ToolPrefix] = backendID
		}
	}

	return tm
}

func (tm *ToolMuxer) HandleToolsCall(userID string, body []byte) ([]byte, *PoolRouter, error) {
	var req ToolsCallRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, fmt.Errorf("failed to parse tools/call request: %w", err)
	}

	// Get tool name from params (MCP spec uses params.name, not params.tools)
	toolName := req.Params.Name
	if toolName == "" && len(req.Params.Tools) > 0 {
		// Fallback to legacy format for backward compatibility
		toolName = req.Params.Tools[0].Name
	}
	if toolName == "" {
		return nil, nil, fmt.Errorf("no tool name in request params")
	}

	backendID, prefix, err := tm.findBackendForTool(toolName)
	if err != nil {
		return nil, nil, err
	}

	command, poolSize, _, ok := tm.getBackendConfig(backendID)
	if !ok {
		return nil, nil, fmt.Errorf("backend %q not found in store or config", backendID)
	}

	// Build explicit env for this user+backend.
	env := tm.BuildEnvForUser(userID, backendID)

	// Get or create a per-user pool with the user's credentials in the env.
	pool := tm.poolManager.GetOrCreateUserPool(
		backendID, userID, command, poolSize, env,
	)

	router := &PoolRouter{
		Pool:        pool,
		UserID:      userID,
		Backend:     backendID,
		Prefix:      prefix,
		ShouldStrip: prefix != "",
		Dedicated:   true,
	}

	modifiedBody := body
	if prefix != "" {
		modifiedBody = tm.stripPrefixFromName(body, prefix)
	}

	return modifiedBody, router, nil
}

// BuildEnvForUser constructs a []string of "KEY=VALUE" pairs for the given
// user and backend. It starts with the bridge's own env, overlays the
// backend's static Env map from config, then overlays per-user tokens from
// the SQLite store (or legacy SecretStore).
func (tm *ToolMuxer) BuildEnvForUser(userID, backendID string) []string {
	// Start with bridge's own environment as a base.
	envMap := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			envMap[e[:idx]] = e[idx+1:]
		}
	}

	// Overlay backend-level static env from store/config.
	_, _, backendEnv, ok := tm.getBackendConfig(backendID)
	if ok {
		for k, v := range backendEnv {
			envMap[k] = v
		}
	}

	// Overlay per-user tokens.
	if tm.store != nil {
		tokens, err := tm.store.GetUserTokens(userID, backendID)
		if err == nil {
			for _, tok := range tokens {
				envMap[tok.EnvKey] = tok.Value
			}
		}
	} else if tm.secrets != nil {
		// Legacy path: use old SecretStore for tests.
		backendCfg, cfgOK := tm.config.Backends[backendID]
		if cfgOK {
			for _, secretRef := range backendCfg.Secrets {
				value, err := tm.secrets.Get(userID, secretRef.EnvKey)
				if err == nil {
					envMap[secretRef.EnvKey] = value
				}
			}
		}
	}

	// Convert map to []string.
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

func (tm *ToolMuxer) findBackendForTool(toolName string) (string, string, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for prefix, backendID := range tm.backendPrefixes {
		if strings.HasPrefix(toolName, prefix+"_") || strings.HasPrefix(toolName, prefix+"/") {
			return backendID, prefix, nil
		}
	}

	// Single-backend fallback: if there's exactly one backend, route everything there.
	ids := tm.listBackendIDs()
	if len(ids) == 1 {
		return ids[0], "", nil
	}

	return "", "", fmt.Errorf("no backend found for tool: %s", toolName)
}

func (tm *ToolMuxer) stripPrefix(body []byte, prefix string) []byte {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	params, ok := req["params"].(map[string]interface{})
	if !ok {
		return body
	}

	tools, ok := params["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return body
	}

	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		return body
	}

	if name, ok := tool["name"].(string); ok {
		stripped := strings.TrimPrefix(name, prefix+"_")
		stripped = strings.TrimPrefix(stripped, prefix+"/")
		tool["name"] = stripped
	}

	newBody, _ := json.Marshal(req)
	return newBody
}

// stripPrefixFromName strips the prefix from params.name (correct MCP format)
func (tm *ToolMuxer) stripPrefixFromName(body []byte, prefix string) []byte {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	params, ok := req["params"].(map[string]interface{})
	if !ok {
		return body
	}

	if name, ok := params["name"].(string); ok {
		stripped := strings.TrimPrefix(name, prefix+"_")
		stripped = strings.TrimPrefix(stripped, prefix+"/")
		params["name"] = stripped
	}

	newBody, _ := json.Marshal(req)
	return newBody
}

func (tm *ToolMuxer) GetPrefixForBackend(backendID string) string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for prefix, bid := range tm.backendPrefixes {
		if bid == backendID {
			return prefix
		}
	}
	return ""
}

// getBackendConfig retrieves backend configuration. It checks the SQLite store
// first (DB is source of truth), then falls back to the config map.
func (tm *ToolMuxer) getBackendConfig(backendID string) (command string, poolSize int, env map[string]string, ok bool) {
	// Try store first.
	if tm.store != nil {
		b, err := tm.store.GetBackend(backendID)
		if err == nil {
			var envMap map[string]string
			if b.Env != "" && b.Env != "{}" {
				if jsonErr := json.Unmarshal([]byte(b.Env), &envMap); jsonErr != nil {
					envMap = nil
				}
			}
			if envMap == nil {
				envMap = make(map[string]string)
			}
			return b.Command, b.PoolSize, envMap, true
		}
	}

	// Fallback to config.
	if bc, found := tm.config.Backends[backendID]; found {
		return bc.Command, bc.PoolSize, bc.Env, true
	}

	return "", 0, nil, false
}

// listBackendIDs returns all backend IDs, preferring the DB store.
func (tm *ToolMuxer) listBackendIDs() []string {
	if tm.store != nil {
		if backends, err := tm.store.ListBackends(); err == nil && len(backends) > 0 {
			ids := make([]string, 0, len(backends))
			for _, b := range backends {
				ids = append(ids, b.ID)
			}
			return ids
		}
	}
	ids := make([]string, 0, len(tm.config.Backends))
	for id := range tm.config.Backends {
		ids = append(ids, id)
	}
	return ids
}

// RefreshPrefixes reloads the tool prefix map from the DB. Call this after
// backends are added/edited via the web UI so routing picks up changes.
func (tm *ToolMuxer) RefreshPrefixes() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.backendPrefixes = make(map[string]string)

	if tm.store != nil {
		if backends, err := tm.store.ListBackends(); err == nil && len(backends) > 0 {
			for _, b := range backends {
				if b.ToolPrefix != "" {
					tm.backendPrefixes[b.ToolPrefix] = b.ID
				}
			}
			return
		}
	}

	for backendID, backendCfg := range tm.config.Backends {
		if backendCfg.ToolPrefix != "" {
			tm.backendPrefixes[backendCfg.ToolPrefix] = backendID
		}
	}
}

type PoolRouter struct {
	Pool        *poolmgr.Pool
	UserID      string
	Backend     string
	Prefix      string
	ShouldStrip bool
	Dedicated   bool
}

func (pr *PoolRouter) StripPrefix(body []byte) []byte {
	if !pr.ShouldStrip || pr.Prefix == "" {
		return body
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	params, ok := req["params"].(map[string]interface{})
	if !ok {
		return body
	}

	tools, ok := params["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return body
	}

	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		return body
	}

	if name, ok := tool["name"].(string); ok {
		stripped := strings.TrimPrefix(name, pr.Prefix+"_")
		stripped = strings.TrimPrefix(stripped, pr.Prefix+"/")
		tool["name"] = stripped
	}

	newBody, _ := json.Marshal(req)
	return newBody
}

func (pr *PoolRouter) AddPrefix(body []byte) []byte {
	if pr.Prefix == "" {
		return body
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	params, ok := req["params"].(map[string]interface{})
	if !ok {
		return body
	}

	tools, ok := params["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return body
	}

	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		return body
	}

	if name, ok := tool["name"].(string); ok {
		tool["name"] = pr.Prefix + "_" + name
	}

	newBody, _ := json.Marshal(req)
	return newBody
}
