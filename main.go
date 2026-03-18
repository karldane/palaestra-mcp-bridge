package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
	"github.com/mcp-bridge/mcp-bridge/web"
)


func main() {
	seedUser := flag.Bool("seed", false, "Seed a default test user (admin@localhost / admin) if no users exist")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	insecureTesting := flag.Bool("INSECURE_TESTING_MODE", false, "Enable insecure testing mode on port 8081 (no auth, admin user)")
	flag.Parse()

	if *versionFlag {
		fmt.Println("mcp-bridge version 1.0.0")
		os.Exit(0)
	}
	logJSON("info", "DEBUG: MAIN FUNCTION STARTED - UNIQUE STRING 12345")

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
		logJSON("info", fmt.Sprintf("loaded config from %s with %d backends, authCodeTTL=%v, accessTokenTTL=%v",
			configPath, len(cfg.Backends), cfg.Server.AuthCodeTTL, cfg.Server.AccessTokenTTL))
	} else {
		logJSON("info", fmt.Sprintf("no config file loaded (tried %s): %v", configPath, err))
		// No config file — single backend mode using env vars
		cfg = &config.InternalConfig{
			Server: config.ServerConfig{
				Port:                 port,
				LogLevel:             "info",
				AuthCodeTTL:          "10m",
				AccessTokenTTL:       "24h",
				AuthCodeTTLParsed:    auth.DefaultCodeTTL,
				AccessTokenTTLParsed: auth.DefaultTokenTTL,
			},
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
		CodeTTL:  cfg.Server.AuthCodeTTLParsed,
		TokenTTL: cfg.Server.AccessTokenTTLParsed,
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
			return nil, fmt.Errorf("backend %s found: %w", backendID, err)
		}
		// Build the environment: start with base env
		env := os.Environ()

		// Apply Env mappings with a dummy token for testing
		// The mappings convert user token keys to backend-specific keys
		if b.EnvMappings != "" && b.EnvMappings != "{}" {
			var mappings map[string]string
			if err := json.Unmarshal([]byte(b.EnvMappings), &mappings); err == nil {
				// Use a dummy token value for testing - the user can put their
				// actual token in Env template if they want real auth
				dummyToken := "probe_test_token"
				for _, backendKey := range mappings {
					env = append(env, backendKey+"="+dummyToken)
				}
			}
		}

		// Apply static Env template (higher priority than mappings)
		if b.Env != "" && b.Env != "{}" {
			var envMap map[string]string
			if err := json.Unmarshal([]byte(b.Env), &envMap); err == nil {
				for k, v := range envMap {
					env = append(env, k+"="+v)
				}
			}
		}

		// Debug: include command and env in result
		fmt.Printf("[DEBUG ProbeBackend] backend=%s, command=%q, env=%v\n", backendID, b.Command, env)

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

	// Start insecure testing server on port 8081 if enabled
	if *insecureTesting {
		fmt.Println("================================================================================")
		fmt.Println("                    *** INSECURE TESTING MODE ENABLED ***                     ")
		fmt.Println("                                                                                ")
		fmt.Println("  THIS BRIDGE IS RUNNING IN INSECURE TESTING MODE.                             ")
		fmt.Println("  Port 8081 is open WITHOUT AUTHENTICATION - DO NOT EXPOSE TO THE INTERNET!   ")
		fmt.Println("  All requests on port 8081 are authenticated as admin user.                  ")
		fmt.Println("================================================================================")

		// Get admin user from DB
		adminUser, err := st.GetUserByEmail("admin@localhost")
		if err != nil {
			log.Fatalf("INSECURE_TESTING_MODE requires admin@localhost user to exist: %v", err)
		}

		// Create mux for testing server (same as production but without auth)
		testMux := http.NewServeMux()
		authHandler.Register(testMux)
		webHandler.Register(testMux)
		testMux.HandleFunc("/healthz", healthzHandler)
		testMux.HandleFunc("/readyz", readyzHandler(a))

		// Testing server uses the same MCP bridge but injects admin user into context
		testMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), auth.UserIDKey, adminUser.ID)
			mcpBridgeServer.Handler().ServeHTTP(w, r.WithContext(ctx))
		}))

		go func() {
			logJSON("info", "INSECURE TESTING SERVER started on :8081 (no auth, admin user)")
			if err := http.ListenAndServe(":8081", testMux); err != nil && err != http.ErrServerClosed {
				log.Printf("INSECURE TESTING SERVER error: %v", err)
			}
		}()
	}

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
