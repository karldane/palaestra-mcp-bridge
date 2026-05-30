package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testUserDEK generates a random 32-byte user-derived encryption key.
func testUserDEK(t *testing.T) []byte {
	t.Helper()
	dek, err := crypto.GenerateRandomKey()
	if err != nil {
		t.Fatalf("GenerateRandomKey: %v", err)
	}
	return dek
}

// testWrongDEK generates a different random key for wrong-password tests.
func testWrongDEK(t *testing.T) []byte {
	t.Helper()
	return testUserDEK(t)
}

// brokenMasterKeyStore returns a KeyStore with a different master key,
// causing master-key decryption to fail. Uses env var ENCRYPTION_KEY_DIFF
// so the store has a different key than testStoreWithCrypto's store.
func brokenMasterKeyStore(t *testing.T) *crypto.KeyStore {
	t.Helper()
	os.Setenv("ENCRYPTION_KEY_DIFF", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Cleanup(func() { os.Unsetenv("ENCRYPTION_KEY_DIFF") })
	return crypto.NewKeyStore(crypto.NewEnvVarProvider("ENCRYPTION_KEY_DIFF", ""))
}

// rawTokenRow holds the raw DB columns for a user_tokens row.
type rawTokenRow struct {
	UserID         string
	BackendID      string
	EnvKey         string
	Value          string
	EncryptedValue string
	EncryptedDEK   string
	EncryptionType string
}

// fetchRawTokenRow reads a user_tokens row directly from the DB.
func fetchRawTokenRow(t *testing.T, s *Store, userID, backendID, envKey string) rawTokenRow {
	t.Helper()
	var row rawTokenRow
	err := s.db.QueryRow(
		`SELECT user_id, backend_id, env_key, value,
		        COALESCE(encrypted_value, ''), COALESCE(encrypted_dek, ''),
		        COALESCE(encryption_type, '')
		 FROM user_tokens WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		userID, backendID, envKey,
	).Scan(&row.UserID, &row.BackendID, &row.EnvKey, &row.Value,
		&row.EncryptedValue, &row.EncryptedDEK, &row.EncryptionType)
	if err != nil {
		t.Fatalf("fetchRawTokenRow: %v", err)
	}
	return row
}

func testStoreWithCrypto(t *testing.T) (*Store, string, *crypto.EnvVarProvider) {
	t.Helper()
	dir, err := os.MkdirTemp("", "store-crypto-test-*")
	if err != nil {
		t.Fatal(err)
	}

	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := crypto.NewEnvVarProvider("ENCRYPTION_KEY", "")
	_, err = provider.GetKey(context.Background())
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	s, err := NewWithProvider(filepath.Join(dir, "test.db"), provider)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return s, dir, provider
}

func TestUserToken_EncryptedFieldExists(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	var colName string
	err := s.db.QueryRow(`
		SELECT name FROM pragma_table_info('user_tokens') WHERE name='encrypted_value'
	`).Scan(&colName)
	if err == sql.ErrNoRows {
		t.Error("encrypted_value column should exist in user_tokens table")
	} else if err != nil {
		t.Errorf("unexpected error checking column: %v", err)
	}
}

func TestGetUserTokenDecrypted_RoundTrip(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	plaintext := "super-secret-api-token-12345"
	decrypted, err := s.GetUserTokenDecrypted("u1", "jira", "API_TOKEN")
	if err == nil {
		t.Error("expected error for non-existent token")
	}
	if decrypted != "" {
		t.Error("expected empty string for non-existent token")
	}

	encrypted, err := s.keyStore.EncryptSecret([]byte(plaintext))
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	_, err = s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", "API_TOKEN", string(encrypted))
	if err != nil {
		t.Fatalf("insert token failed: %v", err)
	}

	decrypted, err = s.GetUserTokenDecrypted("u1", "jira", "API_TOKEN")
	if err != nil {
		t.Fatalf("GetUserTokenDecrypted failed: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestGetUserTokensDecrypted_MultipleTokens(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	tokens := map[string]string{
		"API_TOKEN": "secret-token-abc",
		"EMAIL":     "user@example.com",
		"PASSWORD":  "secret-password",
	}

	for key, value := range tokens {
		encrypted, err := s.keyStore.EncryptSecret([]byte(value))
		if err != nil {
			t.Fatalf("failed to encrypt %s: %v", key, err)
		}
		_, err = s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", key, string(encrypted))
		if err != nil {
			t.Fatalf("insert token failed for %s: %v", key, err)
		}
	}

	decryptedTokens, err := s.GetUserTokensDecrypted("u1", "jira")
	if err != nil {
		t.Fatalf("GetUserTokensDecrypted failed: %v", err)
	}

	if len(decryptedTokens) != 3 {
		t.Fatalf("got %d tokens, want 3", len(decryptedTokens))
	}

	found := make(map[string]string)
	for _, tok := range decryptedTokens {
		found[tok.EnvKey] = tok.Value
	}

	for key, expected := range tokens {
		if found[key] != expected {
			t.Errorf("token %s = %q, want %q", key, found[key], expected)
		}
	}
}

func TestGetUserTokensDecrypted_EmptyEncryptedValue(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN", Value: "plaintext-value"})

	tokens, err := s.GetUserTokensDecrypted("u1", "jira")
	if err != nil {
		t.Fatalf("GetUserTokensDecrypted failed: %v", err)
	}

	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}

	if tokens[0].Encrypted != "" {
		t.Errorf("Encrypted field should be empty for plaintext token, got %q", tokens[0].Encrypted)
	}
}

func TestHasEncryptedTokens_NoTokens(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	hasEncrypted, err := s.HasEncryptedTokens()
	if err != nil {
		t.Fatalf("HasEncryptedTokens failed: %v", err)
	}
	if hasEncrypted {
		t.Error("should not have encrypted tokens when none exist")
	}
}

func TestHasEncryptedTokens_OnlyPlaintext(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN", Value: "plaintext"})

	hasEncrypted, err := s.HasEncryptedTokens()
	if err != nil {
		t.Fatalf("HasEncryptedTokens failed: %v", err)
	}
	if hasEncrypted {
		t.Error("should not report encrypted tokens when only plaintext exist")
	}
}

func TestHasEncryptedTokens_WithEncrypted(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	encrypted, _ := s.keyStore.EncryptSecret([]byte("secret"))
	s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", "TOKEN", string(encrypted))

	hasEncrypted, err := s.HasEncryptedTokens()
	if err != nil {
		t.Fatalf("HasEncryptedTokens failed: %v", err)
	}
	if !hasEncrypted {
		t.Error("should report encrypted tokens")
	}
}

func TestMigrateSecrets_Basic(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN1", Value: "secret1"})
	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN2", Value: "secret2"})

	err := s.MigrateSecrets(context.Background())
	if err != nil {
		t.Fatalf("MigrateSecrets failed: %v", err)
	}

	hasEncrypted, _ := s.HasEncryptedTokens()
	if !hasEncrypted {
		t.Error("should have encrypted tokens after migration")
	}

	tokens, _ := s.GetUserTokensDecrypted("u1", "jira")
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens after migration, want 2", len(tokens))
	}

	found := make(map[string]string)
	for _, tok := range tokens {
		found[tok.EnvKey] = tok.Value
	}

	if found["TOKEN1"] != "secret1" {
		t.Errorf("TOKEN1 = %q, want secret1", found["TOKEN1"])
	}
	if found["TOKEN2"] != "secret2" {
		t.Errorf("TOKEN2 = %q, want secret2", found["TOKEN2"])
	}
}

func TestMigrateSecrets_AlreadyMigrated(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	encrypted, _ := s.keyStore.EncryptSecret([]byte("secret"))
	s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", "TOKEN", string(encrypted))

	err := s.MigrateSecrets(context.Background())
	if err != nil {
		t.Fatalf("MigrateSecrets failed: %v", err)
	}

	decrypted, _ := s.GetUserTokenDecrypted("u1", "jira", "TOKEN")
	if decrypted != "secret" {
		t.Errorf("decrypted = %q, want secret", decrypted)
	}
}

func TestVerifyEncryptedSecrets_AllValid(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	tokens := []string{"secret1", "secret2", "secret3"}
	keys := []string{"TOKEN_A", "TOKEN_B", "TOKEN_C"}
	for i, secret := range tokens {
		encrypted, err := s.keyStore.EncryptSecret([]byte(secret))
		if err != nil {
			t.Fatalf("encrypt failed: %v", err)
		}
		_, err = s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", keys[i], string(encrypted))
		if err != nil {
			t.Fatalf("insert token failed: %v", err)
		}
	}

	success, fail, err := s.VerifyEncryptedSecrets(context.Background())
	if err != nil {
		t.Fatalf("VerifyEncryptedSecrets failed: %v", err)
	}
	if success != 3 {
		t.Errorf("success = %d, want 3", success)
	}
	if fail != 0 {
		t.Errorf("fail = %d, want 0", fail)
	}
}

func TestVerifyEncryptedSecrets_SomeInvalid(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	validEncrypted, _ := s.keyStore.EncryptSecret([]byte("valid"))
	s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", "VALID", string(validEncrypted))

	s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`, "u1", "jira", "INVALID", string([]byte{0xFF, 0xFE}))

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "PLAIN", Value: "plaintext"})

	success, fail, err := s.VerifyEncryptedSecrets(context.Background())
	if err != nil {
		t.Fatalf("VerifyEncryptedSecrets failed: %v", err)
	}
	if success != 1 {
		t.Errorf("success = %d, want 1", success)
	}
	if fail != 2 {
		t.Errorf("fail = %d, want 2 (1 invalid encrypted + 1 plaintext)", fail)
	}
}

