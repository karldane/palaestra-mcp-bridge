package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testStore creates a fresh SQLite Store in a temp dir. The caller should
// defer s.Close() and os.RemoveAll(dir).
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return s, dir
}

// ---------- New / migrate ----------

func TestNew_CreatesDatabase(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// DB should be open and the tables should exist.
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='backends'`).Scan(&name)
	if err != nil {
		t.Fatalf("backends table not found: %v", err)
	}
	if name != "backends" {
		t.Fatalf("got table name %q, want backends", name)
	}
}

func TestNew_AllTablesExist(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	tables := []string{"backends", "users", "user_tokens", "oauth_clients", "oauth_codes", "oauth_sessions"}
	for _, tbl := range tables {
		var name string
		err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", tbl, err)
		}
	}
}

func TestNew_IdempotentMigration(t *testing.T) {
	dir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "test.db")

	// Open twice — second call should succeed (CREATE TABLE IF NOT EXISTS).
	s1, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2, err := New(dbPath)
	if err != nil {
		t.Fatalf("second New() failed: %v", err)
	}
	s2.Close()
}

// ---------- Backends ----------

func TestBackend_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{
		ID:         "filesystem",
		Command:    "npx -y @modelcontextprotocol/server-filesystem /tmp",
		PoolSize:   2,
		ToolPrefix: "fs_",
		Env:        `{}`,
		Enabled:    true,
	}

	// Create
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend: %v", err)
	}

	// Get
	got, err := s.GetBackend("filesystem")
	if err != nil {
		t.Fatalf("GetBackend: %v", err)
	}
	if got.Command != b.Command {
		t.Errorf("Command = %q, want %q", got.Command, b.Command)
	}
	if got.PoolSize != 2 {
		t.Errorf("PoolSize = %d, want 2", got.PoolSize)
	}
	if got.ToolPrefix != "fs_" {
		t.Errorf("ToolPrefix = %q, want %q", got.ToolPrefix, "fs_")
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}

	// Update
	b.PoolSize = 5
	b.Enabled = false
	if err := s.UpdateBackend(b); err != nil {
		t.Fatalf("UpdateBackend: %v", err)
	}
	got, _ = s.GetBackend("filesystem")
	if got.PoolSize != 5 {
		t.Errorf("after update PoolSize = %d, want 5", got.PoolSize)
	}
	if got.Enabled {
		t.Error("after update Enabled = true, want false")
	}

	// List
	b2 := &Backend{ID: "fetch", Command: "npx fetch-mcp", PoolSize: 1, Env: "{}", Enabled: true}
	s.CreateBackend(b2)
	list, err := s.ListBackends()
	if err != nil {
		t.Fatalf("ListBackends: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListBackends len = %d, want 2", len(list))
	}
	// Sorted by id: fetch < filesystem
	if list[0].ID != "fetch" || list[1].ID != "filesystem" {
		t.Errorf("list order: got [%s, %s], want [fetch, filesystem]", list[0].ID, list[1].ID)
	}

	// Delete
	if err := s.DeleteBackend("filesystem"); err != nil {
		t.Fatalf("DeleteBackend: %v", err)
	}
	_, err = s.GetBackend("jira")
	if err != sql.ErrNoRows {
		t.Errorf("after delete GetBackend err = %v, want ErrNoRows", err)
	}
}

func TestBackend_DuplicateID(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{ID: "dup", Command: "echo", PoolSize: 1, Env: "{}"}
	if err := s.CreateBackend(b); err != nil {
		t.Fatal(err)
	}
	err := s.CreateBackend(b)
	if err == nil {
		t.Fatal("expected error on duplicate backend ID, got nil")
	}
}

func TestBackend_GetNotFound(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	_, err := s.GetBackend("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("GetBackend(nonexistent) err = %v, want ErrNoRows", err)
	}
}

// ---------- Users ----------

func TestUser_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "secret123",
	}

	// Create — auto-generates ID
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" {
		t.Fatal("CreateUser did not set ID")
	}

	// Get by ID
	got, err := s.GetUser(u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("Name = %q, want Alice", got.Name)
	}
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got.Email)
	}
	if !IsBcrypt(got.Password) {
		t.Errorf("Password should be bcrypt hash, got %q", got.Password)
	}
	if CheckPassword(got.Password, "secret123") != nil {
		t.Error("CheckPassword failed for stored bcrypt vs original plaintext")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Get by email
	got2, err := s.GetUserByEmail("alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got2.ID != u.ID {
		t.Errorf("GetUserByEmail ID = %q, want %q", got2.ID, u.ID)
	}

	// Delete
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_, err = s.GetUser(u.ID)
	if err != sql.ErrNoRows {
		t.Errorf("after delete GetUser err = %v, want ErrNoRows", err)
	}
}

func TestUser_DuplicateEmail(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u1 := &User{Name: "Alice", Email: "dup@example.com", Password: "pw1"}
	u2 := &User{Name: "Bob", Email: "dup@example.com", Password: "pw2"}
	if err := s.CreateUser(u1); err != nil {
		t.Fatal(err)
	}
	err := s.CreateUser(u2)
	if err == nil {
		t.Fatal("expected error on duplicate email, got nil")
	}
}

func TestUser_GetByEmail_NotFound(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	_, err := s.GetUserByEmail("nobody@example.com")
	if err != sql.ErrNoRows {
		t.Errorf("GetUserByEmail err = %v, want ErrNoRows", err)
	}
}

func TestUser_ExplicitID(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "custom-id-42", Name: "Bob", Email: "bob@example.com", Password: "pw"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	if u.ID != "custom-id-42" {
		t.Errorf("ID = %q, want custom-id-42", u.ID)
	}
	got, _ := s.GetUser("custom-id-42")
	if got.Name != "Bob" {
		t.Errorf("Name = %q, want Bob", got.Name)
	}
}

// ---------- User Tokens ----------

func TestUserToken_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Need a user and backend first (FK constraints).
	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)
	b2 := &Backend{ID: "conf", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b2)

	// Set tokens
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "ATLASSIAN_API_TOKEN", Value: "tok1"})
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "ATLASSIAN_EMAIL", Value: "a@x.com"})
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "conf", EnvKey: "CONF_TOKEN", Value: "ctok"})

	// Get per backend
	tokens, err := s.GetUserTokens("u1", "jira")
	if err != nil {
		t.Fatalf("GetUserTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens for jira, want 2", len(tokens))
	}

	// Get all
	all, err := s.GetAllUserTokens("u1")
	if err != nil {
		t.Fatalf("GetAllUserTokens: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d total tokens, want 3", len(all))
	}

	// Upsert (INSERT OR REPLACE)
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "ATLASSIAN_API_TOKEN", Value: "updated"})
	tokens, _ = s.GetUserTokens("u1", "jira")
	found := false
	for _, tok := range tokens {
		if tok.EnvKey == "ATLASSIAN_API_TOKEN" && tok.Value == "updated" {
			found = true
		}
	}
	if !found {
		t.Error("upsert did not update ATLASSIAN_API_TOKEN")
	}

	// Delete single token
	if err := s.DeleteUserToken("u1", "jira", "ATLASSIAN_EMAIL"); err != nil {
		t.Fatalf("DeleteUserToken: %v", err)
	}
	tokens, _ = s.GetUserTokens("u1", "jira")
	if len(tokens) != 1 {
		t.Errorf("after delete got %d jira tokens, want 1", len(tokens))
	}
}

func TestUserToken_CascadeOnUserDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "KEY", Value: "val"})

	s.DeleteUser("u1")
	tokens, _ := s.GetUserTokens("u1", "jira")
	if len(tokens) != 0 {
		t.Errorf("tokens not cascaded on user delete: got %d", len(tokens))
	}
}

func TestUserToken_CascadeOnBackendDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "KEY", Value: "val"})

	s.DeleteBackend("jira")
	tokens, _ := s.GetUserTokens("u1", "jira")
	if len(tokens) != 0 {
		t.Errorf("tokens not cascaded on backend delete: got %d", len(tokens))
	}
}

// ---------- OAuth Clients ----------

func TestOAuthClient_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	c := &OAuthClient{
		ClientSecret: "sec123",
		RedirectURIs: `["http://localhost:9876/callback"]`,
		ClientName:   "opencode",
	}

	// Create — auto-generates client_id
	if err := s.CreateOAuthClient(c); err != nil {
		t.Fatalf("CreateOAuthClient: %v", err)
	}
	if c.ClientID == "" {
		t.Fatal("ClientID not generated")
	}

	// Get
	got, err := s.GetOAuthClient(c.ClientID)
	if err != nil {
		t.Fatalf("GetOAuthClient: %v", err)
	}
	if got.ClientName != "opencode" {
		t.Errorf("ClientName = %q, want opencode", got.ClientName)
	}
	if got.ClientSecret != "sec123" {
		t.Errorf("ClientSecret = %q, want sec123", got.ClientSecret)
	}
	if got.RedirectURIs != `["http://localhost:9876/callback"]` {
		t.Errorf("RedirectURIs = %q, want [\"http://localhost:9876/callback\"]", got.RedirectURIs)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func TestOAuthClient_ExplicitID(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	c := &OAuthClient{ClientID: "my-client", ClientSecret: "s", RedirectURIs: "[]", ClientName: "test"}
	if err := s.CreateOAuthClient(c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOAuthClient("my-client")
	if got.ClientName != "test" {
		t.Errorf("ClientName = %q, want test", got.ClientName)
	}
}

func TestOAuthClient_NotFound(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	_, err := s.GetOAuthClient("missing")
	if err != sql.ErrNoRows {
		t.Errorf("err = %v, want ErrNoRows", err)
	}
}

// ---------- OAuth Codes ----------

func TestOAuthCode_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	code := &OAuthCode{
		UserID:        "u1",
		ClientID:      "client1",
		RedirectURI:   "http://localhost/callback",
		CodeChallenge: "abc123challenge",
		Scope:         "mcp",
		ExpiresAt:     time.Now().Add(10 * time.Minute).UTC(),
	}

	// Create — auto-generates code
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatalf("CreateOAuthCode: %v", err)
	}
	if code.Code == "" {
		t.Fatal("Code not generated")
	}

	// Get
	got, err := s.GetOAuthCode(code.Code)
	if err != nil {
		t.Fatalf("GetOAuthCode: %v", err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
	if got.ClientID != "client1" {
		t.Errorf("ClientID = %q, want client1", got.ClientID)
	}
	if got.CodeChallenge != "abc123challenge" {
		t.Errorf("CodeChallenge = %q, want abc123challenge", got.CodeChallenge)
	}
	if got.Scope != "mcp" {
		t.Errorf("Scope = %q, want mcp", got.Scope)
	}

	// Delete
	if err := s.DeleteOAuthCode(code.Code); err != nil {
		t.Fatalf("DeleteOAuthCode: %v", err)
	}
	_, err = s.GetOAuthCode(code.Code)
	if err != sql.ErrNoRows {
		t.Errorf("after delete err = %v, want ErrNoRows", err)
	}
}

func TestOAuthCode_ExplicitCode(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	code := &OAuthCode{
		Code:      "explicit-code-xyz",
		UserID:    "u1",
		ClientID:  "c1",
		ExpiresAt: time.Now().Add(5 * time.Minute).UTC(),
	}
	if err := s.CreateOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOAuthCode("explicit-code-xyz")
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
}

func TestOAuthCode_CascadeOnUserDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	code := &OAuthCode{Code: "c1", UserID: "u1", ClientID: "x", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	s.CreateOAuthCode(code)

	s.DeleteUser("u1")
	_, err := s.GetOAuthCode("c1")
	if err != sql.ErrNoRows {
		t.Errorf("code not cascaded on user delete: err = %v", err)
	}
}

// ---------- OAuth Sessions ----------

func TestOAuthSession_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	sess := &OAuthSession{
		UserID:    "u1",
		ClientID:  "client1",
		Scope:     "mcp",
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}

	// Create — auto-generates tokens
	if err := s.CreateOAuthSession(sess); err != nil {
		t.Fatalf("CreateOAuthSession: %v", err)
	}
	if sess.AccessToken == "" {
		t.Fatal("AccessToken not generated")
	}
	if sess.RefreshToken == "" {
		t.Fatal("RefreshToken not generated")
	}

	// Get by access token
	got, err := s.GetOAuthSession(sess.AccessToken)
	if err != nil {
		t.Fatalf("GetOAuthSession: %v", err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
	if got.Scope != "mcp" {
		t.Errorf("Scope = %q, want mcp", got.Scope)
	}
	if got.RefreshToken != sess.RefreshToken {
		t.Errorf("RefreshToken mismatch")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Get by refresh token
	got2, err := s.GetOAuthSessionByRefresh(sess.RefreshToken)
	if err != nil {
		t.Fatalf("GetOAuthSessionByRefresh: %v", err)
	}
	if got2.AccessToken != sess.AccessToken {
		t.Errorf("AccessToken mismatch via refresh lookup")
	}

	// Delete
	if err := s.DeleteOAuthSession(sess.AccessToken); err != nil {
		t.Fatalf("DeleteOAuthSession: %v", err)
	}
	_, err = s.GetOAuthSession(sess.AccessToken)
	if err != sql.ErrNoRows {
		t.Errorf("after delete err = %v, want ErrNoRows", err)
	}
}

func TestOAuthSession_ExplicitTokens(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	sess := &OAuthSession{
		AccessToken:  "my-access-tok",
		RefreshToken: "my-refresh-tok",
		UserID:       "u1",
		ClientID:     "c1",
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
	}
	if err := s.CreateOAuthSession(sess); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetOAuthSession("my-access-tok")
	if got.RefreshToken != "my-refresh-tok" {
		t.Errorf("RefreshToken = %q, want my-refresh-tok", got.RefreshToken)
	}
}

func TestOAuthSession_CascadeOnUserDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	sess := &OAuthSession{AccessToken: "at1", RefreshToken: "rt1", UserID: "u1", ClientID: "c1", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	s.CreateOAuthSession(sess)

	s.DeleteUser("u1")
	_, err := s.GetOAuthSession("at1")
	if err != sql.ErrNoRows {
		t.Errorf("session not cascaded on user delete: err = %v", err)
	}
}

// ---------- Expiry cleanup ----------

func TestDeleteExpiredSessions(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	// One expired, one valid.
	expired := &OAuthSession{
		AccessToken: "expired-tok", RefreshToken: "r1", UserID: "u1", ClientID: "c1",
		ExpiresAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	valid := &OAuthSession{
		AccessToken: "valid-tok", RefreshToken: "r2", UserID: "u1", ClientID: "c1",
		ExpiresAt: time.Now().Add(1 * time.Hour).UTC(),
	}
	s.CreateOAuthSession(expired)
	s.CreateOAuthSession(valid)

	n, err := s.DeleteExpiredSessions()
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d sessions, want 1", n)
	}

	// Expired one should be gone.
	_, err = s.GetOAuthSession("expired-tok")
	if err != sql.ErrNoRows {
		t.Error("expired session still exists")
	}

	// Valid one should remain.
	_, err = s.GetOAuthSession("valid-tok")
	if err != nil {
		t.Error("valid session was deleted")
	}
}

func TestDeleteExpiredCodes(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	expired := &OAuthCode{
		Code: "exp-code", UserID: "u1", ClientID: "c1",
		ExpiresAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	valid := &OAuthCode{
		Code: "val-code", UserID: "u1", ClientID: "c1",
		ExpiresAt: time.Now().Add(1 * time.Hour).UTC(),
	}
	s.CreateOAuthCode(expired)
	s.CreateOAuthCode(valid)

	n, err := s.DeleteExpiredCodes()
	if err != nil {
		t.Fatalf("DeleteExpiredCodes: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d codes, want 1", n)
	}

	_, err = s.GetOAuthCode("exp-code")
	if err != sql.ErrNoRows {
		t.Error("expired code still exists")
	}
	_, err = s.GetOAuthCode("val-code")
	if err != nil {
		t.Error("valid code was deleted")
	}
}

func TestDeleteExpiredSessions_NoneExpired(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	n, err := s.DeleteExpiredSessions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("deleted %d, want 0", n)
	}
}

func TestDeleteExpiredCodes_NoneExpired(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	n, err := s.DeleteExpiredCodes()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("deleted %d, want 0", n)
	}
}

// ---------- generateID ----------

func TestGenerateID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateID()
		if len(id) != 32 { // 16 bytes = 32 hex chars
			t.Errorf("generateID len = %d, want 32", len(id))
		}
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

// ---------- User Role ----------

func TestUser_RoleDefault(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "Alice", Email: "alice@example.com", Password: "pw"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(u.ID)
	if got.Role != "user" {
		t.Errorf("default Role = %q, want user", got.Role)
	}
}

func TestUser_RoleAdmin(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "Admin", Email: "admin@example.com", Password: "pw", Role: "admin"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(u.ID)
	if got.Role != "admin" {
		t.Errorf("Role = %q, want admin", got.Role)
	}
	// Also check GetUserByEmail
	got2, _ := s.GetUserByEmail("admin@example.com")
	if got2.Role != "admin" {
		t.Errorf("GetUserByEmail Role = %q, want admin", got2.Role)
	}
}

// ---------- ListUsers ----------

func TestListUsers(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Empty
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("ListUsers empty: got %d, want 0", len(users))
	}

	// Add users
	s.CreateUser(&User{Name: "Bob", Email: "bob@example.com", Password: "pw"})
	s.CreateUser(&User{Name: "Alice", Email: "alice@example.com", Password: "pw", Role: "admin"})

	users, err = s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("ListUsers: got %d, want 2", len(users))
	}
	// Ordered by email
	if users[0].Email != "alice@example.com" || users[1].Email != "bob@example.com" {
		t.Errorf("order: got [%s, %s], want [alice@, bob@]", users[0].Email, users[1].Email)
	}
	if users[0].Role != "admin" {
		t.Errorf("Alice role = %q, want admin", users[0].Role)
	}
}

// ---------- UpdateUser ----------

func TestUpdateUser(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "Alice", Email: "alice@example.com", Password: "pw", Role: "user"}
	s.CreateUser(u)

	u.Name = "Alice Updated"
	u.Role = "admin"
	u.Password = "newpw"
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetUser(u.ID)
	if got.Name != "Alice Updated" {
		t.Errorf("Name = %q, want Alice Updated", got.Name)
	}
	if got.Role != "admin" {
		t.Errorf("Role = %q, want admin", got.Role)
	}
	if !IsBcrypt(got.Password) {
		t.Errorf("Password should be bcrypt hash, got %q", got.Password)
	}
	if CheckPassword(got.Password, "newpw") != nil {
		t.Error("CheckPassword failed for stored bcrypt vs updated plaintext")
	}
}

// ---------- Web Sessions ----------

func TestWebSession_CRUD(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	sess := &WebSession{
		UserID:    "u1",
		ExpiresAt: time.Now().Add(24 * time.Hour).UTC(),
	}

	// Create — auto-generates token
	if err := s.CreateWebSession(sess); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if sess.Token == "" {
		t.Fatal("Token not generated")
	}

	// Get
	got, err := s.GetWebSession(sess.Token)
	if err != nil {
		t.Fatalf("GetWebSession: %v", err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want u1", got.UserID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Delete
	if err := s.DeleteWebSession(sess.Token); err != nil {
		t.Fatalf("DeleteWebSession: %v", err)
	}
	_, err = s.GetWebSession(sess.Token)
	if err != sql.ErrNoRows {
		t.Errorf("after delete err = %v, want ErrNoRows", err)
	}
}

func TestWebSession_CascadeOnUserDelete(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	sess := &WebSession{Token: "ws1", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	s.CreateWebSession(sess)

	s.DeleteUser("u1")
	_, err := s.GetWebSession("ws1")
	if err != sql.ErrNoRows {
		t.Errorf("web session not cascaded on user delete: err = %v", err)
	}
}

func TestDeleteExpiredWebSessions(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	expired := &WebSession{Token: "exp", UserID: "u1", ExpiresAt: time.Now().Add(-1 * time.Hour).UTC()}
	valid := &WebSession{Token: "val", UserID: "u1", ExpiresAt: time.Now().Add(1 * time.Hour).UTC()}
	s.CreateWebSession(expired)
	s.CreateWebSession(valid)

	n, err := s.DeleteExpiredWebSessions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}

	_, err = s.GetWebSession("exp")
	if err != sql.ErrNoRows {
		t.Error("expired session still exists")
	}
	_, err = s.GetWebSession("val")
	if err != nil {
		t.Error("valid session was deleted")
	}
}

// ---------- Bcrypt Helpers ----------

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("hello")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !IsBcrypt(hash) {
		t.Errorf("expected bcrypt hash, got %q", hash)
	}
	// Two hashes of the same password should differ (random salt).
	hash2, _ := HashPassword("hello")
	if hash == hash2 {
		t.Error("two hashes of same password should differ")
	}
}

func TestIsBcrypt(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012", true},
		{"$2b$12$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012", true},
		{"$2y$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012", true},
		{"plaintext", false},
		{"", false},
		{"$2x$10$abc", false},
	}
	for _, tc := range cases {
		if got := IsBcrypt(tc.in); got != tc.want {
			t.Errorf("IsBcrypt(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCheckPassword_Bcrypt(t *testing.T) {
	hash, _ := HashPassword("mypassword")
	if err := CheckPassword(hash, "mypassword"); err != nil {
		t.Errorf("CheckPassword should succeed for correct password: %v", err)
	}
	if err := CheckPassword(hash, "wrongpassword"); err == nil {
		t.Error("CheckPassword should fail for wrong password")
	}
}

func TestCheckPassword_Plaintext(t *testing.T) {
	// Legacy plaintext path.
	if err := CheckPassword("legacy123", "legacy123"); err != nil {
		t.Errorf("CheckPassword should succeed for matching plaintext: %v", err)
	}
	if err := CheckPassword("legacy123", "wrong"); err == nil {
		t.Error("CheckPassword should fail for non-matching plaintext")
	}
}

func TestCreateUser_AutoHashes(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "Bob", Email: "bob@example.com", Password: "plaintextpw", Role: "user"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetUser(u.ID)
	if !IsBcrypt(got.Password) {
		t.Fatalf("CreateUser should auto-hash; got %q", got.Password)
	}
	if CheckPassword(got.Password, "plaintextpw") != nil {
		t.Error("stored hash should verify against original plaintext")
	}
}

func TestCreateUser_PreHashedPassthrough(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	hash, _ := HashPassword("already")
	u := &User{Name: "Carol", Email: "carol@example.com", Password: hash, Role: "user"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetUser(u.ID)
	if got.Password != hash {
		t.Errorf("pre-hashed password should be stored as-is; got %q, want %q", got.Password, hash)
	}
}

func TestUpdateUser_AutoHashes(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{Name: "Dave", Email: "dave@example.com", Password: "old", Role: "user"}
	s.CreateUser(u)

	u.Password = "newplaintext"
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetUser(u.ID)
	if !IsBcrypt(got.Password) {
		t.Fatalf("UpdateUser should auto-hash; got %q", got.Password)
	}
	if CheckPassword(got.Password, "newplaintext") != nil {
		t.Error("stored hash should verify against updated plaintext")
	}
}
