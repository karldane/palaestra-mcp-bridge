package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// PrecacheConfig holds configuration for precaching
type PrecacheConfig struct {
	UserEmail string       // CLI mode only (--precache-tooling flag)
	UserID    string       // UI mode: logged-in user's ID; takes precedence over fallback chain
	Store     *store.Store
}

// fetchToolsForPrecacheFn is a package-level variable so tests can replace it.
var fetchToolsForPrecacheFn = fetchToolsForPrecacheImpl

// RunPrecacheForBackend precaches tools for a single backend.
// If cfg.UserID is set, that user's tokens are used (UI-triggered path).
// If cfg.UserEmail is set, that user's tokens are used (CLI path).
// Otherwise the startup fallback chain is used (static env -> first admin -> no tokens).
// Returns the number of tools cached and any error encountered.
func RunPrecacheForBackend(ctx context.Context, cfg PrecacheConfig, backendID string) (int, error) {
	backend, err := cfg.Store.GetBackend(backendID)
	if err != nil {
		return 0, fmt.Errorf("get backend %s: %w", backendID, err)
	}
	if !backend.Enabled {
		return 0, nil
	}

	// Resolve tokens and user based on config
	var tokens []store.UserToken
	var user *store.User
	useStartupFallback := false

	switch {
	case cfg.UserID != "":
		tokens, err = cfg.Store.GetUserTokensDecrypted(cfg.UserID, backendID)
		if err != nil {
			return 0, fmt.Errorf("get tokens for user %s: %w", cfg.UserID, err)
		}
		user, err = cfg.Store.GetUser(cfg.UserID)
		if err != nil {
			shared.Warnf("Failed to get user %s: %v", cfg.UserID, err)
		}
	case cfg.UserEmail != "":
		user, err = cfg.Store.GetUserByEmail(cfg.UserEmail)
		if err != nil {
			return 0, fmt.Errorf("get user by email: %w", err)
		}
		tokens, err = cfg.Store.GetUserTokensDecrypted(user.ID, backendID)
		if err != nil {
			shared.Warnf("Failed to get tokens for %s: %v", backendID, err)
			tokens = nil
		}
	default:
		useStartupFallback = true
	}

	env, err := buildEnvForPrecache(backend, tokens, user)
	if err != nil {
		return 0, fmt.Errorf("build env: %w", err)
	}

	var tools []map[string]interface{}
	tools, err = fetchToolsForPrecacheFn(ctx, backend.Command, env)
	if err == nil && len(tools) == 0 {
		err = fmt.Errorf("no tools returned")
	}

	// Startup fallback chain: static env -> first admin -> no tokens
	if useStartupFallback && (err != nil || len(tools) == 0) {
		shared.Infof("Startup precache %s: static env returned %d tools, trying admin user", backendID, len(tools))
		admin := cfg.Store.FirstAdminUser()
		if admin != nil {
			adminTokens, tokenErr := cfg.Store.GetUserTokensDecrypted(admin.ID, backendID)
			if tokenErr == nil {
				adminEnv, envErr := buildEnvForPrecache(backend, adminTokens, admin)
				if envErr == nil {
					adminTools, spawnErr := fetchToolsForPrecacheFn(ctx, backend.Command, adminEnv)
					if spawnErr == nil {
						tools = adminTools
						err = nil
					}
				}
			}
		}
	}

	if useStartupFallback && (err != nil || len(tools) == 0) {
		shared.Warnf("Startup precache %s: no credentials available, storing 0 tools", backendID)
		cfg.Store.SetBackendPrecacheError(backendID, "no credentials available for precache")
		return 0, nil
	}

	if err != nil {
		cfg.Store.SetBackendPrecacheError(backendID, err.Error())
		return 0, err
	}

	// Cache the tools
	if err := cacheBackendCapabilities(cfg.Store, backendID, tools); err != nil {
		cfg.Store.SetBackendPrecacheError(backendID, err.Error())
		return 0, fmt.Errorf("cache capabilities: %w", err)
	}

	// Mark as available (also clears precache_error)
	cfg.Store.SetBackendAvailable(backendID)
	return len(tools), nil
}

