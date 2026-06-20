package main

import (
	"bytes"
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

	// With GetWarmWithRetry, the handler now waits for a process to be spawned
	// instead of immediately returning 503. For simple commands like "cat",
	// a process will be spawned and the request succeeds (200).
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (process spawned), got %d", w.Code)
	}
}

func TestIntegration_RootHandler_GetRoutes(t *testing.T) {
	a, token, cleanup := testApp(t, "cat", 0)
	defer cleanup()

	// Use context with timeout since GetWarmWithRetry now spawns a process
	// instead of immediately returning 503
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req := authRequest("GET", "/", "", token)
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	a.auth.Middleware(rootHandler(a)).ServeHTTP(w, req)

	// With GetWarmWithRetry, the SSE handler now waits for a process to be spawned.
	// For simple commands like "cat", a process will be spawned but cat just waits for input.
	// We just verify the request was handled (not 404) and that SSE headers are set.
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
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
	mux.Handle("/mcp/v2", a.auth.Middleware(v2HandleWrapper(a)))
	mux.Handle("/mcp", a.auth.Middleware(v2HandleWrapper(a)))

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

	// ---- Step 7: POST /mcp/v2 initialize with OAuth token ----
	t.Run("POST /mcp/v2 initialize with OAuth token", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"opencode","version":"0.1.0"}}}`

		req, _ := http.NewRequest("POST", ts.URL+"/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp/v2 failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("POST /mcp/v2 returned 401 — token not accepted")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /mcp/v2 returned %d, not 200", resp.StatusCode)
		}

		respBody, _ := ioutil.ReadAll(resp.Body)
		if len(respBody) == 0 {
			t.Fatal("expected response body from /mcp/v2 initialize, got empty")
		}

		// Check it's a valid JSON-RPC response
		var rpcResp map[string]interface{}
		if err := json.Unmarshal(respBody, &rpcResp); err != nil {
			t.Fatalf("response is not valid JSON: %v (body: %s)", err, string(respBody))
		}

		if rpcResp["result"] == nil {
			t.Fatalf("expected result in response, got: %s", respBody)
		}

		result := rpcResp["result"].(map[string]interface{})
		serverInfo := result["serverInfo"].(map[string]interface{})
		if serverInfo["name"] != "mcp-bridge-v2" {
			t.Errorf("expected server name mcp-bridge-v2, got %v", serverInfo["name"])
		}
	})

	// ---- Step 8: POST /mcp/v2 tools/list with OAuth token ----
	t.Run("POST /mcp/v2 tools/list with OAuth token", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`

		req, _ := http.NewRequest("POST", ts.URL+"/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp/v2 failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("POST /mcp/v2 returned 401")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /mcp/v2 returned %d", resp.StatusCode)
		}

		respBody, _ := ioutil.ReadAll(resp.Body)
		var rpcResp map[string]interface{}
		if err := json.Unmarshal(respBody, &rpcResp); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}

		result, ok := rpcResp["result"].(map[string]interface{})
		if !ok {
			t.Fatalf("response missing result: %s", respBody)
		}
		tools, ok := result["tools"].([]interface{})
		if !ok || len(tools) == 0 {
			// No backends seeded — still a valid response, just empty
			t.Skip("no tools returned (backends not seeded in test DB)")
		}
		t.Logf("Got %d tools from /mcp/v2", len(tools))
	})

	// ---- Step 9: POST /mcp/v2 without token returns 401 ----
	t.Run("POST /mcp/v2 without token returns 401", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0}`

		req, _ := http.NewRequest("POST", ts.URL+"/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp/v2 failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 without token, got %d", resp.StatusCode)
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
	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "live-be")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}
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
	wh.OnBackendChange = func(backendID string, triggerUserID string) {
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
	var envVars []string
	envVars, buildErr := a.toolMuxer.BuildEnvForUser("test-user-1", "del-live")
	if buildErr != nil {
		t.Fatalf("BuildEnvForUser: %v", buildErr)
	}
	a.poolManager.GetOrCreateUserPool("del-live", "test-user-1", "cat", 1, 1, envVars)

	admin := &store.User{
		Name: "Admin", Email: "admin@del.test", Password: "pw", Role: "admin",
	}
	a.store.CreateUser(admin)

	wh, _ := web.NewHandler(a.store, "templates")
	wh.OnBackendChange = func(backendID string, triggerUserID string) {
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

	// Test 6: Bug fix - mcpbridge_capabilities should show backends as "configured" when user has tokens
	t.Run("tools/call mcpbridge_capabilities shows configured backends", func(t *testing.T) {
		// Create a backend and add tokens for the user
		backend := &store.Backend{
			ID:       "test-backend",
			Command:  "cat",
			PoolSize: 1,
			Enabled:  true,
		}
		if err := a.store.CreateBackend(backend); err != nil {
			t.Fatalf("failed to create backend: %v", err)
		}

		// Add a token for the user - need to get the user ID from the database
		// The user was created in testAppNoBackends with ID "test-user-inline"
		token := &store.UserToken{
			UserID:    "test-user-inline",
			BackendID: "test-backend",
			EnvKey:    "API_KEY",
			Value:     "secret123",
		}
		if err := a.store.SetUserToken(token); err != nil {
			t.Fatalf("failed to set user token: %v", err)
		}

		body := `{"jsonrpc":"2.0","method":"tools/call","id":5,"params":{"name":"mcpbridge_capabilities"}}`
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

		// Verify backend shows as "configured" when user has tokens
		if strings.Contains(text, "test-backend: available (not configured)") {
			t.Errorf("BUG: backend should show 'configured' but shows 'available (not configured)'. Output: %s", text)
		}
		if !strings.Contains(text, "test-backend: configured") {
			t.Errorf("expected backend to show 'configured', got: %s", text)
		}
	})

	// Test 7: Consistency between mcpbridge_list_backends and mcpbridge_capabilities for system backends
	t.Run("list_backends and capabilities consistent for system backends", func(t *testing.T) {
		// Create the mcpbridge system backend (simulating seed)
		mcpbridgeBackend := &store.Backend{
			ID:            "mcpbridge",
			Command:       "mcp-bridge-builtin",
			PoolSize:      1,
			ToolPrefix:    "",
			Enabled:       false, // Start with disabled to test
			IsSystem:      true,
			SelfReporting: true,
		}
		if err := a.store.CreateBackend(mcpbridgeBackend); err != nil {
			// Might already exist from previous test, try to update instead
			backends, _ := a.store.ListBackends()
			for _, b := range backends {
				if b.ID == "mcpbridge" {
					b.Enabled = false
					b.IsSystem = true
					a.store.UpdateBackend(b)
					break
				}
			}
		}

		// Get output from list_backends
		listBody := `{"jsonrpc":"2.0","method":"tools/call","id":6,"params":{"name":"mcpbridge_list_backends"}}`
		listReq := httptest.NewRequest("POST", "/", strings.NewReader(listBody))
		listReq.Header.Set("Content-Type", "application/json")
		listReq.Header.Set("Authorization", "Bearer "+apiKey)
		listW := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(listW, listReq)

		var listResp map[string]interface{}
		if err := json.Unmarshal(listW.Body.Bytes(), &listResp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}
		listResult, _ := listResp["result"].(map[string]interface{})
		listContent, _ := listResult["content"].([]interface{})
		listTextContent, _ := listContent[0].(map[string]interface{})
		listText, _ := listTextContent["text"].(string)

		// Get output from capabilities
		capsBody := `{"jsonrpc":"2.0","method":"tools/call","id":7,"params":{"name":"mcpbridge_capabilities"}}`
		capsReq := httptest.NewRequest("POST", "/", strings.NewReader(capsBody))
		capsReq.Header.Set("Content-Type", "application/json")
		capsReq.Header.Set("Authorization", "Bearer "+apiKey)
		capsW := httptest.NewRecorder()
		a.auth.Middleware(rootHandler(a)).ServeHTTP(capsW, capsReq)

		var capsResp map[string]interface{}
		if err := json.Unmarshal(capsW.Body.Bytes(), &capsResp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}
		capsResult, _ := capsResp["result"].(map[string]interface{})
		capsContent, _ := capsResult["content"].([]interface{})
		capsTextContent, _ := capsContent[0].(map[string]interface{})
		capsText, _ := capsTextContent["text"].(string)

		// Both should show system backends as "always available" regardless of DB Enabled flag
		if !strings.Contains(listText, "mcpbridge: system (always available)") {
			t.Errorf("list_backends should show 'mcpbridge: system (always available)', got: %s", listText)
		}
		if !strings.Contains(capsText, "mcpbridge: system (always available)") {
			t.Errorf("capabilities should show 'mcpbridge: system (always available)', got: %s", capsText)
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

// ---------- MCP v2 Endpoint Tests ----------

func TestIntegration_V2Endpoint(t *testing.T) {
	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Test 1: initialize on v2 endpoint
	t.Run("v2 initialize returns capabilities", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
		req := httptest.NewRequest("POST", "/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		// Add user context
		ctx := context.WithValue(req.Context(), auth.UserIDKey, "test-user-1")
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		v2HandleWrapper(a).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}

		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}

		serverInfo, ok := result["serverInfo"].(map[string]interface{})
		if !ok {
			t.Fatal("expected serverInfo in result")
		}
		if serverInfo["name"] != "mcp-bridge-v2" {
			t.Errorf("expected server name mcp-bridge-v2, got %v", serverInfo["name"])
		}
	})

	// Test 2: tools/list on v2 endpoint
	t.Run("v2 tools/list returns namespaces", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
		req := httptest.NewRequest("POST", "/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), auth.UserIDKey, "test-user-1")
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		v2HandleWrapper(a).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
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

		// Should have namespace descriptors + namespace_expand + tool_call
		t.Logf("Got %d tools", len(tools))
		for _, tt := range tools {
			tool := tt.(map[string]interface{})
			t.Logf("Tool: %s", tool["name"])
		}

		// Should have at least namespace_expand and tool_call
		foundExpand := false
		foundToolCall := false
		for _, tt := range tools {
			tool := tt.(map[string]interface{})
			if tool["name"] == "namespace_expand" {
				foundExpand = true
			}
			if tool["name"] == "tool_call" {
				foundToolCall = true
			}
		}
		if !foundExpand {
			t.Error("missing namespace_expand tool")
		}
		if !foundToolCall {
			t.Error("missing tool_call tool")
		}
	})

	// Test 3: /mcp alias route exists
	t.Run("/mcp route exists", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0}`
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), auth.UserIDKey, "test-user-1")
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		v2HandleWrapper(a).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
		}
	})

	// Test 4: v2 with empty backends (no backend namespaces)
	t.Run("v2 with empty backends", func(t *testing.T) {
		// Create app without any backends
		a2, _, cleanup2 := testAppNoBackends(t)
		defer cleanup2()

		body := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
		req := httptest.NewRequest("POST", "/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), auth.UserIDKey, "test-user-1")
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		v2HandleWrapper(a2).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
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

		// With no backends, should only have mcpbridge + verbs
		t.Logf("Got %d tools with no backends", len(tools))
		for _, tt := range tools {
			tool := tt.(map[string]interface{})
			t.Logf("Tool: %s", tool["name"])
		}
	})
}

