package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// PrecacheConfig holds configuration for precaching
type PrecacheConfig struct {
	UserEmail string
	Store     *store.Store
}

// RunPrecache scans all enabled backends for a user and caches their tool definitions
func RunPrecache(ctx context.Context, cfg PrecacheConfig) error {
	shared.Infof("Starting tool precache for user %s", cfg.UserEmail)

	// Get user
	user, err := cfg.Store.GetUserByEmail(cfg.UserEmail)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	// Get enabled backends
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

		shared.Infof("Precaching tools for backend: %s", backend.ID)

		// Get user tokens for this backend (with decryption)
		tokens, err := cfg.Store.GetUserTokensDecrypted(user.ID, backend.ID)
		if err != nil {
			shared.Warnf("Failed to get tokens for %s: %v", backend.ID, err)
			failed = append(failed, backend.ID)
			continue
		}

		// Build environment for the backend
		env, err := buildEnvForPrecache(backend, tokens)
		if err != nil {
			shared.Warnf("Failed to build env for %s: %v", backend.ID, err)
			failed = append(failed, backend.ID)
			continue
		}

		// Spawn and get tools
		tools, err := fetchToolsForPrecache(ctx, backend.Command, env)
		if err != nil {
			shared.Warnf("Failed to fetch tools for %s: %v", backend.ID, err)
			cfg.Store.SetBackendUnavailable(backend.ID, err.Error())
			failed = append(failed, backend.ID)
			continue
		}

		// Cache the tools
		if err := cacheBackendCapabilities(cfg.Store, backend.ID, tools); err != nil {
			shared.Warnf("Failed to cache tools for %s: %v", backend.ID, err)
			failed = append(failed, backend.ID)
			continue
		}

		// Mark as available
		cfg.Store.SetBackendAvailable(backend.ID)
		shared.Infof("Cached %d tools for backend %s", len(tools), backend.ID)
		succeeded = append(succeeded, backend.ID)
	}

	// Summary
	shared.Infof("Precache complete: %d succeeded, %d failed", len(succeeded), len(failed))
	if len(failed) > 0 {
		shared.Warnf("Failed backends: %s", strings.Join(failed, ", "))
	}

	return nil
}

// buildEnvForPrecache builds environment variables for a backend
func buildEnvForPrecache(backend *store.Backend, tokens []store.UserToken) (map[string]string, error) {
	// Start with current process environment
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			env[e[:idx]] = e[idx+1:]
		}
	}

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
			return result, nil
		}
	}

	// No mappings - add tokens directly
	for _, token := range tokens {
		env[token.EnvKey] = token.Value
	}

	return env, nil
}

// fetchToolsForPrecache spawns a backend process and requests its tools
func fetchToolsForPrecache(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
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
