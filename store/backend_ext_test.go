package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
)

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()
	if id1 == "" {
		t.Error("GenerateID should not return empty string")
	}
	if len(id1) != 32 {
		t.Errorf("GenerateID length = %d, want 32", len(id1))
	}
	if id1 == id2 {
		t.Error("two GenerateID calls should return different values")
	}
}

func TestHashAPIKey(t *testing.T) {
	hash, err := HashAPIKey("test-key-123")
	if err != nil {
		t.Fatalf("HashAPIKey: %v", err)
	}
	if hash == "" {
		t.Error("HashAPIKey should not return empty hash")
	}
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") {
		t.Errorf("HashAPIKey should return bcrypt hash, got %q", hash)
	}
	if !ValidateAPIKey("test-key-123", hash) {
		t.Error("ValidateAPIKey should return true for correct key")
	}
	if ValidateAPIKey("wrong-key", hash) {
		t.Error("ValidateAPIKey should return false for wrong key")
	}
}

func TestSetting_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Get non-existent setting
	val, err := s.GetSetting("nonexistent")
	if err != nil {
		t.Fatalf("GetSetting(nonexistent): %v", err)
	}
	if val != "" {
		t.Errorf("GetSetting(nonexistent) = %q, want empty", val)
	}

	// Set a setting
	if err := s.SetSetting("test_key", "test_value"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	// Get it back
	val, err = s.GetSetting("test_key")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "test_value" {
		t.Errorf("GetSetting = %q, want %q", val, "test_value")
	}

	// Update
	if err := s.SetSetting("test_key", "updated_value"); err != nil {
		t.Fatalf("SetSetting update: %v", err)
	}
	val, err = s.GetSetting("test_key")
	if err != nil {
		t.Fatalf("GetSetting after update: %v", err)
	}
	if val != "updated_value" {
		t.Errorf("GetSetting after update = %q, want %q", val, "updated_value")
	}

	// Multiple settings
	if err := s.SetSetting("key1", "val1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("key2", "val2"); err != nil {
		t.Fatal(err)
	}
	val1, _ := s.GetSetting("key1")
	val2, _ := s.GetSetting("key2")
	if val1 != "val1" || val2 != "val2" {
		t.Errorf("multiple settings: got %q, %q", val1, val2)
	}
}

func TestMigrateDefaultBackend_noDefault(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	err := s.MigrateDefaultBackend()
	if err != nil {
		t.Fatalf("MigrateDefaultBackend with no default: %v", err)
	}
}