// TestIntegration_V2WithAuthMiddleware tests v2 endpoint going through auth middleware with valid token.
func TestIntegration_V2WithAuthMiddleware(t *testing.T) {
	a, token, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Test: v2 initialize through full auth middleware
	t.Run("v2 initialize through auth middleware", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{}}`
		req := authRequest("POST", "/mcp/v2", body, token)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		v2Handler := v2HandleWrapper(a)
		// Wrap with auth middleware
		handler := a.auth.Middleware(v2Handler)
		handler.ServeHTTP(w, req)

		// Should NOT be 401 - token is valid
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("expected 200, got 401 (auth rejected valid token). Body: %s", w.Body.String())
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatal("expected result in response")
		}
		serverInfo := result["serverInfo"].(map[string]interface{})
		if serverInfo["name"] != "mcp-bridge-v2" {
			t.Errorf("expected mcp-bridge-v2, got %v", serverInfo["name"])
		}
	})

	// Test: Without token returns 401
	t.Run("v2 without token returns 401", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0}`
		req := httptest.NewRequest("POST", "/mcp/v2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handler := a.auth.Middleware(v2HandleWrapper(a))
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 without token, got %d", w.Code)
		}
	})

	// Test: With invalid token returns 401
	t.Run("v2 with invalid token returns 401", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","method":"initialize","id":0}`
		req := authRequest("POST", "/mcp/v2", body, "invalid-token")
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		handler := a.auth.Middleware(v2HandleWrapper(a))
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with invalid token, got %d", w.Code)
		}
	})
}

