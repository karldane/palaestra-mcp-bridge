package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
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
