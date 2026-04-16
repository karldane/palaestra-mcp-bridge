package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
)

// JSONRPCError represents a JSON-RPC error object
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC response
type JSONRPCResponse struct {
	ID      interface{}     `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// ApprovalRequestExecutor implements enforcer.ApprovalExecutor
type ApprovalRequestExecutor struct {
	poolManager *poolmgr.PoolManager
	getPool     func(userID, backendID string) *poolmgr.Pool
	getPrefix   func(backendID string) string // Returns the prefix for a backend (e.g., "github" for "github_delete_file")
}

// NewApprovalRequestExecutor creates a new approval executor
func NewApprovalRequestExecutor(poolManager *poolmgr.PoolManager, getPool func(userID, backendID string) *poolmgr.Pool, getPrefix func(backendID string) string) enforcer.ApprovalExecutor {
	return &ApprovalRequestExecutor{
		poolManager: poolManager,
		getPool:     getPool,
		getPrefix:   getPrefix,
	}
}

// stripToolPrefix strips the prefix from a tool name (e.g., "github_delete_file" -> "delete_file")
func stripToolPrefix(toolName, prefix string) string {
	if prefix == "" {
		return toolName
	}
	stripped := strings.TrimPrefix(toolName, prefix+"_")
	stripped = strings.TrimPrefix(stripped, prefix+"/")
	return stripped
}

// ExecuteRequest executes the original request with the user's environment
func (e *ApprovalRequestExecutor) ExecuteRequest(userID string, backendID string, requestBody string) (int, string, error) {
	shared.Infof("ApprovalExecutor: START - user=%s backend=%s body_len=%d", userID, backendID, len(requestBody))

	if backendID == "" {
		shared.Errorf("ApprovalExecutor: ERROR - empty backendID for user %s", userID)
		return 0, "", fmt.Errorf("empty backend ID - cannot execute request")
	}

	pool := e.getPool(userID, backendID)
	if pool == nil {
		shared.Errorf("ApprovalExecutor: ERROR - no pool found for backend=%s user=%s", backendID, userID)
		return 0, "", fmt.Errorf("no pool found for backend %s", backendID)
	}

	shared.Infof("ApprovalExecutor: pool found - backend=%s user=%s, warm_count=%d", backendID, userID, pool.WarmCount())
	pool.TouchLastUsed()

	// Wait for a warm process to be available (up to 30 seconds for spawning)
	// This is needed because pools may not have processes spawned yet for this user
	if !pool.WaitForWarm(30 * time.Second) {
		shared.Errorf("ApprovalExecutor: TIMEOUT waiting for warm process - backend=%s user=%s, warm_count=%d", backendID, userID, pool.WarmCount())
		return http.StatusServiceUnavailable, "", fmt.Errorf("timeout waiting for backend process to start (backend: %s)", backendID)
	}

	shared.Infof("ApprovalExecutor: got warm process - backend=%s user=%s", backendID, userID)

	// Get prefix for this backend and strip it from tool name
	prefix := ""
	if e.getPrefix != nil {
		prefix = e.getPrefix(backendID)
		if prefix != "" {
			shared.Infof("ApprovalExecutor: stripping prefix '%s' for backend %s", prefix, backendID)
			requestBody = stripPrefixFromRequestBody(requestBody, prefix)
		}
	}

	// Now get the warm process
	select {
	case proc := <-pool.Warm:
		return e.executeWithProcess(pool, proc, requestBody)
	default:
		// This shouldn't happen after WaitForWarm succeeded, but handle it
		shared.Errorf("ApprovalExecutor: no process after WaitForWarm succeeded - backend=%s user=%s", backendID, userID)
		return http.StatusServiceUnavailable, "", fmt.Errorf("no warm processes available after waiting")
	}
}

// stripPrefixFromRequestBody strips the tool prefix from a JSON-RPC request body
func stripPrefixFromRequestBody(body string, prefix string) string {
	var req map[string]interface{}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return body
	}

	params, ok := req["params"].(map[string]interface{})
	if !ok {
		return body
	}

	if name, ok := params["name"].(string); ok {
		stripped := stripToolPrefix(name, prefix)
		if stripped != name {
			shared.Infof("ApprovalExecutor: stripped prefix '%s' from tool '%s' -> '%s'", prefix, name, stripped)
			params["name"] = stripped
			newBody, _ := json.Marshal(req)
			return string(newBody)
		}
	}

	return body
}

// executeWithProcess executes the request using the given process
func (e *ApprovalRequestExecutor) executeWithProcess(pool *poolmgr.Pool, proc *poolmgr.ManagedProcess, requestBody string) (int, string, error) {
	var msg poolmgr.JSONRPCMessage
	if err := json.Unmarshal([]byte(requestBody), &msg); err != nil {
		pool.Warm <- proc
		return http.StatusBadRequest, "", fmt.Errorf("invalid JSON-RPC: %w", err)
	}

	reqID := fmt.Sprintf("%v", msg.ID)
	if reqID == "" || reqID == "<nil>" {
		reqID = fmt.Sprintf("exec-%d", time.Now().UnixNano())
		msg.ID = reqID
		modifiedBody, _ := json.Marshal(msg)
		requestBody = string(modifiedBody)
	}

	buf := new(bytes.Buffer)
	if err := json.Compact(buf, []byte(requestBody)); err != nil {
		buf.Reset()
		buf.WriteString(requestBody)
	}

	respCh := pool.RegisterRequest(reqID)
	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())
	shared.Debugf("ExecuteRequest: sent request, waiting for response (timeout=60s)")

	select {
	case response, ok := <-respCh:
		pool.UnregisterRequest(reqID)
		if !ok || len(response) == 0 {
			pool.Warm <- proc
			return http.StatusGatewayTimeout, "", fmt.Errorf("no response received")
		}

		shared.Debugf("ExecuteRequest: received response, len=%d", len(response))

		// Parse the JSON-RPC response to determine success/failure
		var rpcResp JSONRPCResponse
		if err := json.Unmarshal(response, &rpcResp); err != nil {
			// Can't parse - treat as success with raw response
			shared.Warnf("ExecuteRequest: could not parse JSON-RPC response: %v", err)
			pool.Warm <- proc
			return http.StatusOK, string(response), nil
		}

		if rpcResp.Error != nil {
			// JSON-RPC error response
			shared.Infof("ExecuteRequest: JSON-RPC error code=%d message=%s", rpcResp.Error.Code, rpcResp.Error.Message)
			pool.Warm <- proc
			// Use JSON-RPC error code as HTTP status (or 400 if it's an internal error)
			httpStatus := rpcResp.Error.Code
			if httpStatus < 100 || httpStatus >= 600 {
				httpStatus = http.StatusBadRequest
			}
			return httpStatus, string(response), fmt.Errorf("JSON-RPC error: %s", rpcResp.Error.Message)
		}

		// Success response
		pool.Warm <- proc
		return http.StatusOK, string(response), nil

	case <-time.After(60 * time.Second):
		pool.UnregisterRequest(reqID)
		shared.Errorf("ExecuteRequest: timeout after 60s, killing stuck process")
		proc.Kill()
		pool.Warm <- proc
		return http.StatusGatewayTimeout, "", fmt.Errorf("request timeout after 60s")
	}
}