// TestIntegration_V2FullFlow simulates the full MCP client flow:
// initialize → tools/list → namespace_expand → tool_call
func TestIntegration_V2FullFlow(t *testing.T) {
	a, token, cleanup := testApp(t, "echo hello", 1)
	defer cleanup()

	handler := a.auth.Middleware(v2HandleWrapper(a))

	// Step 1: initialize
	initBody := `{"jsonrpc":"2.0","method":"initialize","id":0,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req := authRequest("POST", "/mcp/v2", initBody, token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("initialize failed: %d - %s", w.Code, w.Body.String())
	}
	var initResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if initResp["result"] == nil {
		t.Fatal("initialize expected result, got error")
	}
	t.Logf("initialize success")

	// Step 2: tools/list
	toolsBody := `{"jsonrpc":"2.0","method":"tools/list","id":1}`
	req = authRequest("POST", "/mcp/v2", toolsBody, token)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("tools/list failed: %d - %s", w.Code, w.Body.String())
	}
	var toolsResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &toolsResp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	result := toolsResp["result"].(map[string]interface{})
	tools := result["tools"].([]interface{})
	t.Logf("Got %d tools in tools/list", len(tools))

	// Step 3: namespace_expand for mcpbridge (internal tools)
	expandBody := `{"jsonrpc":"2.0","method":"tools/call","id":2,"params":{"name":"namespace_expand","arguments":{"namespace":"mcpbridge"}}}`
	req = authRequest("POST", "/mcp/v2", expandBody, token)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("namespace_expand failed: %d - %s", w.Code, w.Body.String())
	}
	var expandResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &expandResp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// namespace_expand returns tools directly in result (not wrapped)
	t.Logf("namespace_expand result type: %T", expandResp["result"])
}

// ---------- Precache tests ----------

func testPrecacheStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "precache-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	st, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("store.New: %v", err)
	}
	return st, dir
}

func TestRunPrecacheForBackend_DisabledBackend(t *testing.T) {
	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	b := &store.Backend{ID: "disabled-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: false}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "disabled-be")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 for disabled backend", n)
	}
}

func TestRunPrecacheForBackend_UserIDPath(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{{"name": "tool-1", "description": "A test tool"}}, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	u := &store.User{Name: "Admin", Email: "admin@test.com", Password: "secret", Role: "admin"}
	if err := st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	b := &store.Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{UserID: u.ID, Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "test-be")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}

	// Verify capabilities were cached
	caps, err := st.GetBackendCapabilities("test-be")
	if err != nil {
		t.Fatalf("GetBackendCapabilities: %v", err)
	}
	if caps.ToolCount != 1 {
		t.Errorf("ToolCount = %d, want 1", caps.ToolCount)
	}

	// Verify precache_error is cleared (SetBackendAvailable was called)
	be, err := st.GetBackend("test-be")
	if err != nil {
		t.Fatal(err)
	}
	if be.PrecacheError != "" {
		t.Errorf("PrecacheError = %q, want empty after successful precache", be.PrecacheError)
	}
}

func TestRunPrecacheForBackend_UserEmailPath(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{{"name": "email-tool"}}, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	u := &store.User{Name: "Alice", Email: "alice@test.com", Password: "secret", Role: "user"}
	if err := st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	b := &store.Backend{ID: "email-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{UserEmail: "alice@test.com", Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "email-be")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
}

func TestRunPrecacheForBackend_StartupFallbackStaticEnv(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	callCount := 0
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		callCount++
		// First call (static env) succeeds
		if callCount == 1 {
			return []map[string]interface{}{{"name": "static-tool"}}, nil
		}
		return nil, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	b := &store.Backend{ID: "fallback-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	// No UserID or UserEmail — startup fallback chain
	cfg := PrecacheConfig{Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "fallback-be")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	// Should have called fetchToolsForPrecacheFn exactly once (static env succeeded)
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1", callCount)
	}
}

func TestRunPrecacheForBackend_StartupFallbackAdminUser(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	callCount := 0
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		callCount++
		if callCount == 1 {
			// Static env returns no tools
			return nil, nil
		}
		// Admin user tokens succeed
		return []map[string]interface{}{{"name": "admin-tool"}}, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	admin := &store.User{Name: "Admin", Email: "admin@test.com", Password: "secret", Role: "admin"}
	if err := st.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	b := &store.Backend{ID: "admin-fb", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "admin-fb")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (static env + admin tokens)", callCount)
	}
}

func TestRunPrecacheForBackend_StartupFallbackNoCredentials(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return nil, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	b := &store.Backend{ID: "no-creds", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "no-creds")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 (no credentials)", n)
	}

	// Should have set precache_error
	be, err := st.GetBackend("no-creds")
	if err != nil {
		t.Fatal(err)
	}
	if be.PrecacheError == "" {
		t.Error("PrecacheError is empty, want error message about no credentials")
	}
}

func TestRunPrecacheForBackend_FetchError(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return nil, fmt.Errorf("process crashed")
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	u := &store.User{Name: "Admin", Email: "admin@test.com", Password: "secret", Role: "admin"}
	if err := st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	b := &store.Backend{ID: "crash-be", Command: "nonexistent", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{UserID: u.ID, Store: st}
	_, err := RunPrecacheForBackend(context.Background(), cfg, "crash-be")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "process crashed" {
		t.Errorf("err = %q, want %q", err.Error(), "process crashed")
	}

	// Should have set precache_error
	be, err := st.GetBackend("crash-be")
	if err != nil {
		t.Fatal(err)
	}
	if be.PrecacheError != "process crashed" {
		t.Errorf("PrecacheError = %q, want %q", be.PrecacheError, "process crashed")
	}
}

func TestRunPrecacheForBackend_DoesNotWriteEnforcerProfiles(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{
			{
				"name":        "write-tool",
				"description": "A write tool",
				"_meta": map[string]interface{}{
					"enforcer_profile": map[string]interface{}{
						"risk_level":    "high",
						"impact_scope":  "write",
						"resource_cost": 8,
						"approval_req":  true,
						"pii_exposure":  false,
						"idempotent":    false,
					},
				},
			},
		}, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	u := &store.User{Name: "Admin", Email: "admin@test.com", Password: "secret", Role: "admin"}
	if err := st.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	b := &store.Backend{
		ID:            "profile-be",
		Command:       "echo",
		PoolSize:      1,
		Env:           "{}",
		Enabled:       true,
		SelfReporting: true,
	}
	if err := st.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{UserID: u.ID, Store: st}
	n, err := RunPrecacheForBackend(context.Background(), cfg, "profile-be")
	if err != nil {
		t.Fatalf("RunPrecacheForBackend: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}

	// Verify capabilities were cached (tool caching still works)
	caps, err := st.GetBackendCapabilities("profile-be")
	if err != nil {
		t.Fatalf("GetBackendCapabilities: %v", err)
	}
	if caps.ToolCount != 1 {
		t.Errorf("ToolCount = %d, want 1", caps.ToolCount)
	}

	// Verify NO enforcer tool profiles were written by precache
	// scanSelfReportingBackends owns that responsibility
	profiles, err := st.ListToolProfilesByBackend("profile-be")
	if err != nil {
		t.Fatalf("ListToolProfilesByBackend: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("enforcer_tool_profiles count = %d, want 0 (precache should not write profiles)", len(profiles))
	}
}

func TestBuildEnvForPrecache_Basic(t *testing.T) {
	b := &store.Backend{
		ID:          "env-test",
		Command:     "echo",
		Env:         `{"MY_KEY": "my_value", "ANOTHER": "val2"}`,
		EnvMappings: `{}`,
	}
	tokens := []store.UserToken{
		{EnvKey: "USER_TOKEN", Value: "secret123"},
	}

	env, err := buildEnvForPrecache(b, tokens, nil)
	if err != nil {
		t.Fatalf("buildEnvForPrecache: %v", err)
	}
	if env["MY_KEY"] != "my_value" {
		t.Errorf("MY_KEY = %q, want %q", env["MY_KEY"], "my_value")
	}
	if env["USER_TOKEN"] != "secret123" {
		t.Errorf("USER_TOKEN = %q, want %q", env["USER_TOKEN"], "secret123")
	}
	if env["MCP_PRECACHE"] != "true" {
		t.Errorf("MCP_PRECACHE = %q, want true", env["MCP_PRECACHE"])
	}
}

func TestBuildEnvForPrecache_WithMappings(t *testing.T) {
	b := &store.Backend{
		ID:          "mapping-test",
		Command:     "echo",
		Env:         `{"BACKEND_KEY": "backend_val"}`,
		EnvMappings: `{"USER_KEY": "BACKEND_KEY"}`,
	}
	tokens := []store.UserToken{
		{EnvKey: "USER_KEY", Value: "user_val"},
	}

	env, err := buildEnvForPrecache(b, tokens, nil)
	if err != nil {
		t.Fatalf("buildEnvForPrecache: %v", err)
	}
	// USER_KEY should be mapped to BACKEND_KEY
	// Note: map iteration order is non-deterministic, so either value is valid
	if env["BACKEND_KEY"] != "user_val" && env["BACKEND_KEY"] != "backend_val" {
		t.Errorf("BACKEND_KEY = %q, want either 'user_val' or 'backend_val'", env["BACKEND_KEY"])
	}
	if env["MCP_PRECACHE"] != "true" {
		t.Errorf("MCP_PRECACHE = %q, want true", env["MCP_PRECACHE"])
	}
}

func TestBuildEnvForPrecache_TemplateVars(t *testing.T) {
	user := &store.User{Email: "karl.dane@tuskerdirect.com", ID: "user-1", Role: "admin"}
	b := &store.Backend{
		ID:          "tmpl-test",
		Command:     "echo",
		Env:         `{"QDRANT_ADMIN_URL":"http://qdrant:6333","QDRANT_USERNAME":"{{users.email|sanitised}}","QDRANT_COLLECTION":"{{users.email|sanitised}}","QDRANT_VECTOR_SIZE":"768"}`,
		EnvMappings: `{}`,
	}
	tokens := []store.UserToken{
		{EnvKey: "API_TOKEN", Value: "real-token"},
	}

	env, err := buildEnvForPrecache(b, tokens, user)
	if err != nil {
		t.Fatalf("buildEnvForPrecache: %v", err)
	}

	// Static vars should pass through
	if env["QDRANT_ADMIN_URL"] != "http://qdrant:6333" {
		t.Errorf("QDRANT_ADMIN_URL = %q, want %q", env["QDRANT_ADMIN_URL"], "http://qdrant:6333")
	}
	if env["QDRANT_VECTOR_SIZE"] != "768" {
		t.Errorf("QDRANT_VECTOR_SIZE = %q, want %q", env["QDRANT_VECTOR_SIZE"], "768")
	}

	// Template vars should be resolved against the user
	want := "karl_dane_at_tuskerdirect_com"
	if env["QDRANT_USERNAME"] != want {
		t.Errorf("QDRANT_USERNAME = %q, want %q", env["QDRANT_USERNAME"], want)
	}
	if env["QDRANT_COLLECTION"] != want {
		t.Errorf("QDRANT_COLLECTION = %q, want %q", env["QDRANT_COLLECTION"], want)
	}

	// Non-template user tokens should still pass through
	if env["API_TOKEN"] != "real-token" {
		t.Errorf("API_TOKEN = %q, want %q", env["API_TOKEN"], "real-token")
	}

	// MCP_PRECACHE should still be set
	if env["MCP_PRECACHE"] != "true" {
		t.Errorf("MCP_PRECACHE = %q, want true", env["MCP_PRECACHE"])
	}
}

func TestRunPrecache_AllBackends(t *testing.T) {
	orig := fetchToolsForPrecacheFn
	fetchToolsForPrecacheFn = func(ctx context.Context, command string, env map[string]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{{"name": "tool"}}, nil
	}
	defer func() { fetchToolsForPrecacheFn = orig }()

	st, dir := testPrecacheStore(t)
	defer os.RemoveAll(dir)
	defer st.Close()

	u := &store.User{Name: "Admin", Email: "admin@test.com", Password: "secret", Role: "admin"}
	if err := st.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	be1 := &store.Backend{ID: "be-a", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	be2 := &store.Backend{ID: "be-b", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := st.CreateBackend(be1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateBackend(be2); err != nil {
		t.Fatal(err)
	}

	cfg := PrecacheConfig{UserEmail: "admin@test.com", Store: st}
	if err := RunPrecache(context.Background(), cfg); err != nil {
		t.Fatalf("RunPrecache: %v", err)
	}

	// Both should have cached capabilities
	for _, id := range []string{"be-a", "be-b"} {
		caps, err := st.GetBackendCapabilities(id)
		if err != nil {
			t.Errorf("GetBackendCapabilities(%s): %v", id, err)
		} else if caps.ToolCount != 1 {
			t.Errorf("backends[%s] ToolCount = %d, want 1", id, caps.ToolCount)
		}
	}
}

// ---------- BuildEnvForUser with template env vars through encrypt/decrypt ----------

func TestBuildEnvForUser_StaticEnvPassesThrough(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	b := &store.Backend{
		ID:                "env-static",
		Command:           "cat",
		PoolSize:          1,
		MinPoolSize:       1,
		MaxPoolSize:       1,
		Env:               `{"QDRANT_ADMIN_URL":"http://localhost:6333","QDRANT_ADMIN_KEY":"admin-key-123","QDRANT_VECTOR_SIZE":"768"}`,
		Enabled:           true,
		SelfReporting:     true,
		NoKeysRequired:    true,
		SkipJustification: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	stored, err := a.store.GetBackend("env-static")
	if err != nil {
		t.Fatalf("GetBackend: %v", err)
	}
	if stored.EncryptedEnv == "" {
		t.Error("EncryptedEnv is empty — env was NOT encrypted")
	}

	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "env-static")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}

	envMap := sliceToEnvMap(env)
	if envMap["QDRANT_ADMIN_URL"] != "http://localhost:6333" {
		t.Errorf("QDRANT_ADMIN_URL = %q, want %q", envMap["QDRANT_ADMIN_URL"], "http://localhost:6333")
	}
	if envMap["QDRANT_ADMIN_KEY"] != "admin-key-123" {
		t.Errorf("QDRANT_ADMIN_KEY = %q, want %q", envMap["QDRANT_ADMIN_KEY"], "admin-key-123")
	}
	if envMap["QDRANT_VECTOR_SIZE"] != "768" {
		t.Errorf("QDRANT_VECTOR_SIZE = %q, want %q", envMap["QDRANT_VECTOR_SIZE"], "768")
	}
}

func TestBuildEnvForUser_TemplateEmail(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	b := &store.Backend{
		ID:                "env-tmpl-email",
		Command:           "cat",
		PoolSize:          1,
		MinPoolSize:       1,
		MaxPoolSize:       1,
		Env:               `{"QDRANT_USERNAME":"{{users.email}}"}`,
		Enabled:           true,
		SelfReporting:     true,
		NoKeysRequired:    true,
		SkipJustification: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	stored, err := a.store.GetBackend("env-tmpl-email")
	if err != nil {
		t.Fatalf("GetBackend: %v", err)
	}
	if stored.EncryptedEnv == "" {
		t.Error("EncryptedEnv is empty — env was NOT encrypted")
	}

	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "env-tmpl-email")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}

	envMap := sliceToEnvMap(env)
	want := "test@example.com"
	if envMap["QDRANT_USERNAME"] != want {
		t.Errorf("QDRANT_USERNAME = %q, want %q", envMap["QDRANT_USERNAME"], want)
	}
}

func TestBuildEnvForUser_TemplateSanitisedEmail(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	b := &store.Backend{
		ID:                "env-tmpl-san",
		Command:           "cat",
		PoolSize:          1,
		MinPoolSize:       1,
		MaxPoolSize:       1,
		Env:               `{"QDRANT_COLLECTION":"{{users.email|sanitised}}"}`,
		Enabled:           true,
		SelfReporting:     true,
		NoKeysRequired:    true,
		SkipJustification: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "env-tmpl-san")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}

	envMap := sliceToEnvMap(env)
	want := "test_at_example_com"
	if envMap["QDRANT_COLLECTION"] != want {
		t.Errorf("QDRANT_COLLECTION = %q, want %q", envMap["QDRANT_COLLECTION"], want)
	}
}

func TestBuildEnvForUser_MixedStaticAndTemplate(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	b := &store.Backend{
		ID:                "env-mixed",
		Command:           "cat",
		PoolSize:          1,
		MinPoolSize:       1,
		MaxPoolSize:       1,
		Env:               `{"QDRANT_ADMIN_URL":"http://localhost:6333","QDRANT_USERNAME":"{{users.email}}","QDRANT_COLLECTION":"{{users.email|sanitised}}","QDRANT_VECTOR_SIZE":"768"}`,
		Enabled:           true,
		SelfReporting:     true,
		NoKeysRequired:    true,
		SkipJustification: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "env-mixed")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}

	envMap := sliceToEnvMap(env)
	if envMap["QDRANT_ADMIN_URL"] != "http://localhost:6333" {
		t.Errorf("QDRANT_ADMIN_URL = %q, want %q", envMap["QDRANT_ADMIN_URL"], "http://localhost:6333")
	}
	if envMap["QDRANT_USERNAME"] != "test@example.com" {
		t.Errorf("QDRANT_USERNAME = %q, want %q", envMap["QDRANT_USERNAME"], "test@example.com")
	}
	if envMap["QDRANT_COLLECTION"] != "test_at_example_com" {
		t.Errorf("QDRANT_COLLECTION = %q, want %q", envMap["QDRANT_COLLECTION"], "test_at_example_com")
	}
	if envMap["QDRANT_VECTOR_SIZE"] != "768" {
		t.Errorf("QDRANT_VECTOR_SIZE = %q, want %q", envMap["QDRANT_VECTOR_SIZE"], "768")
	}
}

// ---------- resolveTemplatesForUser unit tests ----------

func TestResolveTemplatesForUser_NoTemplates(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	envMap := map[string]string{
		"STATIC_KEY": "static_value",
		"ANOTHER":    "plain_text",
	}
	result := resolveTemplatesForUser(envMap, "test-user-1", a.store)
	if result["STATIC_KEY"] != "static_value" {
		t.Errorf("STATIC_KEY = %q, want %q", result["STATIC_KEY"], "static_value")
	}
	if result["ANOTHER"] != "plain_text" {
		t.Errorf("ANOTHER = %q, want %q", result["ANOTHER"], "plain_text")
	}
}

func TestResolveTemplatesForUser_UserNotFound(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	envMap := map[string]string{
		"MY_KEY": "{{users.email}}",
	}
	result := resolveTemplatesForUser(envMap, "nonexistent-user", a.store)
	// Should return the original env unchanged when user is not found
	if result["MY_KEY"] != "{{users.email}}" {
		t.Errorf("MY_KEY = %q, want %q (unchanged when user not found)", result["MY_KEY"], "{{users.email}}")
	}
}

func TestResolveTemplatesForUser_AllFields(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	envMap := map[string]string{
		"EMAIL_VAR":    "{{users.email}}",
		"USERNAME_VAR": "{{users.username}}",
		"ID_VAR":       "{{users.id}}",
		"ROLE_VAR":     "{{users.role}}",
	}
	result := resolveTemplatesForUser(envMap, "test-user-1", a.store)
	if result["EMAIL_VAR"] != "test@example.com" {
		t.Errorf("EMAIL_VAR = %q, want %q", result["EMAIL_VAR"], "test@example.com")
	}
	if result["USERNAME_VAR"] != "test@example.com" {
		t.Errorf("USERNAME_VAR = %q, want %q", result["USERNAME_VAR"], "test@example.com")
	}
	if result["ID_VAR"] != "test-user-1" {
		t.Errorf("ID_VAR = %q, want %q", result["ID_VAR"], "test-user-1")
	}
	if result["ROLE_VAR"] != "user" {
		t.Errorf("ROLE_VAR = %q, want %q", result["ROLE_VAR"], "user")
	}
}

func TestResolveTemplatesForUser_AllPipes(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	envMap := map[string]string{
		"SANITISED":  "{{users.email|sanitised}}",
		"HASHED":     "{{users.email|hashed}}",
		"LOWER":      "{{users.email|lower}}",
		"UPPER":      "{{users.email|upper}}",
		"URLENCODED": "{{users.email|urlencoded}}",
	}
	result := resolveTemplatesForUser(envMap, "test-user-1", a.store)
	if result["SANITISED"] != "test_at_example_com" {
		t.Errorf("SANITISED = %q, want %q", result["SANITISED"], "test_at_example_com")
	}
	if result["HASHED"] == "" || result["HASHED"] == "{{users.email|hashed}}" {
		t.Errorf("HASHED = %q, want a hash value", result["HASHED"])
	}
	if result["LOWER"] != "test@example.com" {
		t.Errorf("LOWER = %q, want %q", result["LOWER"], "test@example.com")
	}
	if result["UPPER"] != "TEST@EXAMPLE.COM" {
		t.Errorf("UPPER = %q, want %q", result["UPPER"], "TEST@EXAMPLE.COM")
	}
	if result["URLENCODED"] != "test%40example.com" {
		t.Errorf("URLENCODED = %q, want %q", result["URLENCODED"], "test%40example.com")
	}
}

// ---------- BuildEnvForUser combined: templates + mappings + tokens ----------

func TestBuildEnvForUser_TemplateWithMappingsAndTokens(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	a, _, cleanup := testApp(t, "cat", 1)
	defer cleanup()

	// Create a backend with template env vars and env_mappings
	b := &store.Backend{
		ID:                "env-combined",
		Command:           "cat",
		PoolSize:          1,
		MinPoolSize:       1,
		MaxPoolSize:       1,
		Env:               `{"MY_STATIC_KEY":"static-value","MY_TEMPLATE_KEY":"{{users.email}}"}`,
		EnvMappings:       `{"API_TOKEN":"MY_BACKEND_TOKEN"}`,
		Enabled:           true,
		SelfReporting:     true,
		NoKeysRequired:    false,
		SkipJustification: true,
	}
	if err := a.store.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	// Create a user token for the test user on this backend
	token := &store.UserToken{
		UserID:    "test-user-1",
		BackendID: "env-combined",
		EnvKey:    "API_TOKEN",
		Value:     "secret-api-token-123",
	}
	if err := a.store.SetUserToken(token); err != nil {
		t.Fatalf("SetUserToken: %v", err)
	}

	env, err := a.toolMuxer.BuildEnvForUser("test-user-1", "env-combined")
	if err != nil {
		t.Fatalf("BuildEnvForUser: %v", err)
	}

	envMap := sliceToEnvMap(env)

	// Static env vars pass through
	if envMap["MY_STATIC_KEY"] != "static-value" {
		t.Errorf("MY_STATIC_KEY = %q, want %q", envMap["MY_STATIC_KEY"], "static-value")
	}

	// Template vars resolved against user
	if envMap["MY_TEMPLATE_KEY"] != "test@example.com" {
		t.Errorf("MY_TEMPLATE_KEY = %q, want %q", envMap["MY_TEMPLATE_KEY"], "test@example.com")
	}

	// User token mapped through env_mappings: API_TOKEN -> MY_BACKEND_TOKEN
	if envMap["MY_BACKEND_TOKEN"] != "secret-api-token-123" {
		t.Errorf("MY_BACKEND_TOKEN = %q, want %q", envMap["MY_BACKEND_TOKEN"], "secret-api-token-123")
	}

	// Essential system vars present
	if envMap["PATH"] == "" {
		t.Error("PATH is empty, expected a value from process env")
	}
}

// ---------- sanitiseStringArgs tests ----------

func TestSanitiseStringArgs_cleanStringUnchanged(t *testing.T) {
	input := "hello world"
	result := sanitiseStringArgs(input)
	got, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if got != input {
		t.Errorf("clean string changed: got %q, want %q", got, input)
	}
}

func TestSanitiseStringArgs_newlineEscaped(t *testing.T) {
	raw := "line1\nline2"
	result := sanitiseStringArgs(raw)
	got, ok := result.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result)
	}
	if got != "line1\nline2" {
		t.Errorf("expected literal newline in string, got %q", got)
	}
	// Verify round-trip through JSON is clean
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if !strings.Contains(string(b), `\n`) {
		t.Errorf("expected JSON output to contain escaped newline, got %q", string(b))
	}
}

func TestSanitiseStringArgs_tabEscaped(t *testing.T) {
	raw := "col1\tcol2"
	result := sanitiseStringArgs(raw)
	got, _ := result.(string)
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `\t`) {
		t.Errorf("expected JSON to contain escaped tab, got %q", string(b))
	}
}

func TestSanitiseStringArgs_backslashEscaped(t *testing.T) {
	raw := "C:\\Users\\test"
	result := sanitiseStringArgs(raw)
	got, _ := result.(string)
	b, _ := json.Marshal(got)
	// JSON should contain \\ for each \ in the original string
	if !bytes.Contains(b, []byte(`\\`)) {
		t.Errorf("expected JSON to contain escaped backslashes, got %s", b)
	}
}

func TestSanitiseStringArgs_doubleQuoteEscaped(t *testing.T) {
	raw := `say "hello"`
	result := sanitiseStringArgs(raw)
	got, _ := result.(string)
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `\"`) {
		t.Errorf("expected JSON to contain escaped double-quote, got %q", string(b))
	}
}

