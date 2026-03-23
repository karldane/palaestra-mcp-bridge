package store

import (
	"context"
	"os"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
)

func TestDecryptionChain(t *testing.T) {
	// Create a temp database
	tmpDB := "test_decryption_chain.db"
	defer os.Remove(tmpDB)

	// Create a test encryption key
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}

	// Create store with test key
	provider := &directProvider{key: keyBytes}
	s, err := NewWithProvider(tmpDB, provider)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Create a test user
	user := &User{
		ID:    "test-user-id",
		Email: "test@example.com",
		Name:  "Test User",
		Role:  "user",
	}
	if err := s.CreateUser(user); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create a test backend
	backend := &Backend{
		ID:      "test-backend",
		Command: "test-command",
		Enabled: true,
	}
	if err := s.CreateBackend(backend); err != nil {
		t.Fatalf("Failed to create backend: %v", err)
	}

	// Store a token
	testValue := "my-secret-token-12345"
	token := &UserToken{
		UserID:    user.ID,
		BackendID: backend.ID,
		EnvKey:    "API_TOKEN",
		Value:     testValue,
	}
	if err := s.SetUserToken(token); err != nil {
		t.Fatalf("Failed to set token: %v", err)
	}

	// Encrypt the token
	encrypted, err := s.KeyStore().EncryptSecret([]byte(testValue))
	if err != nil {
		t.Fatalf("Failed to encrypt token: %v", err)
	}

	// Update the encrypted value in the database
	_, err = s.db.Exec(`UPDATE user_tokens SET encrypted_value = ? WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		string(encrypted), user.ID, backend.ID, "API_TOKEN")
	if err != nil {
		t.Fatalf("Failed to update encrypted token: %v", err)
	}

	// Test 1: Get tokens should return encrypted value
	tokens, err := s.GetUserTokens(user.ID, backend.ID)
	if err != nil {
		t.Fatalf("Failed to get tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("Expected 1 token, got %d", len(tokens))
	}
	if tokens[0].Encrypted == "" {
		t.Error("Encrypted value should not be empty")
	}

	// Test 2: Get decrypted tokens
	decryptedTokens, err := s.GetUserTokensDecrypted(user.ID, backend.ID)
	if err != nil {
		t.Fatalf("Failed to get decrypted tokens: %v", err)
	}
	if len(decryptedTokens) != 1 {
		t.Fatalf("Expected 1 token, got %d", len(decryptedTokens))
	}
	if decryptedTokens[0].Value != testValue {
		t.Errorf("Decrypted value mismatch: got %q, want %q", decryptedTokens[0].Value, testValue)
	}

	// Test 3: Get single decrypted token
	decrypted, err := s.GetUserTokenDecrypted(user.ID, backend.ID, "API_TOKEN")
	if err != nil {
		t.Fatalf("Failed to get single decrypted token: %v", err)
	}
	if decrypted != testValue {
		t.Errorf("Decrypted value mismatch: got %q, want %q", decrypted, testValue)
	}

	// Test 4: Verify env mappings work
	mappings := map[string]string{
		"API_TOKEN": "ATLASSIAN_API_TOKEN",
	}

	// Simulate what the muxer does
	decryptedMap := make(map[string]string)
	for _, tok := range decryptedTokens {
		if backendKey, ok := mappings[tok.EnvKey]; ok {
			decryptedMap[backendKey] = tok.Value
		}
	}

	if decryptedMap["ATLASSIAN_API_TOKEN"] != testValue {
		t.Errorf("Mapped value mismatch: got %q, want %q", decryptedMap["ATLASSIAN_API_TOKEN"], testValue)
	}

	t.Log("All decryption chain tests passed!")
}

func TestMuxerEnvDecryption(t *testing.T) {
	// Create a temp database
	tmpDB := "test_muxer_env.db"
	defer os.Remove(tmpDB)

	// Create a test encryption key
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}

	// Create store with test key
	provider := &directProvider{key: keyBytes}
	s, err := NewWithProvider(tmpDB, provider)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Create a test user
	user := &User{
		ID:    "test-user-id",
		Email: "test@example.com",
		Name:  "Test User",
		Role:  "user",
	}
	if err := s.CreateUser(user); err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create a test backend with env mappings
	backend := &Backend{
		ID:      "atlassian",
		Command: "npx -y @xuandev/atlassian-mcp",
		Enabled: true,
		Env:     `{"ATLASSIAN_DOMAIN":"test.atlassian.net"}`,
		EnvMappings: `{
			"API_TOKEN":"ATLASSIAN_API_TOKEN",
			"EMAIL":"ATLASSIAN_EMAIL",
			"DOMAIN":"ATLASSIAN_DOMAIN"
		}`,
	}
	if err := s.CreateBackend(backend); err != nil {
		t.Fatalf("Failed to create backend: %v", err)
	}

	// Store API token
	apiToken := "my-atlassian-api-token-12345"
	apiTokenEncrypted, err := s.KeyStore().EncryptSecret([]byte(apiToken))
	if err != nil {
		t.Fatalf("Failed to encrypt API token: %v", err)
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, ?, ?)`,
		user.ID, backend.ID, "API_TOKEN", "", string(apiTokenEncrypted))
	if err != nil {
		t.Fatalf("Failed to insert encrypted API token: %v", err)
	}

	// Store email
	email := "test@example.com"
	emailEncrypted, err := s.KeyStore().EncryptSecret([]byte(email))
	if err != nil {
		t.Fatalf("Failed to encrypt email: %v", err)
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, ?, ?)`,
		user.ID, backend.ID, "EMAIL", "", string(emailEncrypted))
	if err != nil {
		t.Fatalf("Failed to insert encrypted email: %v", err)
	}

	// Test: Get decrypted tokens and apply mappings
	tokens, err := s.GetUserTokensDecrypted(user.ID, backend.ID)
	if err != nil {
		t.Fatalf("Failed to get decrypted tokens: %v", err)
	}

	// Check we got 2 tokens
	if len(tokens) != 2 {
		t.Fatalf("Expected 2 tokens, got %d", len(tokens))
	}

	// Build env map with mappings
	envMap := make(map[string]string)
	for _, tok := range tokens {
		envMap[tok.EnvKey] = tok.Value
	}

	// Apply mappings (as muxer does)
	mappedEnv := make(map[string]string)
	for userKey, value := range envMap {
		if backendKey, hasMapping := map[string]string{
			"API_TOKEN": "ATLASSIAN_API_TOKEN",
			"EMAIL":     "ATLASSIAN_EMAIL",
		}[userKey]; hasMapping {
			mappedEnv[backendKey] = value
		} else {
			mappedEnv[userKey] = value
		}
	}

	// Verify the final env vars
	if mappedEnv["ATLASSIAN_API_TOKEN"] != apiToken {
		t.Errorf("ATLASSIAN_API_TOKEN mismatch: got %q, want %q", mappedEnv["ATLASSIAN_API_TOKEN"], apiToken)
	}
	if mappedEnv["ATLASSIAN_EMAIL"] != email {
		t.Errorf("ATLASSIAN_EMAIL mismatch: got %q, want %q", mappedEnv["ATLASSIAN_EMAIL"], email)
	}

	t.Log("Muxer env decryption test passed!")
}

// directProvider implements crypto.KEKProvider for testing
type directProvider struct {
	key []byte
}

func (p *directProvider) GetKey(ctx context.Context) ([]byte, error) {
	k := make([]byte, len(p.key))
	copy(k, p.key)
	return k, nil
}

func (p *directProvider) KeyID() string {
	return crypto.KeyID(p.key)
}

func (p *directProvider) Close() error {
	return nil
}
