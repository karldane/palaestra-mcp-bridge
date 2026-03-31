package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
)

// rootHandler dispatches based on HTTP method. opencode sends both GET (SSE)
// and POST (JSON-RPC) to the root "/" path.
func rootHandler(a *app) http.HandlerFunc {
	sse := sseHandler(a)
	msg := messagesHandler(a)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			sse(w, r)
		case http.MethodPost:
			msg(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func readyzHandler(a *app) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Readyz just checks that the pool manager has at least one pool.
		if a.poolManager.PoolCount() > 0 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No pools"))
	}
}

// findAvailableBackend finds any enabled backend that has an available pool
func findAvailableBackend(a *app, userID, excludeBackendID string) (*poolmgr.Pool, string) {
	backends, err := a.store.ListBackends()
	if err != nil {
		return nil, ""
	}
	for _, b := range backends {
		if b.Enabled && b.ID != excludeBackendID {
			pool := a.getPoolForUser(userID, b.ID)
			if !pool.IsUnavailable() {
				return pool, b.ID
			}
		}
	}
	return nil, ""
}

func sseHandler(a *app) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r)
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		backendID := a.defaultBackendID()
		pool := a.getPoolForUser(userID, backendID)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// If default backend is unavailable, try to find any available backend
		if pool.IsUnavailable() {
			shared.Warnf("SSE: default backend %s is unavailable, looking for fallback", backendID)
			if fallbackPool, fallbackID := findAvailableBackend(a, userID, backendID); fallbackPool != nil {
				shared.Infof("SSE: falling back to backend %s", fallbackID)
				pool = fallbackPool
				backendID = fallbackID
			} else {
				shared.Warnf("SSE: no fallback available, rejecting connection")
				http.Error(w, "No backends available", http.StatusServiceUnavailable)
				return
			}
		}

		// Try to get a warm process immediately (fail fast for SSE)
		select {
		case proc := <-pool.Warm:
			pool.IncrementActive()
			go func() {
				<-r.Context().Done()
				proc.Kill()
				pool.DecrementActive()
				pool.Wg().Add(1)
				go pool.SpawnAndHandshake()
			}()

			for {
				select {
				case line, ok := <-proc.LineChan:
					if !ok {
						return
					}
					pool.BroadcastToSSE(line)
					fmt.Fprintf(w, "data: %s\n\n", string(line))
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case <-time.After(5 * time.Second):
			// Give the pool a chance to spawn a process, then check again
			if pool.IsUnavailable() {
				shared.Warnf("SSE: backend %s became unavailable while waiting, looking for fallback", backendID)
				if fallbackPool, fallbackID := findAvailableBackend(a, userID, backendID); fallbackPool != nil {
					shared.Infof("SSE: falling back to backend %s", fallbackID)
					pool = fallbackPool
					backendID = fallbackID
					// Wait for warm process from fallback with timeout
					var proc *poolmgr.ManagedProcess
					select {
					case proc = <-pool.Warm:
					case <-time.After(5 * time.Second):
						http.Error(w, "Fallback backend not ready", http.StatusServiceUnavailable)
						return
					case <-r.Context().Done():
						return
					}
					if proc != nil {
						pool.IncrementActive()
						go func() {
							<-r.Context().Done()
							proc.Kill()
							pool.DecrementActive()
							pool.Wg().Add(1)
							go pool.SpawnAndHandshake()
						}()

						for {
							select {
							case line, ok := <-proc.LineChan:
								if !ok {
									return
								}
								pool.BroadcastToSSE(line)
								fmt.Fprintf(w, "data: %s\n\n", string(line))
								w.(http.Flusher).Flush()
							case <-r.Context().Done():
								return
							}
						}
					}
				}
				http.Error(w, "Backend unavailable", http.StatusServiceUnavailable)
				return
			}
			// Try one more time
			select {
			case proc := <-pool.Warm:
				pool.IncrementActive()
				go func() {
					<-r.Context().Done()
					proc.Kill()
					pool.DecrementActive()
					pool.Wg().Add(1)
					go pool.SpawnAndHandshake()
				}()

				for {
					select {
					case line, ok := <-proc.LineChan:
						if !ok {
							return
						}
						pool.BroadcastToSSE(line)
						fmt.Fprintf(w, "data: %s\n\n", string(line))
						w.(http.Flusher).Flush()
					case <-r.Context().Done():
						return
					}
				}
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
	}
}

func messagesHandler(a *app) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r)
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		var msg poolmgr.JSONRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
			return
		}

		method := msg.Method

		// Handle standard MCP methods directly
		switch method {
		case "initialize":
			handleInitialize(a, w, r, userID, body, msg.ID)
			return
		case "tools/list":
			handleToolsList(a, w, r, userID, body, msg.ID)
			return
		case "tools/call":
			handleToolsCall(a, w, r, userID, body, msg.ID)
			return
		default:
			// Fallback to default backend for other methods
			handleDefaultBackend(a, w, r, userID, body, msg.ID)
		}
	}
}
