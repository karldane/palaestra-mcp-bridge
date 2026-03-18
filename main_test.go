package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/auth"
	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/muxer"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
	"github.com/mcp-bridge/mcp-bridge/web"
)

// testApp creates a fully wired app with a temp SQLite database, a seeded user,
// and a valid access token. Callers must defer cleanup().
func testApp(t *testing.T, command string, poolSize int) (a *app, token string, cleanup func()) {
	t.Helper()

	dir, err := ioutil.TempDir("", "mcp-bridge-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")

	st, err := store.New(dbPath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cfg := &config.InternalConfig{
		Server: config.ServerConfig{Port: "0", LogLevel: "info"},
		Backends: map[string]config.BackendConfig{
			"default": {
				Command:  command,
				PoolSize: poolSize,
			},
		},
	}

	pm := poolmgr.NewPoolManagerWithGC(command, poolSize, 15*time.Minute)
	tm := muxer.NewToolMuxerWithStore(pm, st, cfg)

	ah := &auth.Handler{
		Store:    st,
		Issuer:   "http://localhost:0",
		CodeTTL:  10 * time.Minute,
		TokenTTL: 1 * time.Hour,
	}

	a = &app{
		store:       st,
		auth:        ah,
		poolManager: pm,
		toolMuxer:   tm,
		config:      cfg,
	}

	// Seed a test user.
	user := &store.User{
		ID:       "test-user-1",
		Name:     "Test User",
		Email:    "test@example.com",
		Password: "password123",
	}
	if err := st.CreateUser(user); err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to seed user: %v", err)
	}

	// Create an access token directly (bypass OAuth flow for unit tests).
	sess := &store.OAuthSession{
		UserID:    user.ID,
		ClientID:  "test-client",
		Scope:     "mcp",
		ExpiresAt: time.Now().Add(1 * time.Hour).UTC(),
	}
	if err := st.CreateOAuthSession(sess); err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to create session: %v", err)
	}
	token = sess.AccessToken

	cleanup = func() {
		pm.ShutdownAll()
		st.Close()
		os.RemoveAll(dir)
	}

	return a, token, cleanup
}

// testAppWithPool creates a testApp and waits for the pool to have warm processes.
func testAppWithPool(t *testing.T, command string, poolSize int, warmTimeout time.Duration) (a *app, token string, pool *poolmgr.Pool, cleanup func()) {
	t.Helper()
	a, token, cleanup = testApp(t, command, poolSize)

	// Trigger pool creation by calling getPoolForUser.
	backendID := a.defaultBackendID()
	pool = a.getPoolForUser("test-user-1", backendID)

	if warmTimeout > 0 && !pool.WaitForWarm(warmTimeout) {
		cleanup()
		t.Fatalf("timeout waiting for warm processes (command=%s, size=%d)", command, poolSize)
	}
	return a, token, pool, cleanup
}

// authRequest creates an HTTP request with the Bearer token set.
func authRequest(method, url, body, token string) *http.Request {
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	var req *http.Request
	if bodyReader != nil {
		req = httptest.NewRequest(method, url, bodyReader)
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// ---------- Auth middleware tests ----------

func TestIntegration_RootReturns401WithoutToken(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Error("expected WWW-Authenticate header with Bearer")
	}
}

func TestIntegration_RootReturns401WithInvalidToken(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	req := authRequest("GET", "/", "", "invalid-token-abc")
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token, got %d", w.Code)
	}
}

func TestIntegration_HealthzNoAuth(t *testing.T) {
	// /healthz should work without auth
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ---------- SSE tests (with auth) ----------

func TestIntegration_SSEWithAuth(t *testing.T) {
	a, token, _, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}

	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected SSE data, got empty or no data")
	}
}

func TestIntegration_SSEStreamsProcessStdout(t *testing.T) {
	a, token, _, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "data: y") {
		t.Errorf("expected SSE data with 'y' from yes command, got: %s", body)
	}
}

// ---------- Messages tests (with auth) ----------

func TestIntegration_MessagesEndpointRoutesToStdin(t *testing.T) {
	a, token, _, cleanup := testAppWithPool(t, "cat", 1, 2*time.Second)
	defer cleanup()

	testPayload := `{"jsonrpc":"2.0","method":"test","id":1}`

	req := authRequest("POST", "/", testPayload, token)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Errorf("expected warm process to be available, got 503")
	}
	if w.Code == http.StatusUnauthorized {
		t.Errorf("expected auth to pass, got 401")
	}

	body := w.Body.String()
	if body == "" {
		t.Errorf("expected response body, got empty")
	}
}