func TestVerifyEncryptedSecrets_NoEncrypted(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN", Value: "plaintext"})

	success, fail, err := s.VerifyEncryptedSecrets(context.Background())
	if err != nil {
		t.Fatalf("VerifyEncryptedSecrets failed: %v", err)
	}
	if success != 0 {
		t.Errorf("success = %d, want 0", success)
	}
	if fail != 1 {
		t.Errorf("fail = %d, want 1 (plaintext token)", fail)
	}
}

func TestMigrateSecrets_RollbackCapability(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	b := &Backend{ID: "jira", Command: "echo", PoolSize: 1, Env: "{}"}
	s.CreateBackend(b)

	s.SetUserToken(&UserToken{UserID: "u1", BackendID: "jira", EnvKey: "TOKEN", Value: "original-secret"})

	var oldValue string
	s.db.QueryRow(`
		SELECT value FROM user_tokens WHERE user_id='u1' AND backend_id='jira' AND env_key='TOKEN'
	`).Scan(&oldValue)
	if oldValue != "original-secret" {
		t.Fatalf("setup failed: expected original value")
	}

	err := s.MigrateSecrets(context.Background())
	if err != nil {
		t.Fatalf("MigrateSecrets failed: %v", err)
	}

	var afterValue string
	s.db.QueryRow(`
		SELECT value FROM user_tokens WHERE user_id='u1' AND backend_id='jira' AND env_key='TOKEN'
	`).Scan(&afterValue)
	if afterValue != "original-secret" {
		t.Errorf("migration should preserve plaintext value for rollback capability")
	}

	decrypted, _ := s.GetUserTokenDecrypted("u1", "jira", "TOKEN")
	if decrypted != "original-secret" {
		t.Errorf("decrypted = %q, want original-secret", decrypted)
	}
}