// RunPrecache scans all enabled backends and caches their tool definitions.
// Refactored to call RunPrecacheForBackend per backend.
func RunPrecache(ctx context.Context, cfg PrecacheConfig) error {
	shared.Infof("Starting tool precache for user %s", cfg.UserEmail)

	backends, err := cfg.Store.ListBackends()
	if err != nil {
		return fmt.Errorf("list backends: %w", err)
	}

	var failed []string
	var succeeded []string

	for _, backend := range backends {
		if !backend.Enabled {
			continue
		}
		if _, err := RunPrecacheForBackend(ctx, cfg, backend.ID); err != nil {
			shared.Warnf("Precache failed for %s: %v", backend.ID, err)
			failed = append(failed, backend.ID)
		} else {
			succeeded = append(succeeded, backend.ID)
		}
	}

	shared.Infof("Precache complete: %d succeeded, %d failed", len(succeeded), len(failed))
	if len(failed) > 0 {
		shared.Warnf("Failed backends: %s", strings.Join(failed, ", "))
		return fmt.Errorf("precache failed for backends: %s", strings.Join(failed, ", "))
	}
	return nil
}

// buildEnvForPrecache builds environment variables for a backend during precache.
// If a user is provided, template expressions ({{...}}) in the backend's env are
// resolved against that user's record so the backend receives real credentials.
func buildEnvForPrecache(backend *store.Backend, tokens []store.UserToken, user *store.User) (map[string]string, error) {
	// Start with current process environment
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}

	// Tell backends we're in precache mode - they can skip heavy initialization
	env["MCP_PRECACHE"] = "true"

	// Parse system-wide env vars from backend config
	if backend.Env != "" && backend.Env != "{}" {
		// Handle both JSON object and double-quoted JSON string formats
		envStr := backend.Env
		if strings.HasPrefix(envStr, "\"") && strings.HasSuffix(envStr, "\"") {
			// It's a JSON string - unmarshal to get the actual string
			var unquoted string
			if err := json.Unmarshal([]byte(envStr), &unquoted); err == nil {
				envStr = unquoted
			}
		}

		var backendEnv map[string]string
		if err := json.Unmarshal([]byte(envStr), &backendEnv); err == nil {
			for k, v := range backendEnv {
				env[k] = v
			}
		} else {
			shared.Warnf("Failed to parse backend env: %v", err)
		}
	}

	// Add user tokens and apply env mappings
	if backend.EnvMappings != "" && backend.EnvMappings != "{}" {
		var mappings map[string]string
		if err := json.Unmarshal([]byte(backend.EnvMappings), &mappings); err == nil {
			// Build reverse mapping (backend key -> user key)
			reverseMap := make(map[string]string)
			for userKey, backendKey := range mappings {
				reverseMap[backendKey] = userKey
			}

			// For each token, add it to env using the user key (not backend key)
			for _, token := range tokens {
				userKey := token.EnvKey
				// If this token's key is already a backend key (e.g., ATLASSIAN_API_TOKEN),
				// find the corresponding user key (e.g., API_TOKEN)
				if mappedUserKey, ok := reverseMap[token.EnvKey]; ok {
					userKey = mappedUserKey
				}
				env[userKey] = token.Value
			}

			// Apply mappings: convert user keys to backend keys
			result := make(map[string]string)
			for k, v := range env {
				if backendKey, ok := mappings[k]; ok {
					// Map user key to backend key
					result[backendKey] = v
				} else {
					// Keep all other keys as-is (including backend keys like ATLASSIAN_DOMAIN)
					result[k] = v
				}
			}
			env = result
			return resolveTemplatesInPrecacheEnv(env, user), nil
		}
	}

	// No mappings - add tokens directly
	for _, token := range tokens {
		env[token.EnvKey] = token.Value
	}

	return resolveTemplatesInPrecacheEnv(env, user), nil
}