func TestIntegration_MessagesReturns401WithoutAuth(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"jsonrpc":"2.0","method":"test","id":1}`))
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ---------- Readyz ----------

func TestIntegration_ReadyzReturns200WhenPoolExists(t *testing.T) {
	a, _, _, cleanup := testAppWithPool(t, "cat", 2, 2*time.Second)
	defer cleanup()

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	readyzHandler(a)(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestIntegration_ReadyzReturns503WhenNoPools(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 0)
	defer cleanup()

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	readyzHandler(a)(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestIntegration_HealthzAlwaysReturns200(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

// ---------- Pool lifecycle ----------

func TestIntegration_PoolRefillsAfterDisconnect(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()

	req1 := authRequest("GET", "/", "", token)
	req1 = req1.WithContext(ctx1)
	w1 := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w1, req1)

	// Wait for pool to refill
	if !pool.WaitForWarm(2 * time.Second) {
		t.Error("expected pool to refill after disconnect")
	}

	if pool.WarmCount() < 1 {
		t.Errorf("expected pool to refill after disconnect, got %d", pool.WarmCount())
	}
}

func TestIntegration_ConcurrentConnections(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "yes", 2, 3*time.Second)
	defer cleanup()

	numClients := 10
	var wg sync.WaitGroup
	wg.Add(numClients)

	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			req := authRequest("GET", "/", "", token)
			req = req.WithContext(ctx)
			req.Header.Set("Accept", "text/event-stream")

			w := httptest.NewRecorder()
			a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)
		}()
	}

	wg.Wait()

	// Wait for pool to refill
	if !pool.WaitForWarm(3 * time.Second) {
		t.Log("pool did not fully refill after concurrent connections")
	}

	time.Sleep(500 * time.Millisecond)

	if pool.WarmCount() != 2 {
		t.Errorf("expected pool to maintain 2 warm processes, got %d", pool.WarmCount())
	}
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active sessions after all disconnected, got %d", pool.ActiveCount())
	}
}

func TestIntegration_HighConcurrencyStress(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "yes", 2, 5*time.Second)
	defer cleanup()

	numClients := 50
	var wg sync.WaitGroup
	wg.Add(numClients)

	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()

			duration := time.Duration(10+rand.Intn(40)) * time.Millisecond

			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()

			req := authRequest("GET", "/", "", token)
			req = req.WithContext(ctx)
			req.Header.Set("Accept", "text/event-stream")

			w := httptest.NewRecorder()
			a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)
		}()
	}

	wg.Wait()

	// Wait for pool to refill
	if !pool.WaitForWarm(5 * time.Second) {
		t.Log("pool did not fully refill after stress test")
	}

	time.Sleep(500 * time.Millisecond)

	warmCount := pool.WarmCount()
	if warmCount != 2 {
		t.Errorf("expected WarmCount=2 after stress test, got %d", warmCount)
	}

	activeCount := pool.ActiveCount()
	if activeCount != 0 {
		t.Errorf("expected ActiveCount=0 after all clients disconnected, got %d", activeCount)
	}
}

func TestIntegration_GracefulShutdown(t *testing.T) {
	_, _, pool, cleanup := testAppWithPool(t, "cat", 2, 2*time.Second)
	defer cleanup()

	pool.Shutdown()

	if !pool.IsClosed() {
		t.Error("expected pool to be closed after Shutdown")
	}
}

func TestIntegration_ExponentialBackoffOnFailure(t *testing.T) {
	pool := poolmgr.NewPool("test-backoff", 1, "false")
	time.Sleep(50 * time.Millisecond)

	// Drain any that managed to get in before failure
	select {
	case <-pool.Warm:
	default:
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case <-pool.Warm:
		t.Error("should not spawn immediately due to backoff")
	default:
		// expected
	}

	pool.Shutdown()
}

func TestIntegration_ProcessReaperCleansUp(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	// Wait for pool to refill
	if !pool.WaitForWarm(3 * time.Second) {
		t.Error("pool did not refill after connection closed")
	}

	if pool.WarmCount() < 1 {
		t.Errorf("expected pool to refill after connection closed, got %d", pool.WarmCount())
	}
}

// ---------- Headers ----------

func TestIntegration_SSEContentTypeHeader(t *testing.T) {
	a, token, _, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected Content-Type to contain text/event-stream, got %s", contentType)
	}
}

func TestIntegration_SSECacheControlHeader(t *testing.T) {
	a, token, _, cleanup := testAppWithPool(t, "yes", 1, 2*time.Second)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	cacheControl := w.Header().Get("Cache-Control")
	if cacheControl != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %s", cacheControl)
	}
}

// ---------- Structured logging ----------

func TestIntegration_StructuredJSONLogging(t *testing.T) {
	entry := poolmgr.LogEntry{
		Level:   "info",
		Message: "test message",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal log: %v", err)
	}

	if !strings.Contains(string(data), "test message") {
		t.Error("expected log entry to contain message")
	}

	var parsed poolmgr.LogEntry
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal log: %v", err)
	}

	if parsed.Level != "info" {
		t.Errorf("expected level info, got %s", parsed.Level)
	}
}

func TestIntegration_ProcessKillUsesSIGKILL(t *testing.T) {
	pool := poolmgr.NewPool("test-kill", 0, "sleep 60")
	defer pool.Shutdown()

	proc, err := poolmgr.SpawnProcess(pool, "sleep 60", nil)
	if err != nil {
		t.Fatalf("failed to spawn process: %v", err)
	}

	if proc.Cmd.Process == nil {
		t.Fatal("expected process to be started")
	}

	proc.Kill()

	time.Sleep(100 * time.Millisecond)

	if proc.Cmd.ProcessState != nil && !proc.Cmd.ProcessState.Exited() {
		proc.Cmd.Process.Kill()
		t.Error("expected process to be killed")
	}
}

// ---------- Root handler routing ----------

func TestIntegration_RootHandler_MethodNotAllowed(t *testing.T) {
	a, token, cleanup := testApp(t, "cat", 0)
	defer cleanup()

	req := authRequest("DELETE", "/", "", token)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestIntegration_RootHandler_PostRoutes(t *testing.T) {
	a, token, cleanup := testApp(t, "cat", 0)
	defer cleanup()

	req := authRequest("POST", "/", `{"jsonrpc":"2.0","method":"test","id":1}`, token)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	// 503 means the handler ran (no warm processes) — not 404.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from empty pool, got %d", w.Code)
	}
}

func TestIntegration_RootHandler_GetRoutes(t *testing.T) {
	a, token, cleanup := testApp(t, "cat", 0)
	defer cleanup()

	req := authRequest("GET", "/", "", token)
	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	// 503 means the SSE handler ran (no warm processes) — not 404.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from empty pool, got %d", w.Code)
	}
}

// ---------- Per-user pool isolation ----------

func TestIntegration_DifferentUsersGetDifferentPools(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Seed a second user
	user2 := &store.User{
		ID:       "test-user-2",
		Name:     "Test User 2",
		Email:    "test2@example.com",
		Password: "password456",
	}
	if err := a.store.CreateUser(user2); err != nil {
		t.Fatalf("failed to create user2: %v", err)
	}

	backendID := a.defaultBackendID()
	pool1 := a.getPoolForUser("test-user-1", backendID)
	pool2 := a.getPoolForUser("test-user-2", backendID)

	if pool1 == pool2 {
		t.Error("expected different pools for different users")
	}

	// Same user should get the same pool
	pool1Again := a.getPoolForUser("test-user-1", backendID)
	if pool1 != pool1Again {
		t.Error("expected same pool for same user on second call")
	}

	// Both should be dedicated
	if !pool1.IsDedicated() {
		t.Error("expected pool1 to be dedicated")
	}
	if !pool2.IsDedicated() {
		t.Error("expected pool2 to be dedicated")
	}

	pool1.Shutdown()
	pool2.Shutdown()
}

// ---------- Full OAuth + opencode client flow ----------

func TestIntegration_FullOAuthAndOpencodeFlow(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Build a real HTTP test server with the full mux.
	mux := http.NewServeMux()
	a.auth.Register(mux)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler(a))
	mux.Handle("/", a.auth.Middleware(rootHandler(a)))

	// Update issuer to match the test server URL (set after server starts)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a.auth.Issuer = ts.URL

	client := ts.Client()
	// Don't follow redirects automatically
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// ---- Step 1: GET / without token → 401 ----
	t.Run("GET / returns 401 without token", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("GET / failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(wwwAuth, "Bearer") {
			t.Errorf("expected WWW-Authenticate with Bearer, got %s", wwwAuth)
		}
	})

	// ---- Step 2: GET /.well-known/oauth-authorization-server → metadata ----
	var metadata map[string]interface{}
	t.Run("GET metadata", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/.well-known/oauth-authorization-server")
		if err != nil {
			t.Fatalf("GET metadata failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
			t.Fatalf("failed to decode metadata: %v", err)
		}

		if metadata["authorization_endpoint"] == nil {
			t.Error("missing authorization_endpoint")
		}
		if metadata["token_endpoint"] == nil {
			t.Error("missing token_endpoint")
		}
		if metadata["registration_endpoint"] == nil {
			t.Error("missing registration_endpoint")
		}
	})

	// ---- Step 3: POST /register → dynamic client registration ----
	var clientID string
	t.Run("POST /register", func(t *testing.T) {
		regBody := `{"redirect_uris":["http://localhost:12345/callback"],"client_name":"opencode-test"}`
		resp, err := client.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
		if err != nil {
			t.Fatalf("POST /register failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			body, _ := ioutil.ReadAll(resp.Body)
			t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
		}

		var regResp map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
			t.Fatalf("failed to decode register response: %v", err)
		}
		clientID = regResp["client_id"].(string)
		if clientID == "" {
			t.Fatal("expected client_id")
		}
	})

	// ---- Step 4: PKCE + authorize + token exchange ----
	var accessToken string
	t.Run("Full PKCE flow", func(t *testing.T) {
		// Generate PKCE verifier + challenge
		codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		// POST /authorize (login form submission)
		form := url.Values{}
		form.Set("email", "test@example.com")
		form.Set("password", "password123")
		form.Set("client_id", clientID)
		form.Set("redirect_uri", "http://localhost:12345/callback")
		form.Set("state", "test-state-42")
		form.Set("code_challenge", codeChallenge)
		form.Set("scope", "mcp")

		resp, err := client.PostForm(ts.URL+"/authorize", form)
		if err != nil {
			t.Fatalf("POST /authorize failed: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusFound {
			t.Fatalf("expected 302 redirect, got %d", resp.StatusCode)
		}

		location := resp.Header.Get("Location")
		if !strings.Contains(location, "code=") {
			t.Fatalf("expected code in redirect, got: %s", location)
		}

		// Parse code from redirect URL
		redirectURL, err := url.Parse(location)
		if err != nil {
			t.Fatalf("failed to parse redirect URL: %v", err)
		}
		authCode := redirectURL.Query().Get("code")
		state := redirectURL.Query().Get("state")
		if authCode == "" {
			t.Fatal("empty auth code")
		}
		if state != "test-state-42" {
			t.Errorf("expected state=test-state-42, got %s", state)
		}

		// POST /token (exchange code for token)
		tokenForm := url.Values{}
		tokenForm.Set("grant_type", "authorization_code")
		tokenForm.Set("code", authCode)
		tokenForm.Set("redirect_uri", "http://localhost:12345/callback")
		tokenForm.Set("client_id", clientID)
		tokenForm.Set("code_verifier", codeVerifier)

		tokenResp, err := client.PostForm(ts.URL+"/token", tokenForm)
		if err != nil {
			t.Fatalf("POST /token failed: %v", err)
		}
		defer tokenResp.Body.Close()

		if tokenResp.StatusCode != http.StatusOK {
			body, _ := ioutil.ReadAll(tokenResp.Body)
			t.Fatalf("expected 200, got %d: %s", tokenResp.StatusCode, string(body))
		}

		var tokenData map[string]interface{}
		if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
			t.Fatalf("failed to decode token response: %v", err)
		}
		accessToken = tokenData["access_token"].(string)
		if accessToken == "" {
			t.Fatal("empty access_token")
		}
	})

	// ---- Step 5: Use access token to POST / (initialize) ----
	t.Run("POST / initialize with OAuth token", func(t *testing.T) {
		// Trigger pool creation — need a warm process
		backendID := a.defaultBackendID()
		pool := a.getPoolForUser("test-user-1", backendID)
		if !pool.WaitForWarm(3 * time.Second) {
			t.Fatal("pool did not warm up")
		}

		body := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"opencode","version":"0.1.0"}}}`

		req, _ := http.NewRequest("POST", ts.URL+"/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST / failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("POST / returned 401 — token not accepted")
		}
		if resp.StatusCode == http.StatusNotFound {
			t.Fatal("POST / returned 404")
		}

		respBody, _ := ioutil.ReadAll(resp.Body)
		if len(respBody) == 0 {
			t.Fatal("expected response body from initialize, got empty")
		}

		// With cat backend, the response is echoed back.
		var rpc poolmgr.JSONRPCMessage
		if err := json.Unmarshal(respBody, &rpc); err != nil {
			t.Fatalf("response is not valid JSON: %v (body: %s)", err, string(respBody))
		}

		idStr := fmt.Sprintf("%v", rpc.ID)
		if idStr != "0" {
			t.Errorf("expected id=0, got id=%v", rpc.ID)
		}
	})

	// ---- Step 6: POST / tools/list ----
	t.Run("POST / tools/list with OAuth token", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`

		req, _ := http.NewRequest("POST", ts.URL+"/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST / failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("POST / returned 401")
		}

		respBody, _ := ioutil.ReadAll(resp.Body)
		if len(respBody) == 0 {
			t.Fatal("expected response body from tools/list, got empty")
		}

		var rpc poolmgr.JSONRPCMessage
		if err := json.Unmarshal(respBody, &rpc); err != nil {
			t.Fatalf("response is not valid JSON: %v (body: %s)", err, string(respBody))
		}

		idStr := fmt.Sprintf("%v", rpc.ID)
		if idStr != "1" {
			t.Errorf("expected id=1, got id=%v", rpc.ID)
		}
	})
}

// TestIntegration_MultipleRoundsRefillStability verifies pool refill stability.
func TestIntegration_MultipleRoundsRefillStability(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "cat", 2, 3*time.Second)
	defer cleanup()

	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				req := authRequest("GET", "/", "", token)
				req = req.WithContext(ctx)
				req.Header.Set("Accept", "text/event-stream")

				w := httptest.NewRecorder()
				a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)
			}()
		}
		wg.Wait()

		// Wait for pool to refill between rounds
		if !pool.WaitForWarm(3 * time.Second) {
			t.Errorf("round %d: pool did not refill", round)
			continue
		}

		time.Sleep(200 * time.Millisecond)

		if pool.WarmCount() != 2 {
			t.Errorf("round %d: expected 2 warm processes, got %d", round, pool.WarmCount())
		}
		if pool.ActiveCount() != 0 {
			t.Errorf("round %d: expected 0 active, got %d", round, pool.ActiveCount())
		}
	}
}

func TestIntegration_SSESingleProcessPerConnection(t *testing.T) {
	a, token, pool, cleanup := testAppWithPool(t, "yes", 2, 2*time.Second)
	defer cleanup()

	ctx1, cancel1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel1()

	req1 := authRequest("GET", "/", "", token)
	req1 = req1.WithContext(ctx1)
	w1 := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w1, req1)
	}()

	time.Sleep(50 * time.Millisecond)

	activeCount := pool.ActiveCount()
	if activeCount != 1 {
		t.Errorf("expected 1 active session during connection, got %d", activeCount)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	activeCount = pool.ActiveCount()
	if activeCount != 0 {
		t.Errorf("expected 0 active sessions after disconnect, got %d", activeCount)
	}
}

// ---------- Live reload: OnBackendChange wiring ----------

// testWebLogin logs into the web UI and returns a session cookie.
func testWebLogin(t *testing.T, mux *http.ServeMux, email, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"email": {email}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("web login: expected 303, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "mcp_session" {
			return c
		}
	}
	t.Fatal("web login: no session cookie")
	return nil
}

func TestIntegration_LiveReload_EditBackendTearsDownPools(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Create a backend in the DB.
	b := &store.Backend{
		ID: "live-be", Command: "cat", PoolSize: 1, ToolPrefix: "live",
		Env: "{}", Enabled: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	// Create a user pool for this backend so we can verify it gets torn down.
	env := a.toolMuxer.BuildEnvForUser("test-user-1", "live-be")
	pool := a.poolManager.GetOrCreateUserPool("live-be", "test-user-1", "cat", 1, 1, env)
	if pool == nil {
		t.Fatal("expected pool to be created")
	}
	initialPoolCount := a.poolManager.PoolCount()
	if initialPoolCount < 1 {
		t.Fatalf("expected at least 1 pool, got %d", initialPoolCount)
	}

	// Seed an admin user for the web UI.
	admin := &store.User{
		Name: "Admin", Email: "admin@live.test", Password: "pw", Role: "admin",
	}
	if err := a.store.CreateUser(admin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Set up web handler with OnBackendChange wired to muxer+pool manager.
	wh, err := web.NewHandler(a.store, "templates")
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	wh.OnBackendChange = func(backendID string) {
		a.toolMuxer.RefreshPrefixes()
		a.poolManager.RemovePoolsByBackend(backendID)
	}

	httpMux := http.NewServeMux()
	wh.Register(httpMux)

	cookie := testWebLogin(t, httpMux, "admin@live.test", "pw")

	// Edit the backend via the web UI.
	form := url.Values{
		"id":            {"live-be"},
		"command":       {"echo updated"},
		"min_pool_size": {"2"},
		"max_pool_size": {"2"},
		"tool_prefix":   {"live"},
		"env":           {"{}"},
		"enabled":       {"on"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/admin/backends/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("edit: expected 303, got %d", w.Code)
	}

	// Verify the user pool was torn down.
	afterPoolCount := a.poolManager.PoolCount()
	if afterPoolCount >= initialPoolCount {
		t.Errorf("expected pools to decrease after edit: before=%d, after=%d", initialPoolCount, afterPoolCount)
	}

	// Verify the backend was updated in DB.
	updated, err := a.store.GetBackend("live-be")
	if err != nil {
		t.Fatalf("GetBackend: %v", err)
	}
	if updated.Command != "echo updated" {
		t.Errorf("expected command 'echo updated', got %q", updated.Command)
	}
	if updated.PoolSize != 2 {
		t.Errorf("expected pool_size 2, got %d", updated.PoolSize)
	}
}

func TestIntegration_LiveReload_DeleteBackendTearsDownPools(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Create a backend and user pool.
	b := &store.Backend{
		ID: "del-live", Command: "cat", PoolSize: 1, ToolPrefix: "dl",
		Env: "{}", Enabled: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}
	env := a.toolMuxer.BuildEnvForUser("test-user-1", "del-live")
	a.poolManager.GetOrCreateUserPool("del-live", "test-user-1", "cat", 1, 1, env)

	admin := &store.User{
		Name: "Admin", Email: "admin@del.test", Password: "pw", Role: "admin",
	}
	a.store.CreateUser(admin)

	wh, _ := web.NewHandler(a.store, "templates")
	wh.OnBackendChange = func(backendID string) {
		a.toolMuxer.RefreshPrefixes()
		a.poolManager.RemovePoolsByBackend(backendID)
	}

	httpMux := http.NewServeMux()
	wh.Register(httpMux)
	cookie := testWebLogin(t, httpMux, "admin@del.test", "pw")

	beforeCount := a.poolManager.PoolCount()

	form := url.Values{"id": {"del-live"}}
	req := httptest.NewRequest(http.MethodPost, "/web/admin/backends/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	httpMux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete: expected 303, got %d", w.Code)
	}

	afterCount := a.poolManager.PoolCount()
	if afterCount >= beforeCount {
		t.Errorf("expected pool count to decrease after delete: before=%d, after=%d", beforeCount, afterCount)
	}

	// Backend should be gone from DB.
	_, err := a.store.GetBackend("del-live")
	if err == nil {
		t.Error("expected backend to be deleted from DB")
	}
}

// TestIntegration_InlineMCPWithAPIKey tests the inline MCP protocol flow with an API key.
// This tests the case where there are no real backends configured (or only mcpbridge system backend).
func TestIntegration_InlineMCPWithAPIKey(t *testing.T) {
	// Create a test app WITHOUT any backends (only mcpbridge system backend via migration)
	a, apiKey, cleanup := testAppNoBackends(t)
	defer cleanup()

	// Test 1: initialize
	t.Run("initialize returns inline capabilities", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		w := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		if resp["jsonrpc"] != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		serverInfo, ok := result["serverInfo"].(map[string]interface{})
		if !ok {
			t.Fatal("expected serverInfo in result")
		}
		if serverInfo["name"] != "mcp-bridge" {
			t.Errorf("expected server name mcp-bridge, got %v", serverInfo["name"])
		}
	})

	// Test 2: tools/list returns mcpbridge tools
	t.Run("tools/list returns inline tools", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		w := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		tools, ok := result["tools"].([]interface{})
		if !ok {
			t.Fatal("expected tools in result")
		}

		// Should have mcpbridge tools
		if len(tools) == 0 {
			t.Fatal("expected at least one tool")
		}

		// Verify mcpbridge_ping exists
		found := false
		for _, tt := range tools {
			tool := tt.(map[string]interface{})
			if tool["name"] == "mcpbridge_ping" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected mcpbridge_ping tool")
		}
	})

	// Test 3: tools/call with mcpbridge_ping
	t.Run("tools/call mcpbridge_ping works", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":2,"params":{"name":"mcpbridge_ping"}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		w := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		if result["status"] != "ok" {
			t.Errorf("expected status ok, got %v", result["status"])
		}
	})

	// Test 4: tools/call with mcpbridge_list_backends
	t.Run("tools/call mcpbridge_list_backends works", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":3,"params":{"name":"mcpbridge_list_backends"}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		w := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		if result["status"] != "ok" {
			t.Errorf("expected status ok, got %v", result["status"])
		}
	})

	// Test 5: tools/call with mcpbridge_capabilities
	t.Run("tools/call mcpbridge_capabilities works", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/call","id":4,"params":{"name":"mcpbridge_capabilities"}}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		w := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		// Check for content (the tool should return text content)
		content, ok := result["content"].([]interface{})
		if !ok || len(content) == 0 {
			t.Fatal("expected content in result")
		}
		textContent, ok := content[0].(map[string]interface{})
		if !ok {
			t.Fatal("expected text content")
		}
		text, ok := textContent["text"].(string)
		if !ok {
			t.Fatal("expected text string")
		}
		// Verify the output contains expected information
		if !strings.Contains(text, "MCP Bridge Capabilities") {
			t.Errorf("expected output to contain 'MCP Bridge Capabilities', got: %s", text)
		}
		if !strings.Contains(text, "Bridge Admin") {
			t.Errorf("expected output to contain 'Bridge Admin', got: %s", text)
		}
		if !strings.Contains(text, "mcpbridge_") {
			t.Errorf("expected output to contain 'mcpbridge_', got: %s", text)
		}
	})
}

// testAppNoBackends creates a test app with no real backends in the database.
// Only the mcpbridge system backend will exist (via migration).
func testAppNoBackends(t *testing.T) (a *app, apiKey string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	st, err := store.New(dbPath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to open test db: %v", err)
	}

	cfg := &config.InternalConfig{
		Server:   config.ServerConfig{Port: "0", LogLevel: "info"},
		Backends: map[string]config.BackendConfig{},
	}

	pm := poolmgr.NewPoolManagerWithGC("cat", 1, 15*time.Minute)
	tm := muxer.NewToolMuxerWithStore(pm, st, cfg)

	ah := &auth.Handler{
		Store:    st,
		Issuer:   "http://localhost:0",
		CodeTTL:  10 * time.Minute,
		TokenTTL: 1 * time.Hour,
	}

	a = &app{
		store:       st,
		auth:        ah,
		poolManager: pm,
		toolMuxer:   tm,
		config:      cfg,
	}

	// Run migration to create mcpbridge system backend
	if err := st.MigrateDefaultBackend(); err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to migrate: %v", err)
	}

	// Seed a test user.
	user := &store.User{
		ID:       "test-user-inline",
		Name:     "Test User",
		Email:    "test-inline@example.com",
		Password: "password123",
	}
	if err := st.CreateUser(user); err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to seed user: %v", err)
	}

	// Create an API key
	key, hash, err := store.GenerateAPIKey()
	if err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to generate API key: %v", err)
	}
	apiKey = key

	apiKeyRecord := &store.APIKey{
		UserID:  user.ID,
		Name:    "Test Key",
		KeyHash: hash,
	}
	if err := st.CreateAPIKey(apiKeyRecord); err != nil {
		st.Close()
		os.RemoveAll(dir)
		t.Fatalf("failed to create API key: %v", err)
	}

	cleanup = func() {
		pm.ShutdownAll()
		st.Close()
		os.RemoveAll(dir)
	}

	return a, apiKey, cleanup
}