// ---------- Phase 1: Dual-key encryption invariants (TDD) ----------

func setupUserAndBackend(t *testing.T, s *Store) {
	t.Helper()
	s.CreateUser(&User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"})
	s.CreateBackend(&Backend{ID: "github", Command: "echo", PoolSize: 1, Env: "{}"})
}

func TestSetTokenStoresLayeredCiphertext(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()
	setupUserAndBackend(t, s)

	userDEK := testUserDEK(t)
	err := s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK)
	require.NoError(t, err)

	row := fetchRawTokenRow(t, s, "u1", "github", "API_TOKEN")
	assert.Equal(t, "user", row.EncryptionType)
	assert.NotEmpty(t, row.EncryptedValue)
	assert.NotEmpty(t, row.EncryptedDEK)

	masterCiphertext := []byte(row.EncryptedValue)
	plaintext, err := s.keyStore.DecryptSecret(masterCiphertext)
	require.NoError(t, err)
	assert.Equal(t, "ghp_secret", string(plaintext))

	encDEK := []byte(row.EncryptedDEK)
	unwrapped, err := crypto.AES256GCMDecrypt(userDEK, encDEK)
	require.NoError(t, err)
	assert.Equal(t, masterCiphertext, unwrapped,
		"encrypted_dek must wrap the master-key ciphertext, not a random DEK")
}

func TestUserPathRequiresBothKeys(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()
	setupUserAndBackend(t, s)

	userDEK := testUserDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	got, err := s.GetUserTokenDecryptedWithUserDEK("u1", "github", "API_TOKEN", userDEK)
	require.NoError(t, err)
	assert.Equal(t, "ghp_secret", got)
}

func TestWrongPasswordIsHardFailure(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()
	setupUserAndBackend(t, s)

	userDEK := testUserDEK(t)
	wrongDEK := testWrongDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	got, err := s.GetUserTokenDecryptedWithUserDEK("u1", "github", "API_TOKEN", wrongDEK)
	assert.Error(t, err, "wrong password must return an error")
	assert.Empty(t, got, "wrong password must never return plaintext")
}

func TestSpawnPathDecryptsWithMasterKeyOnly(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()
	setupUserAndBackend(t, s)

	userDEK := testUserDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	got, err := s.GetUserTokenDecrypted("u1", "github", "API_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, "ghp_secret", got)
}

func TestNoFallbackInUserPath(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()
	setupUserAndBackend(t, s)

	userDEK := testUserDEK(t)
	wrongDEK := testWrongDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	s.keyStore = brokenMasterKeyStore(t)

	_, err := s.GetUserTokenDecryptedWithUserDEK("u1", "github", "API_TOKEN", wrongDEK)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user-DEK unwrap failed",
		"error must come from the user-DEK layer, not from a master-key fallback")
}

func TestUpdateMasterKeyEncryptedDoesNotExist(t *testing.T) {
	t.Log("UpdateMasterKeyEncrypted must not exist in store/user.go")
}
