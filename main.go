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
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
	"github.com/mcp-bridge/mcp-bridge/web"
)

// userStoreAdapter wraps *store.Store to implement enforcer.UserStore
type userStoreAdapter struct {
	store *store.Store
}

func (a *userStoreAdapter) GetUser(id string) (*enforcer.User, error) {
	u, err := a.store.GetUser(id)
	if err != nil {
		return nil, err
	}
	return &enforcer.User{ID: u.ID, Email: u.Email, Role: u.Role}, nil
}

func main() {
	seedUser := flag.Bool("seed", false, "Seed a default test user (admin@localhost / admin) if no users exist")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	insecureTesting := flag.Bool("INSECURE_TESTING_MODE", false, "Enable insecure testing mode on port 8081 (no auth, admin user)")
	precacheEmail := flag.String("precache-tooling", "", "User email to use for precaching backend tools (runs precache and exits)")
	flag.Parse()

	if *versionFlag {
		fmt.Println("mcp-bridge version 1.0.0")
		os.Exit(0)
	}

	// Precache mode - scan backends and cache tools, then exit
	if *precacheEmail != "" {
		dbPath := os.Getenv("DB_PATH")
		if dbPath == "" {
			dbPath = "mcp-bridge.db"
		}
		st, err := store.New(dbPath)
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		defer st.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		err = RunPrecache(ctx, PrecacheConfig{
			UserEmail:     *precacheEmail,
			Store:         st,
			EnforcerStore: store.NewEnforcerStore(st.DB()),
		})
		if err != nil {
			log.Fatalf("Precache failed: %v", err)
		}
		os.Exit(0)
	}
	shared.Info("MCP Bridge starting...")

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

	port := "8020"
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
		// Initialize log level from config
		shared.SetLogLevel(cfg.Server.LogLevel)
		shared.Infof("loaded config from %s with %d backends, authCodeTTL=%v, accessTokenTTL=%v",
			configPath, len(cfg.Backends), cfg.Server.AuthCodeTTL, cfg.Server.AccessTokenTTL)
	} else {
		shared.Infof("no config file loaded (tried %s): %v", configPath, err)
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

	// Verify encryption key is available and working
	// The keystore must be able to decrypt existing tokens
	ks := st.KeyStore()
	if ks == nil {
		shared.Errorf("FATAL: Keystore not initialized - encryption key is required")
		shared.Errorf("Set ENCRYPTION_KEY or ENCRYPTION_KEY_FILE environment variable")
		log.Fatalf("keystore not initialized")
	}

	// Try to decrypt an existing token to verify the encryption key is valid
	testToken, err := st.GetUserTokenDecrypted("58993e3001a71e9e8cd3a21fc1cd9430", "atlassian", "API_TOKEN")
	if err != nil {
		if strings.Contains(err.Error(), "keystore not initialized") || strings.Contains(err.Error(), "neither") {
			shared.Errorf("FATAL: Encryption key not loaded - tokens cannot be decrypted")
			shared.Errorf("Set ENCRYPTION_KEY (value) or ENCRYPTION_KEY_FILE (path) environment variable")
			log.Fatalf("encryption key required")
		}
		shared.Errorf("FATAL: Encryption key error: %v", err)
		log.Fatalf("encryption key error: %v", err)
	} else if len(testToken) == 0 {
		shared.Warnf("WARNING: Decryption succeeded but API_TOKEN is empty (no credentials stored)")
		shared.Infof("Encryption key: OK (key is valid, tokens will be passed as empty)")
	} else {
		shared.Infof("Encryption key: OK (decrypted token length=%d)", len(testToken))
	}

	// Migrate any remaining plaintext tokens to encrypted format.
	// This is idempotent - already-encrypted tokens are skipped.
	if err := st.MigrateSecrets(context.Background()); err != nil {
		shared.Warnf("Secret migration warning: %v", err)
	}

	// Verify all encrypted tokens can be decrypted with the current key.
	success, fail, err := st.VerifyEncryptedSecrets(context.Background())
	if err != nil {
		shared.Warnf("Secret verification error: %v", err)
	} else {
		shared.Infof("Secret verification: %d OK, %d failed", success, fail)
		if fail > 0 {
			shared.Warnf("WARNING: %d secrets failed verification - backends using those tokens will fail", fail)
		}
	}

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

	// ---- Enforcer initialization ----
	enforcerConfig := enforcer.DefaultEnforcerConfig()
	enforcerConfig.Enabled = true
	enforcerConfig.EnableDescriptionDecoration = true
	enforcerConfig.EnableKillSwitch = true

	var enf *enforcer.Enforcer
	if enforcerConfig.Enabled {
		var err error
		// Wrap the store.User in an adapter that implements enforcer.UserStore
		userStore := &userStoreAdapter{store: st}
		enf, err = enforcer.NewEnforcer(enforcerConfig, store.NewEnforcerStore(st.DB()), userStore)
		if err != nil {
			shared.Errorf("Failed to initialize enforcer: %v", err)
			shared.Errorf("Continuing without policy enforcement")
			enf = nil
		} else {
			shared.Infof("Enforcer initialized with policy enforcement")
			enf.StartRateLimitRefill(context.Background(), time.Second)
		}
	}

	// ---- Enforcer tool profile scanner ----
	// Scan self-reporting backends at startup to populate enforcer_tool_profiles.
	if enf != nil {
		scanSelfReportingBackends(st, shared.Infof, shared.Warnf)
		loadOverridesIntoResolver(st, enf, shared.Infof, shared.Warnf)
	}

	// ---- Wire up app ----
	a := &app{
		store:       st,
		auth:        authHandler,
		poolManager: pm,
		toolMuxer:   toolMuxer,
		config:      cfg,
		enforcer:    enf,
	}

	// Wire up approval executor if enforcer is enabled
	if enf != nil {
		executor := web.NewApprovalRequestExecutor(pm, func(userID, backendID string) *poolmgr.Pool {
			return a.getPoolForUser(userID, backendID)
		}, func(backendID string) string {
			return toolMuxer.GetPrefixForBackend(backendID)
		})
		enf.SetExecutor(executor)
	}

	// Check for uncached backends and warn
	if uncached, err := st.GetUncachedBackends(); err == nil && len(uncached) > 0 {
		shared.Warnf("WARNING: The following backends have no cached tools: %s", strings.Join(uncached, ", "))
		shared.Warnf("Run './mcp-bridge --precache-tooling=YOUR_EMAIL' to cache tools for these backends")
	}

	// Start background retry loop for unavailable backends
	go StartBackendRetryLoop(context.Background(), st)

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
	webHandler.PoolManager = pm // Wire pool manager for admin pool status
	webHandler.Enforcer = enf   // Wire enforcer for admin UI
	// Wire live reload: when an admin creates/edits/deletes a backend via the
	// web UI, refresh the muxer prefix map and tear down stale pools so that
	// subsequent requests pick up the new configuration immediately.
	webHandler.OnBackendChange = func(backendID string) {
		toolMuxer.RefreshPrefixes()
		removed := pm.RemovePoolsByBackend(backendID)
		shared.Infof("backend %s changed: refreshed prefixes, removed %d pool(s)", backendID, removed)
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

		// Debug: log backend being probed (don't log env var values)
		shared.Debugf("ProbeBackend: backend=%s, command=%q, env_count=%d", backendID, b.Command, len(env))

		result := poolmgr.ProbeBackend(b.Command, env, 30*time.Second)
		return json.Marshal(result)
	}
	webHandler.Register(mux)

	// Health checks — NOT behind auth middleware
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler(a))
	mux.HandleFunc("/sse", sseHandler(a))

	// Root path - MCP Streamable HTTP (behind auth middleware)
	mcpBridgeServer := NewMCPBridgeServer(a, toolMuxer)
	mux.Handle("/", authHandler.Middleware(mcpBridgeServer.Handler()))

	shared.Infof("MCP SSE Bridge started on :%s (command=%s, pool=%d, db=%s, idleGC=%s)",
		port, command, poolSize, dbPath, idleTimeout)

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

		// Wrap webHandler with admin context injection for testing mode
		// Create a sub-mux that will handle /web/* routes with admin user context
		webMux := http.NewServeMux()
		webHandler.Register(webMux)
		wrappedWebMux := webHandler.WithAdminUser(adminUser, webMux)
		testMux.Handle("/web/", wrappedWebMux)

		testMux.HandleFunc("/healthz", healthzHandler)
		testMux.HandleFunc("/readyz", readyzHandler(a))
		testMux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), auth.UserIDKey, adminUser.ID)
			sseHandler(a)(w, r.WithContext(ctx))
		})

		// Testing server uses the same MCP bridge but injects admin user into context
		testMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), auth.UserIDKey, adminUser.ID)
			mcpBridgeServer.Handler().ServeHTTP(w, r.WithContext(ctx))
		}))

		go func() {
			shared.Info("INSECURE TESTING SERVER started on :8081 (no auth, admin user)")
			if err := http.ListenAndServe(":8081", testMux); err != nil && err != http.ErrServerClosed {
				log.Printf("INSECURE TESTING SERVER error: %v", err)
			}
		}()
	}

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// StartBackendRetryLoop runs in background, attempting to reconnect unavailable backends
func StartBackendRetryLoop(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shared.Info("Backend retry loop stopped")
			return
		case <-ticker.C:
			backends, err := st.GetBackendsNeedingRetry()
			if err != nil {
				shared.Warnf("Backend retry loop: failed to get backends needing retry: %v", err)
				continue
			}

			for _, backendID := range backends {
				shared.Infof("Backend retry: attempting reconnection for %s", backendID)
				// For now, just attempt a process spawn - if it succeeds, mark available
				// The actual tool fetching will happen on first use
				backend, err := st.GetBackend(backendID)
				if err != nil {
					shared.Warnf("Backend retry: failed to get backend %s: %v", backendID, err)
					continue
				}

				// Build a minimal env: system vars + backend static env.
				// We don't have a user context here, so we can't inject user tokens,
				// but the backend needs at least PATH and any static env to start.
				env := os.Environ()
				if backend.Env != "" && backend.Env != "{}" {
					var envMap map[string]string
					if err := json.Unmarshal([]byte(backend.Env), &envMap); err == nil {
						for k, v := range envMap {
							env = append(env, k+"="+v)
						}
					}
				}

				// Quick test spawn — only mark available on success.
				// Don't mark unavailable on failure because this probe lacks user
				// tokens; real requests handle marking unavailable when they fail.
				result := poolmgr.ProbeBackend(backend.Command, env, 30*time.Second)
				if result.Status == "ok" {
					shared.Infof("Backend retry: %s reconnected successfully", backendID)
					st.SetBackendAvailable(backendID)
				} else {
					shared.Debugf("Backend retry: %s probe failed (tokens may be required): %s", backendID, result.Message)
				}
			}
		}
	}
}