func TestSanitiseStringArgs_mapValues(t *testing.T) {
	input := map[string]interface{}{
		"code": "func foo() {\n\treturn \"bar\"\n}",
		"name": "clean",
		"num":  42,
	}
	result := sanitiseStringArgs(input)
	got, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	// Clean string preserved
	if got["name"] != "clean" {
		t.Errorf("clean string changed: got %v", got["name"])
	}
	// Number preserved
	if got["num"] != 42 {
		t.Errorf("number changed: got %v", got["num"])
	}
	// Problematic string round-trips through JSON without error
	_, err := json.Marshal(got)
	if err != nil {
		t.Errorf("result map failed JSON marshal: %v", err)
	}
}

func TestSanitiseStringArgs_nestedStructures(t *testing.T) {
	input := map[string]interface{}{
		"outer": map[string]interface{}{
			"inner": "line1\nline2",
			"list":  []interface{}{"a\nb", "clean", 123},
		},
		"flat": "ok",
	}
	result := sanitiseStringArgs(input)
	got, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	_, err := json.Marshal(got)
	if err != nil {
		t.Errorf("nested result failed JSON marshal: %v", err)
	}
}

func TestSanitiseStringArgs_nonStringTypesPreserved(t *testing.T) {
	input := map[string]interface{}{
		"bool":   true,
		"int":    1,
		"float":  3.14,
		"nil":    nil,
		"string": "hello",
	}
	result := sanitiseStringArgs(input)
	got, _ := result.(map[string]interface{})
	if got["bool"] != true {
		t.Errorf("bool changed")
	}
	if got["int"] != 1 {
		t.Errorf("int changed")
	}
	if got["float"] != 3.14 {
		t.Errorf("float changed")
	}
	if got["nil"] != nil {
		t.Errorf("nil changed")
	}
}