// resolveTemplatesInPrecacheEnv resolves template expressions in the precache
// env map against the given user. If user is nil, template values are left
// in place (they will be stripped by the caller if needed).
func resolveTemplatesInPrecacheEnv(env map[string]string, user *store.User) map[string]string {
	if user == nil {
		return env
	}
	hasTmpl := false
	for _, v := range env {
		if strings.Contains(v, "{{") {
			hasTmpl = true
			break
		}
	}
	if !hasTmpl {
		return env
	}
	resolved, err := muxer.ResolveEnvTemplates(env, user)
	if err != nil {
		shared.Warnf("resolveTemplatesInPrecacheEnv: template resolution failed: %v", err)
		return env
	}
	return resolved
}

// fetchToolsForPrecacheImpl spawns a backend process and requests its tools
func fetchToolsForPrecacheImpl(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
	// Parse command
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	cmd := parts[0]
	args := parts[1:]

	execCmd := exec.Command(cmd, args...)
	execCmd.Env = envToSlice(env)
	execCmd.Dir = "/tmp"

	stdin, err := execCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Capture stderr for debugging
	stderrPipe, err := execCmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	stderrBuf := &strings.Builder{}
	go io.Copy(stderrBuf, stderrPipe)

	shared.Debugf("Starting process: %s %v", cmd, args)
	if err := execCmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	shared.Infof("Process started with PID: %d", execCmd.Process.Pid)

	// Send initialize
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "precache-init",
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "mcp-bridge-precache",
				"version": "1.0.0",
			},
		},
	}
	initBytes, _ := json.Marshal(initReq)
	stdin.Write(initBytes)
	stdin.Write([]byte("\n"))

	// Send tools/list
	toolsReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "precache-tools",
		"method":  "tools/list",
	}
	toolsBytes, _ := json.Marshal(toolsReq)
	stdin.Write(toolsBytes)
	stdin.Write([]byte("\n"))

	// Set timeout
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			execCmd.Process.Kill()
		case <-done:
		}
	}()

	// Read responses
	var tools []map[string]interface{}
	decoder := json.NewDecoder(stdout)
	for decoder.More() {
		var resp map[string]interface{}
		if err := decoder.Decode(&resp); err != nil {
			break
		}

		// Check for error
		if errResp, ok := resp["error"]; ok {
			shared.Warnf("MCP error response for %s: %v", command, errResp)
		}

		// Look for tools/list result
		if id, ok := resp["id"].(string); ok && id == "precache-tools" {
			if result, ok := resp["result"].(map[string]interface{}); ok {
				if t, ok := result["tools"].([]interface{}); ok {
					for _, tool := range t {
						if m, ok := tool.(map[string]interface{}); ok {
							tools = append(tools, m)
						}
					}
				}
			}
			break
		}
	}

	close(done)
	stdin.Close()
	execCmd.Wait()

	// Log stderr if no tools were returned (for debugging)
	if len(tools) == 0 {
		stderrOutput := stderrBuf.String()
		if stderrOutput != "" {
			shared.Debugf("Backend process stderr for %s: %s", command, strings.TrimSpace(stderrOutput))
		}
	}

	return tools, nil
}

// cacheBackendCapabilities stores tools in the backend_capabilities table
func cacheBackendCapabilities(s *store.Store, backendID string, tools []map[string]interface{}) error {
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("marshal tools: %w", err)
	}

	_, err = s.DB().Exec(`
		INSERT INTO backend_capabilities (backend_id, tools, tool_count, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET
			tools = excluded.tools,
			tool_count = excluded.tool_count,
			updated_at = CURRENT_TIMESTAMP`,
		backendID, string(toolsJSON), len(tools))

	return err
}

func envToSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// DrainOutput drains and discards all output from a reader
func drainOutput(r io.Reader) {
	buf := make([]byte, 1024)
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}
}
