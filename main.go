package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
	"github.com/mcp-bridge/mcp-bridge/web"
)

// app holds the shared dependencies wired up in main() and used by handlers.
type app struct {
	store       *store.Store
	auth        *auth.Handler
	poolManager *poolmgr.PoolManager
	toolMuxer   *muxer.ToolMuxer
	config      *config.InternalConfig
}

// getPoolForUser returns a per-user pool for the given backend. It builds an
// explicit environment from the bridge env + backend static env + per-user
// tokens, then gets or creates a dedicated pool keyed by backendID:userID.
func (a *app) getPoolForUser(userID, backendID string) *poolmgr.Pool {
	// Look up backend from DB first, fall back to config.
	var command string
	var poolSize int

	if b, err := a.store.GetBackend(backendID); err == nil {
		command = b.Command
		poolSize = b.PoolSize
	} else if bc, ok := a.config.Backends[backendID]; ok {
		command = bc.Command
		poolSize = bc.PoolSize
	} else {
		// Shouldn't happen, but fall back to defaults.
		command = "echo"
		poolSize = 1
	}

	env := a.toolMuxer.BuildEnvForUser(userID, backendID)
	return a.poolManager.GetOrCreateUserPool(
		backendID, userID, command, poolSize, env,
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

// handleInitialize handles the initialize method
func handleInitialize(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "mcp-bridge",
				"version": "1.0.0",
			},
		},
	}
	json.NewEncoder(w).Encode(response)
}