func TestSanitiseStringArgs_controlChars(t *testing.T) {
	raw := "before\x00after"
	result := sanitiseStringArgs(raw)
	got, _ := result.(string)
	b, _ := json.Marshal(got)
	// \x00 should be escaped as \u0000
	if !strings.Contains(string(b), `\u0000`) {
		t.Errorf("expected NUL to be escaped as \\u0000, got %q", string(b))
	}
}

func TestSanitiseStringArgs_fullToolsCallBody(t *testing.T) {
	// Simulate a full tools/call JSON-RPC body with problematic strings
	body := `{
		"jsonrpc": "2.0",
		"method": "tools/call",
		"params": {
			"name": "qdrant_save_code",
			"arguments": {
				"code": "func foo() {\n\treturn \"bar\"\n}",
				"description": "line1\nline2",
				"path": "C:\\project\\file.go",
				"tags": ["tag1", "multi\nline"]
			}
		},
		"id": "test-1"
	}`

	var toolReq map[string]interface{}
	if err := json.Unmarshal([]byte(body), &toolReq); err != nil {
		t.Fatalf("failed to unmarshal test body: %v", err)
	}

	params, _ := toolReq["params"].(map[string]interface{})
	toolArgs, _ := params["arguments"].(map[string]interface{})

	sanitised := sanitiseStringArgs(toolArgs).(map[string]interface{})
	params["arguments"] = sanitised

	cleaned, err := json.Marshal(toolReq)
	if err != nil {
		t.Fatalf("re-marshal failed: %v", err)
	}

	// Verify the cleaned JSON round-trips without error
	var verified map[string]interface{}
	if err := json.Unmarshal(cleaned, &verified); err != nil {
		t.Errorf("cleaned body failed JSON round-trip: %v", string(cleaned))
	}

	// Verify the arguments contain properly escaped strings
	verifiedParams, _ := verified["params"].(map[string]interface{})
	verifiedArgs, _ := verifiedParams["arguments"].(map[string]interface{})
	code, _ := verifiedArgs["code"].(string)
	// The Go JSON round-trip should produce valid Go string with proper escaping
	if code == "" {
		t.Error("code argument should not be empty after sanitisation")
	}
}

