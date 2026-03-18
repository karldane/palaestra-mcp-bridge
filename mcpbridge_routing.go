package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
)

// handleToolsList aggregates tools from all enabled backends
func (s *MCPBridgeServer) handleToolsList(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	fmt.Printf("[DEBUG handleToolsList] userID=%s START\n", userID)
	backends, err := s.app.store.ListBackends()
	if err != nil {
		fmt.Printf("[DEBUG handleToolsList] ERROR listing backends: %v\n", err)
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}
	fmt.Printf("[DEBUG handleToolsList] found %d backends in DB\n", len(backends))
	for i, b := range backends {
		fmt.Printf("[DEBUG handleToolsList] backend[%d]: id=%s, enabled=%v, command=%s\n", i, b.ID, b.Enabled, b.Command)
	}
	if len(backends) == 0 {
		fmt.Printf("[DEBUG handleToolsList] no backends, falling back to default\n")
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}

	var allTools []map[string]interface{}
	var firstError error

	for _, backend := range backends {
		if !backend.Enabled {
			fmt.Printf("[DEBUG handleToolsList] skipping disabled backend: %s\n", backend.ID)
			continue
		}

		fmt.Printf("[DEBUG handleToolsList] processing backend: %s for user: %s\n", backend.ID, userID)
		pool := s.app.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		proc, err := pool.WaitForWarmWithMax(15 * time.Second)
		if err != nil {
			if strings.Contains(err.Error(), "max_pool_size reached") {
				fmt.Printf("[DEBUG handleToolsList] max pool size reached for backend %s: %v\n", backend.ID, err)
			} else {
				fmt.Printf("[DEBUG handleToolsList] timeout waiting for warm process for backend %s\n", backend.ID)
			}
			allTools = append(allTools, map[string]interface{}{
				"name":        backend.ID + "_error",
				"description": "Backend temporarily unavailable: " + err.Error(),
			})
			continue
		}

		fmt.Printf("[DEBUG handleToolsList] got warm process for backend %s\n", backend.ID)
		reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "tools/list",
			"id":      reqID,
		}
		reqBody, _ := json.Marshal(req)
		reqBody = append(reqBody, '\n')
		fmt.Printf("[DEBUG handleToolsList] sending tools/list to backend %s, reqID=%s\n", backend.ID, reqID)

		respCh := pool.RegisterRequest(reqID)
		proc.Stdin.Write(reqBody)

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			fmt.Printf("[DEBUG handleToolsList] received response from backend %s, ok=%v, len=%d\n", backend.ID, ok, len(response))
			if ok && len(response) > 0 {
				var result struct {
					Result struct {
						Tools []map[string]interface{} `json:"tools"`
					} `json:"result"`
					Error map[string]interface{} `json:"error"`
				}
				if err := json.Unmarshal(response, &result); err == nil {
					if result.Error != nil {
						fmt.Printf("[DEBUG handleToolsList] tools/list error from backend %s: %v\n", backend.ID, result.Error)
						if firstError == nil {
							firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
						}
					} else {
						fmt.Printf("[DEBUG handleToolsList] backend %s returned %d tools\n", backend.ID, len(result.Result.Tools))
						if err := s.app.store.SetBackendCapabilities(backend.ID, result.Result.Tools); err != nil {
							fmt.Printf("[DEBUG handleToolsList] failed to cache capabilities for %s: %v\n", backend.ID, err)
						} else {
							fmt.Printf("[DEBUG handleToolsList] cached %d tools for backend %s\n", len(result.Result.Tools), backend.ID)
						}
						prefix := s.toolMuxer.GetPrefixForBackend(backend.ID)
						fmt.Printf("[DEBUG handleToolsList] prefix for backend %s: %q\n", backend.ID, prefix)
						for _, tool := range result.Result.Tools {
							if name, ok := tool["name"].(string); ok && prefix != "" {
								tool["name"] = prefix + "_" + name
							}
							allTools = append(allTools, tool)
						}
					}
				} else {
					fmt.Printf("[DEBUG handleToolsList] JSON unmarshal error from backend %s: %v\n", backend.ID, err)
				}
			}
		case <-time.After(10 * time.Second):
			pool.UnregisterRequest(reqID)
			fmt.Printf("[DEBUG handleToolsList] TIMEOUT waiting for tools/list from backend %s\n", backend.ID)
		}

		pool.Warm <- proc
	}

	// Add mcpbridge system tools
	allTools = append(allTools, shared.SystemToolsAsMap()...)

	respID := id
	if respID == nil || respID == "" {
		respID = 1
	}

	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      respID,
		"result": map[string]interface{}{
			"tools": allTools,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleToolsCall routes the call to the correct backend based on tool name prefix
func (s *MCPBridgeServer) handleToolsCall(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	modifiedBody, router, err := s.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		fmt.Printf("tools/call routing error: %v\n", err)
		s.handleDefaultBackend(w, r, userID, body, id)
		return
	}

	pool := router.Pool
	pool.TouchLastUsed()

	select {
	case proc := <-pool.Warm:
		var msg poolmgr.JSONRPCMessage
		if err := json.Unmarshal(modifiedBody, &msg); err != nil {
			pool.Warm <- proc
			http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
			return
		}

		reqID := fmt.Sprintf("%v", msg.ID)
		if reqID == "" || reqID == "<nil>" {
			reqID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
			msg.ID = reqID
			modifiedBody, _ = json.Marshal(msg)
		}

		buf := new(bytes.Buffer)
		if err := json.Compact(buf, modifiedBody); err != nil {
			buf.Reset()
			buf.Write(modifiedBody)
		}
		buf.WriteByte('\n')

		respCh := pool.RegisterRequest(reqID)
		proc.Stdin.Write(buf.Bytes())

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			if ok && len(response) > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.Write(response)
			} else {
				w.WriteHeader(http.StatusGatewayTimeout)
				w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"No response received"}}`))
			}
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 30s"}}`))
		}

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}

// handleDefaultBackend routes to the default backend (legacy behavior)
func (s *MCPBridgeServer) handleDefaultBackend(w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := s.app.defaultBackendID()
	pool := s.app.getPoolForUser(userID, backendID)
	pool.TouchLastUsed()

	select {
	case proc := <-pool.Warm:
		var msg poolmgr.JSONRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			pool.Warm <- proc
			http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
			return
		}

		reqID := fmt.Sprintf("%v", msg.ID)
		if reqID == "" || reqID == "<nil>" {
			reqID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
			msg.ID = reqID
			body, _ = json.Marshal(msg)
		}

		buf := new(bytes.Buffer)
		if err := json.Compact(buf, body); err != nil {
			buf.Reset()
			buf.Write(body)
		}
		buf.WriteByte('\n')

		respCh := pool.RegisterRequest(reqID)
		proc.Stdin.Write(buf.Bytes())

		select {
		case response, ok := <-respCh:
			pool.UnregisterRequest(reqID)
			if ok && len(response) > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.Write(response)
			} else {
				w.WriteHeader(http.StatusGatewayTimeout)
				w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"No response received"}}`))
			}
		case <-time.After(30 * time.Second):
			pool.UnregisterRequest(reqID)
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 30s"}}`))
		}

		pool.Warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}