func TestMigrateDefaultBackend_withDefault(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{
		ID:       "default",
		Command:  "echo legacy",
		PoolSize: 1,
		Env:      "{}",
		Enabled:  true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	err := s.MigrateDefaultBackend()
	if err != nil {
		t.Fatalf("MigrateDefaultBackend: %v", err)
	}

	// "default" should now be "mcpbridge" with is_system=1
	got, err := s.GetBackend("mcpbridge")
	if err != nil {
		t.Fatalf("GetBackend(mcpbridge) after migration: %v", err)
	}
	if !got.IsSystem {
		t.Errorf("IsSystem = false, want true")
	}

	// "default" should no longer exist
	_, err = s.GetBackend("default")
	if err != sql.ErrNoRows {
		t.Errorf("GetBackend(default) after migration err = %v, want ErrNoRows", err)
	}
}

func TestMigrateDefaultBackend_withUserToken(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{
		ID:       "default",
		Command:  "echo legacy",
		PoolSize: 1,
		Env:      "{}",
		Enabled:  true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	u := &User{Name: "Test", Email: "test@example.com", Password: "pw"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	ut := &UserToken{
		UserID:    u.ID,
		BackendID: "default",
		EnvKey:    "API_KEY",
		Value:     "secret-value",
	}
	if err := s.SetUserToken(ut); err != nil {
		t.Fatalf("SetUserToken: %v", err)
	}

	if err := s.MigrateDefaultBackend(); err != nil {
		t.Fatalf("MigrateDefaultBackend: %v", err)
	}

	// Verify token was migrated by checking we can get it under the new backend ID
	tokens, err := s.GetUserTokens(u.ID, "mcpbridge")
	if err != nil {
		t.Fatalf("GetUserTokens after migration: %v", err)
	}
	found := false
	for _, tok := range tokens {
		if tok.BackendID == "mcpbridge" && tok.EnvKey == "API_KEY" {
			found = true
			break
		}
	}
	if !found {
		t.Error("user token was not migrated to mcpbridge backend")
	}
}

func TestOAuthClient_ListAndDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Empty list
	clients, err := s.ListOAuthClients()
	if err != nil {
		t.Fatalf("ListOAuthClients (empty): %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("expected empty list, got %d", len(clients))
	}

	// Create clients
	c1 := &OAuthClient{ClientID: "c1", ClientSecret: "secret1", RedirectURIs: `["http://localhost"]`}
	c2 := &OAuthClient{ClientID: "c2", ClientSecret: "secret2", RedirectURIs: `["http://example.com"]`}
	if err := s.CreateOAuthClient(c1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthClient(c2); err != nil {
		t.Fatal(err)
	}

	// List
	clients, err = s.ListOAuthClients()
	if err != nil {
		t.Fatalf("ListOAuthClients: %v", err)
	}
	if len(clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(clients))
	}

	// Delete
	if err := s.DeleteOAuthClient("c1"); err != nil {
		t.Fatalf("DeleteOAuthClient: %v", err)
	}
	clients, err = s.ListOAuthClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Errorf("after delete, expected 1 client, got %d", len(clients))
	}
	if clients[0].ClientID != "c2" {
		t.Errorf("remaining client = %q, want c2", clients[0].ClientID)
	}

	// Delete non-existent should not error
	if err := s.DeleteOAuthClient("nonexistent"); err != nil {
		t.Errorf("DeleteOAuthClient(nonexistent): %v", err)
	}
}

func TestToolProfile_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	now := time.Now()
	profile := ToolProfileRow{
		ID:           "p1",
		BackendID:    "test-backend",
		ToolName:     "test_tool",
		RiskLevel:    "high",
		ImpactScope:  "system",
		ResourceCost: 5,
		RequiresHITL: true,
		PIIExposure:  false,
		Idempotent:   true,
		RawProfile:   `{"risk":"high"}`,
		ScannedAt:    now,
	}

	// Upsert
	if err := s.UpsertToolProfile(profile); err != nil {
		t.Fatalf("UpsertToolProfile: %v", err)
	}

	// GetToolProfile
	got, err := s.GetToolProfile("test-backend", "test_tool")
	if err != nil {
		t.Fatalf("GetToolProfile: %v", err)
	}
	if got.RiskLevel != "high" {
		t.Errorf("RiskLevel = %q, want high", got.RiskLevel)
	}
	if got.ImpactScope != "system" {
		t.Errorf("ImpactScope = %q, want system", got.ImpactScope)
	}
	if got.ResourceCost != 5 {
		t.Errorf("ResourceCost = %d, want 5", got.ResourceCost)
	}
	if !got.RequiresHITL {
		t.Error("RequiresHITL = false, want true")
	}
	if got.PIIExposure {
		t.Error("PIIExposure = true, want false")
	}
	if !got.Idempotent {
		t.Error("Idempotent = false, want true")
	}
	if got.RawProfile != `{"risk":"high"}` {
		t.Errorf("RawProfile = %q", got.RawProfile)
	}

	// GetToolProfile not found
	_, err = s.GetToolProfile("test-backend", "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("GetToolProfile nonexistent err = %v, want ErrNoRows", err)
	}

	// Update via Upsert
	profile.RiskLevel = "medium"
	if err := s.UpsertToolProfile(profile); err != nil {
		t.Fatalf("UpsertToolProfile (update): %v", err)
	}
	got, err = s.GetToolProfile("test-backend", "test_tool")
	if err != nil {
		t.Fatal(err)
	}
	if got.RiskLevel != "medium" {
		t.Errorf("after update RiskLevel = %q, want medium", got.RiskLevel)
	}
}

func TestToolProfile_ListByBackend(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	profiles := []ToolProfileRow{
		{ID: "p1", BackendID: "be1", ToolName: "tool_a", RiskLevel: "low", ScannedAt: time.Now()},
		{ID: "p2", BackendID: "be1", ToolName: "tool_b", RiskLevel: "high", ScannedAt: time.Now()},
		{ID: "p3", BackendID: "be2", ToolName: "tool_c", RiskLevel: "medium", ScannedAt: time.Now()},
	}
	for _, p := range profiles {
		if err := s.UpsertToolProfile(p); err != nil {
			t.Fatalf("UpsertToolProfile: %v", err)
		}
	}

	// List by backend
	be1Profiles, err := s.ListToolProfilesByBackend("be1")
	if err != nil {
		t.Fatalf("ListToolProfilesByBackend: %v", err)
	}
	if len(be1Profiles) != 2 {
		t.Errorf("be1 profiles: got %d, want 2", len(be1Profiles))
	}

	// ListAll
	all, err := s.ListAllToolProfiles()
	if err != nil {
		t.Fatalf("ListAllToolProfiles: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("all profiles: got %d, want 3", len(all))
	}

	// Delete by backend
	if err := s.DeleteToolProfilesByBackend("be1"); err != nil {
		t.Fatalf("DeleteToolProfilesByBackend: %v", err)
	}
	all, err = s.ListAllToolProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("after delete be1: got %d profiles, want 1", len(all))
	}
}

func TestToolProfile_GetToolProfilesByBackend(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	profiles := []ToolProfileRow{
		{ID: "p1", BackendID: "be1", ToolName: "tool_a", RiskLevel: "low", ScannedAt: time.Now()},
		{ID: "p2", BackendID: "be1", ToolName: "tool_b", RiskLevel: "high", ScannedAt: time.Now()},
	}
	for _, p := range profiles {
		if err := s.UpsertToolProfile(p); err != nil {
			t.Fatal(err)
		}
	}

	profiles, err := s.GetToolProfilesByBackend("be1")
	if err != nil {
		t.Fatalf("GetToolProfilesByBackend: %v", err)
	}
	if len(profiles) != 2 {
		t.Errorf("got %d profiles, want 2", len(profiles))
	}

	_, err = s.GetToolProfilesByBackend("nonexistent")
	if err != nil {
		if err != sql.ErrNoRows {
			t.Errorf("unexpected error for nonexistent: %v", err)
		}
	}
}

func TestBackendProfileSummaries(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	profiles := []ToolProfileRow{
		{ID: "p1", BackendID: "be1", ToolName: "t1", RiskLevel: "high", ScannedAt: time.Now()},
		{ID: "p2", BackendID: "be1", ToolName: "t2", RiskLevel: "low", ScannedAt: time.Now()},
		{ID: "p3", BackendID: "be1", ToolName: "t3", RiskLevel: "critical", ScannedAt: time.Now()},
		{ID: "p4", BackendID: "be2", ToolName: "t4", RiskLevel: "medium", ScannedAt: time.Now()},
	}
	for _, p := range profiles {
		if err := s.UpsertToolProfile(p); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := s.ListBackendProfileSummaries()
	if err != nil {
		t.Fatalf("ListBackendProfileSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("got %d summaries, want 2", len(summaries))
	}
}

func TestBackendStatus_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Create backend first
	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	// List empty
	statuses, err := s.ListBackendStatuses()
	if err != nil {
		t.Fatalf("ListBackendStatuses: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}

	// Update (insert)
	now := time.Now()
	bs := BackendStatus{
		BackendID:    "test-be",
		Status:       "available",
		LastAttempt:  &now,
		LastSuccess:  &now,
		RetryCount:   0,
		ErrorMessage: "",
	}
	if err := s.UpdateBackendStatus(bs); err != nil {
		t.Fatalf("UpdateBackendStatus: %v", err)
	}

	// Get
	got, err := s.GetBackendStatus("test-be")
	if err != nil {
		t.Fatalf("GetBackendStatus: %v", err)
	}
	if got.Status != "available" {
		t.Errorf("Status = %q, want available", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", got.RetryCount)
	}

	// Update
	bs.Status = "unavailable"
	bs.ErrorMessage = "connection failed"
	if err := s.UpdateBackendStatus(bs); err != nil {
		t.Fatalf("UpdateBackendStatus (update): %v", err)
	}
	got, err = s.GetBackendStatus("test-be")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" {
		t.Errorf("after update Status = %q, want unavailable", got.Status)
	}
	if got.ErrorMessage != "connection failed" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}

	// List
	statuses, err = s.ListBackendStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Errorf("expected 1 status, got %d", len(statuses))
	}

	// Get non-existent
	_, err = s.GetBackendStatus("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("GetBackendStatus(nonexistent) err = %v, want ErrNoRows", err)
	}
}

func TestSetBackendUnavailable(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	if err := s.SetBackendUnavailable("test-be", "first failure"); err != nil {
		t.Fatalf("SetBackendUnavailable: %v", err)
	}

	got, err := s.GetBackendStatus("test-be")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" {
		t.Errorf("Status = %q, want unavailable", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.NextRetry == nil {
		t.Error("NextRetry should not be nil")
	}
	if got.ErrorMessage != "first failure" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}

	// Second failure should increment retry
	if err := s.SetBackendUnavailable("test-be", "second failure"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetBackendStatus("test-be")
	if err != nil {
		t.Fatal(err)
	}
	if got.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", got.RetryCount)
	}
	if got.ErrorMessage != "second failure" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
}

func TestSetBackendAvailable(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	if err := s.SetBackendUnavailable("test-be", "temp failure"); err != nil {
		t.Fatal(err)
	}

	if err := s.SetBackendAvailable("test-be"); err != nil {
		t.Fatalf("SetBackendAvailable: %v", err)
	}

	got, err := s.GetBackendStatus("test-be")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" {
		t.Errorf("Status = %q, want available", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", got.RetryCount)
	}
}

func TestGetBackendsNeedingRetry(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	// No backends needing retry initially
	backends, err := s.GetBackendsNeedingRetry()
	if err != nil {
		t.Fatalf("GetBackendsNeedingRetry: %v", err)
	}
	if len(backends) != 0 {
		t.Errorf("expected 0, got %d", len(backends))
	}

	// Set as unavailable with a past next_retry by manipulating via raw SQL
	// SetBackendUnavailable sets next_retry = now + 1s which could be > CURRENT_TIMESTAMP
	// Use raw SQL to insert directly with a past next_retry
	_, err = s.db.Exec(`
		INSERT INTO backend_status (backend_id, status, last_attempt, retry_count, next_retry, error_message, updated_at)
		VALUES (?, 'unavailable', datetime('now', '-1 minute'), 1, datetime('now', '-1 second'), 'failure', CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET
			status = 'unavailable',
			last_attempt = excluded.last_attempt,
			retry_count = excluded.retry_count,
			next_retry = excluded.next_retry,
			error_message = excluded.error_message,
			updated_at = CURRENT_TIMESTAMP`,
		"test-be")
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	backends, err = s.GetBackendsNeedingRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend needing retry, got %d: %v", len(backends), backends)
	}
	if backends[0] != "test-be" {
		t.Errorf("backend = %q, want test-be", backends[0])
	}
}

func TestGetUncachedBackends(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b1 := &Backend{ID: "be-cached", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	b2 := &Backend{ID: "be-uncached", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	b3 := &Backend{ID: "be-disabled", Command: "echo", PoolSize: 1, Env: "{}", Enabled: false}
	if err := s.CreateBackend(b1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBackend(b2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBackend(b3); err != nil {
		t.Fatal(err)
	}

	// Cache be-cached
	if err := s.SetBackendCapabilities("be-cached", []map[string]interface{}{{"name": "tool1"}}); err != nil {
		t.Fatal(err)
	}

	uncached, err := s.GetUncachedBackends()
	if err != nil {
		t.Fatalf("GetUncachedBackends: %v", err)
	}
	if len(uncached) != 1 {
		t.Errorf("expected 1 uncached backend, got %d: %v", len(uncached), uncached)
	}
	if len(uncached) > 0 && uncached[0] != "be-uncached" {
		t.Errorf("uncached = %v, want [be-uncached]", uncached)
	}
}

func TestUserPasswordSalt(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "SaltTest", Email: "salt@example.com", Password: "pw", PasswordSalt: "pre-set-salt"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	// Check that CreateUser preserved our pre-set salt (didn't auto-generate)
	salt, err := s.GetUserPasswordSalt(u.ID)
	if err != nil {
		t.Fatalf("GetUserPasswordSalt: %v", err)
	}
	if salt != "pre-set-salt" {
		t.Errorf("initial salt = %q, want pre-set-salt", salt)
	}

	// Update
	if err := s.UpdateUserPasswordSalt(u.ID, "test-salt-value"); err != nil {
		t.Fatalf("UpdateUserPasswordSalt: %v", err)
	}

	salt, err = s.GetUserPasswordSalt(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if salt != "test-salt-value" {
		t.Errorf("salt = %q, want test-salt-value", salt)
	}
}

func TestRateLimitBucketConfigs(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Create a backend first (FK constraint: rate_limit_buckets.backend_id REFERENCES backends)
	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}

	es := NewEnforcerStore(s.DB())

	// List empty
	configs, err := es.ListRateLimitBucketConfigs()
	if err != nil {
		t.Fatalf("ListRateLimitBucketConfigs: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs, got %d", len(configs))
	}

	// Upsert
	config := enforcer.RateLimitBucketConfigRow{
		ID:         "cfg1",
		BackendID:  "test-be",
		BucketType: "risk",
		Capacity:   100,
		RefillRate: 10,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := es.UpsertRateLimitBucketConfig(config); err != nil {
		t.Fatalf("UpsertRateLimitBucketConfig: %v", err)
	}

	configs, err = es.ListRateLimitBucketConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Errorf("expected 1 config, got %d", len(configs))
	}
	if configs[0].BackendID != "test-be" {
		t.Errorf("BackendID = %q, want test-be", configs[0].BackendID)
	}

	// Update
	config.Capacity = 200
	if err := es.UpsertRateLimitBucketConfig(config); err != nil {
		t.Fatal(err)
	}
	configs, err = es.ListRateLimitBucketConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Errorf("expected 1 config after update, got %d", len(configs))
	}
	if configs[0].Capacity != 200 {
		t.Errorf("Capacity = %d, want 200", configs[0].Capacity)
	}
}

func TestRateLimitStates(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Create a backend and user first (FK constraints on rate_limit_states)
	b := &Backend{ID: "test-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}
	u := &User{Name: "RateLimitUser", Email: "rl@example.com", Password: "pw"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	es := NewEnforcerStore(s.DB())

	// List empty
	states, err := es.ListRateLimitStates()
	if err != nil {
		t.Fatalf("ListRateLimitStates: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("expected 0 states, got %d", len(states))
	}

	// Upsert
	state := enforcer.RateLimitStateRow{
		ID:           "st1",
		UserID:       u.ID,
		BackendID:    "test-be",
		BucketType:   "risk",
		CurrentLevel: 50,
		LastRefillAt: time.Now(),
		CreatedAt:    time.Now(),
	}
	if err := es.UpsertRateLimitState(state); err != nil {
		t.Fatalf("UpsertRateLimitState: %v", err)
	}

	states, err = es.ListRateLimitStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Errorf("expected 1 state, got %d", len(states))
	}
	if states[0].CurrentLevel != 50 {
		t.Errorf("CurrentLevel = %d, want 50", states[0].CurrentLevel)
	}
	if states[0].BucketType != "risk" {
		t.Errorf("BucketType = %q", states[0].BucketType)
	}

	// Update
	state.CurrentLevel = 20
	if err := es.UpsertRateLimitState(state); err != nil {
		t.Fatal(err)
	}
	states, err = es.ListRateLimitStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Errorf("expected 1 state after update, got %d", len(states))
	}
	if states[0].CurrentLevel != 20 {
		t.Errorf("CurrentLevel = %d, want 20", states[0].CurrentLevel)
	}
}