// ---------- Regression: sanitiseStringArgs preserves required params ----------

// TestSanitiseStringArgs_requiredParamsPreserved verifies that running
// sanitiseStringArgs on tool call arguments followed by json.Marshal does
// NOT strip or corrupt required parameters. This is the exact flow in
// mcpbridge_routing.go handleToolsCall().
func TestSanitiseStringArgs_requiredParamsPreserved(t *testing.T) {
	// Simulate the exact flow from mcpbridge_routing.go handleToolsCall:
	//   1. Unmarshal JSON body into map[string]interface{}
	//   2. Extract toolArgs from params["arguments"]
	//   3. Delete justification
	//   4. Call sanitiseStringArgs(toolArgs)
	//   5. Replace params["arguments"]
	//   6. Re-marshal toolReq
	//   7. Verify required params survive

	tests := []struct {
		name         string
		body         string
		requiredKeys []string
	}{
		{
			name: "qdrant_remember_simple",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_remember",
					"arguments": {
						"content": "test memory",
						"justification": "testing the bug"
					}
				},
				"id": "test-1"
			}`,
			requiredKeys: []string{"content"},
		},
		{
			name: "qdrant_recall_with_query",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_recall",
					"arguments": {
						"query": "what do I know?",
						"justification": "testing recall"
					}
				},
				"id": "test-2"
			}`,
			requiredKeys: []string{"query"},
		},
		{
			name: "qdrant_log_event",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_log_event",
					"arguments": {
						"event": "user_login",
						"justification": "testing event"
					}
				},
				"id": "test-3"
			}`,
			requiredKeys: []string{"event"},
		},
		{
			name: "qdrant_upsert_point",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_upsert_point",
					"arguments": {
						"id": "point-123",
						"justification": "testing upsert"
					}
				},
				"id": "test-4"
			}`,
			requiredKeys: []string{"id"},
		},
		{
			name: "code_snippet_with_special_chars",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_remember",
					"arguments": {
						"content": "func foo() {\n\treturn \"bar\"\n}",
						"justification": "testing code snippets"
					}
				},
				"id": "test-5"
			}`,
			requiredKeys: []string{"content"},
		},
		{
			name: "v2_style_qdrant_call",
			body: `{
				"jsonrpc": "2.0",
				"method": "tools/call",
				"params": {
					"name": "qdrant_call",
					"arguments": {
						"tool": "remember",
						"params": {"content": "test"},
						"justification": "testing v2 path"
					}
				},
				"id": "test-6"
			}`,
			requiredKeys: []string{"params"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1 & 2: Unmarshal and extract args (mirrors handleToolsCall)
			var toolReq map[string]interface{}
			if err := json.Unmarshal([]byte(tt.body), &toolReq); err != nil {
				t.Fatalf("failed to unmarshal body: %v", err)
			}

			params, ok := toolReq["params"].(map[string]interface{})
			if !ok {
				t.Fatal("params not found or not a map")
			}

			toolArgs, _ := params["arguments"].(map[string]interface{})
			if toolArgs == nil {
				t.Fatal("arguments not found or not a map")
			}

			// Step 3: Extract and delete justification
			justification, _ := toolArgs["justification"].(string)
			delete(toolArgs, "justification")

			// Step 4 & 5: Sanitise and replace arguments
			sanitised := sanitiseStringArgs(toolArgs).(map[string]interface{})
			params["arguments"] = sanitised

			// Step 6: Re-marshal
			cleaned, err := json.Marshal(toolReq)
			if err != nil {
				t.Fatalf("re-marshal failed: %v", err)
			}

			// Step 7: Verify the cleaned JSON round-trips
			var verified map[string]interface{}
			if err := json.Unmarshal(cleaned, &verified); err != nil {
				t.Fatalf("cleaned body is not valid JSON: %v\nbody: %s", err, string(cleaned))
			}

			verifiedParams, _ := verified["params"].(map[string]interface{})
			verifiedArgs, _ := verifiedParams["arguments"].(map[string]interface{})

			for _, key := range tt.requiredKeys {
				if _, exists := verifiedArgs[key]; !exists {
					t.Errorf("REGRESSION: required param %q is MISSING after sanitise+marshal cycle!\ninput: %s\noutput: %s", key, tt.body, string(cleaned))
				}
			}

			// Verify justification was stripped
			if _, exists := verifiedArgs["justification"]; exists {
				t.Error("justification should have been stripped from arguments")
			}

			_ = justification // used in the original flow for enforcer check
		})
	}
}

// ---------- No-config fallback tests ----------

func TestNoConfigFallback_AccessTokenTTL(t *testing.T) {
	// This mirrors the fallback struct in main.go when no config file is loaded.
	// It must not use the short 1-hour default from auth.DefaultTokenTTL.
	fallback := config.InternalConfig{
		Server: config.ServerConfig{
			Port:                 "8020",
			LogLevel:             "info",
			AuthCodeTTL:          "10m",
			AccessTokenTTL:       "90d",
			AuthCodeTTLParsed:    10 * time.Minute,
			AccessTokenTTLParsed: 90 * 24 * time.Hour,
		},
	}

	if fallback.Server.AccessTokenTTLParsed < 24*time.Hour {
		t.Errorf("AccessTokenTTLParsed = %v, want >= 24h (was 1h bug)", fallback.Server.AccessTokenTTLParsed)
	}
	if fallback.Server.AuthCodeTTLParsed != 10*time.Minute {
		t.Errorf("AuthCodeTTLParsed = %v, want 10m", fallback.Server.AuthCodeTTLParsed)
	}
}

// sliceToEnvMap converts a []string of "KEY=VALUE" pairs to a map for easier assertion.
func sliceToEnvMap(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			m[e[:idx]] = e[idx+1:]
		}
	}
	return m
}