// handleToolsList aggregates tools from all enabled backends
func handleToolsList(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backends, err := a.store.ListBackends()
	if err != nil {
		// Fallback to default backend on error
		handleDefaultBackend(a, w, r, userID, body, id)
		return
	}
	if len(backends) == 0 {
		// No backends configured, return only system tools
		var allTools []map[string]interface{}

		// Add system tools
		systemTools := []map[string]interface{}{
			{
				"name":        "mcpbridge_ping",
				"description": "Check bridge connectivity and get current timestamp",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				"name":        "mcpbridge_version",
				"description": "Get mcp-bridge version information",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				"name":        "mcpbridge_list_backends",
				"description": "List configured backends",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				"name":        "mcpbridge_refresh_tools",
				"description": "Refresh and list tools from all enabled backends",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		}
		allTools = append(allTools, systemTools...)

		// Build aggregated response
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
		return
	}

	var allTools []map[string]interface{}
	var firstError error

	for _, backend := range backends {
		if !backend.Enabled {
			continue
		}

		pool := a.getPoolForUser(userID, backend.ID)
		pool.TouchLastUsed()

		select {
		case proc := <-pool.Warm:
			// Build tools/list request
			reqID := fmt.Sprintf("list-%s-%d", backend.ID, time.Now().UnixNano())
			req := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "tools/list",
				"id":      reqID,
			}
			reqBody, _ := json.Marshal(req)
			reqBody = append(reqBody, '\n')

			respCh := pool.RegisterRequest(reqID)
			proc.Stdin.Write(reqBody)

			select {
			case response, ok := <-respCh:
				pool.UnregisterRequest(reqID)
				if ok && len(response) > 0 {
					var result struct {
						Result struct {
							Tools []map[string]interface{} `json:"tools"`
						} `json:"result"`
						Error map[string]interface{} `json:"error"`
					}
					if err := json.Unmarshal(response, &result); err == nil {
						if result.Error != nil {
							log.Printf("tools/list error from backend %s: %v", backend.ID, result.Error)
							if firstError == nil {
								firstError = fmt.Errorf("backend %s error: %v", backend.ID, result.Error)
							}
						} else {
							// Add prefix to tool names if configured
							prefix := a.toolMuxer.GetPrefixForBackend(backend.ID)
							for _, tool := range result.Result.Tools {
								if name, ok := tool["name"].(string); ok && prefix != "" {
									tool["name"] = prefix + "_" + name
								}
								allTools = append(allTools, tool)
							}
						}
					}
				}
			case <-time.After(10 * time.Second):
				pool.UnregisterRequest(reqID)
				log.Printf("tools/list timeout from backend %s", backend.ID)
			}

			pool.Warm <- proc
		default:
			log.Printf("No warm process for backend %s", backend.ID)
		}
	}

	// Add system tools
	systemTools := []map[string]interface{}{
		{
			"name":        "mcpbridge_ping",
			"description": "Check bridge connectivity and get current timestamp",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_version",
			"description": "Get mcp-bridge version information",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_list_backends",
			"description": "List configured backends",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "mcpbridge_refresh_tools",
			"description": "Refresh and list tools from all enabled backends",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
	allTools = append(allTools, systemTools...)

	// Build aggregated response
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
func handleToolsCall(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	// Check if it's a system tool (mcpbridge_*)
	var toolReq map[string]interface{}
	if err := json.Unmarshal(body, &toolReq); err == nil {
		if params, ok := toolReq["params"].(map[string]interface{}); ok {
			if name, ok := params["name"].(string); ok {
				if strings.HasPrefix(name, "mcpbridge_") {
					// System tools are handled directly
					var result string
					switch name {
					case "mcpbridge_ping":
						result = "pong " + time.Now().UTC().Format(time.RFC3339)
					case "mcpbridge_version":
						result = "mcp-bridge version 1.0.0"
					case "mcpbridge_list_backends":
						backends, err := a.store.ListBackends()
						if err != nil {
							result = "Error: " + err.Error()
						} else {
							for _, b := range backends {
								status := "disabled"
								if b.Enabled {
									status = "enabled"
								}
								result += "- " + b.ID + ": " + status + "\n"
							}
						}
					case "mcpbridge_refresh_tools":
						result = "Refreshed tools from all enabled backends"
					default:
						result = "Unknown system tool: " + name
					}
					response := map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      id,
						"result": map[string]interface{}{
							"content": []map[string]interface{}{
								{
									"type": "text",
									"text": result,
								},
							},
							"status": "ok",
						},
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
					return
				}
			}
		}
	}

	// Use muxer to route to correct backend
	modifiedBody, router, err := a.toolMuxer.HandleToolsCall(userID, body)
	if err != nil {
		log.Printf("tools/call routing error: %v", err)
		// Fallback to default backend
		handleDefaultBackend(a, w, r, userID, body, id)
		return
	}

	pool := router.Pool
	pool.TouchLastUsed()

	select {
	case proc := <-pool.Warm:
		// Ensure we have a valid ID
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

		// Compact and add newline
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
func handleDefaultBackend(a *app, w http.ResponseWriter, r *http.Request, userID string, body []byte, id interface{}) {
	backendID := a.defaultBackendID()
	pool := a.getPoolForUser(userID, backendID)
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

func logJSON(level, message string) {
	entry := poolmgr.LogEntry{
		Level:   level,
		Message: message,
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
}

// seedDefaultUser creates a test user (admin@localhost / admin) if no users
// exist in the database. This is for local development and testing only.
func seedDefaultUser(st *store.Store) {
	// Check if the user already exists by trying to look up by email.
	if existing, err := st.GetUserByEmail("admin@localhost"); err == nil {
		if existing.Role != "admin" {
			existing.Role = "admin"
			st.UpdateUser(existing)
			logJSON("info", "seed: upgraded admin@localhost to role=admin")
		} else {
			logJSON("info", "seed: user admin@localhost already exists, skipping")
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
		logJSON("error", fmt.Sprintf("seed: failed to create user: %v", err))
		return
	}
	logJSON("info", fmt.Sprintf("seed: created user admin@localhost (id=%s, password=admin)", user.ID))
}

// seedBackendsFromConfig imports backends from the config file into the SQLite
// database if the DB has no backends yet. This is a one-time migration: once
// backends exist in the DB, the config file is no longer consulted for backend
// definitions (the DB is authoritative).
func seedBackendsFromConfig(st *store.Store, cfg *config.InternalConfig) {
	existing, err := st.ListBackends()
	if err != nil {
		logJSON("error", fmt.Sprintf("seed-backends: list: %v", err))
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
			logJSON("error", fmt.Sprintf("seed-backends: create %s: %v", id, err))
			continue
		}
		count++
	}
	if count > 0 {
		logJSON("info", fmt.Sprintf("seed-backends: imported %d backends from config into DB", count))
	}
}

func main() {
	seedUser := flag.Bool("seed", false, "Seed a default test user (admin@localhost / admin) if no users exist")
	flag.Parse()

	command := os.Getenv("COMMAND")
	if command == "" {
		command = "sh -c 'cat; sleep 1'"
	}

	poolSize := 2
	if s := os.Getenv("POOL_SIZE"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			poolSize = n
		}
	}

	port := "8080"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	// Load config file if present (optional — env vars still work without it)
	var cfg *config.InternalConfig

	configPath := os.Getenv("CONFIG_FILE")
	if configPath == "" {
		configPath = "config.yaml"
	}

	if loadedCfg, err := config.Load(configPath); err == nil {
		cfg = loadedCfg
		if cfg.Server.Port != "" {
			port = cfg.Server.Port
		}
		logJSON("info", fmt.Sprintf("loaded config from %s with %d backends", configPath, len(cfg.Backends)))
	} else {
		// No config file — single backend mode using env vars
		cfg = &config.InternalConfig{
			Server: config.ServerConfig{Port: port, LogLevel: "info"},
			Backends: map[string]config.BackendConfig{
				"default": {
					Command:  command,
					PoolSize: poolSize,
				},
			},
		}
	}

	// ---- SQLite store ----
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "mcp-bridge.db"
	}
	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer st.Close()

	// Seed a default user if requested.
	if *seedUser {
		seedDefaultUser(st)
	}

	// Seed backends from config into DB if the DB has none yet.
	// This provides a migration path: existing config.yaml backends are
	// imported into SQLite on first run, after which the DB is authoritative.
	seedBackendsFromConfig(st, cfg)

	// ---- Idle timeout for per-user pools ----
	idleTimeout := poolmgr.DefaultIdleTimeout
	if s := os.Getenv("IDLE_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			idleTimeout = d
		}
	}

	// ---- Pool manager with GC ----
	pm := poolmgr.NewPoolManagerWithGC(command, poolSize, idleTimeout)
	defer pm.ShutdownAll()

	// ---- Tool muxer backed by SQLite store ----
	toolMuxer := muxer.NewToolMuxerWithStore(pm, st, cfg)

	// ---- Auth handler ----
	issuer := os.Getenv("ISSUER")
	if issuer == "" {
		issuer = "http://localhost:" + port
	}

	authHandler := &auth.Handler{
		Store:    st,
		Issuer:   issuer,
		CodeTTL:  auth.DefaultCodeTTL,
		TokenTTL: auth.DefaultTokenTTL,
	}

	// ---- Wire up app ----
	a := &app{
		store:       st,
		auth:        authHandler,
		poolManager: pm,
		toolMuxer:   toolMuxer,
		config:      cfg,
	}

	// ---- HTTP routing ----
	mux := http.NewServeMux()

	// OAuth endpoints — NOT behind auth middleware
	authHandler.Register(mux)

	// Web UI — cookie-based session auth (NOT behind OAuth middleware)
	templateDir := os.Getenv("TEMPLATE_DIR")
	if templateDir == "" {
		templateDir = "templates"
	}
	webHandler, err := web.NewHandler(st, templateDir)
	if err != nil {
		log.Fatalf("failed to load web templates: %v", err)
	}
	// Wire live reload: when an admin creates/edits/deletes a backend via the
	// web UI, refresh the muxer prefix map and tear down stale pools so that
	// subsequent requests pick up the new configuration immediately.
	webHandler.OnBackendChange = func(backendID string) {
		toolMuxer.RefreshPrefixes()
		removed := pm.RemovePoolsByBackend(backendID)
		logJSON("info", fmt.Sprintf("backend %s changed: refreshed prefixes, removed %d pool(s)", backendID, removed))
	}
	// Wire probe: when admin clicks "Test" on a backend, spawn a temporary
	// process and attempt the MCP handshake, returning JSON result bytes.
	webHandler.OnProbeBackend = func(backendID string) ([]byte, error) {
		b, err := st.GetBackend(backendID)
		if err != nil {
			return nil, fmt.Errorf("backend %s not found: %w", backendID, err)
		}
		// Build the environment: bridge env + backend static env.
		var env []string
		if b.Env != "" && b.Env != "{}" {
			var envMap map[string]string
			if err := json.Unmarshal([]byte(b.Env), &envMap); err == nil {
				env = os.Environ()
				for k, v := range envMap {
					env = append(env, k+"="+v)
				}
			}
		}
		result := poolmgr.ProbeBackend(b.Command, env, 10*time.Second)
		return json.Marshal(result)
	}
	webHandler.Register(mux)

	// Health checks — NOT behind auth middleware
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler(a))

	// Root path - MCP Streamable HTTP (behind auth middleware)
	mcpBridgeServer := NewMCPBridgeServer(a, toolMuxer)
	mux.Handle("/", authHandler.Middleware(mcpBridgeServer.Handler()))

	logJSON("info", fmt.Sprintf("MCP SSE Bridge started on :%s (command=%s, pool=%d, db=%s, idleGC=%s)",
		port, command, poolSize, dbPath, idleTimeout))

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
