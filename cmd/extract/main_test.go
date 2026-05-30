package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "extract-test-*")
	require.NoError(t, err)

	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	provider := crypto.NewEnvVarProvider("ENCRYPTION_KEY", "")

	s, err := store.NewWithProvider(filepath.Join(dir, "test.db"), provider)
	require.NoError(t, err)

	cleanup := func() {
		s.Close()
		os.Unsetenv("ENCRYPTION_KEY")
		os.RemoveAll(dir)
	}

	s.CreateUser(&store.User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"})
	s.CreateBackend(&store.Backend{ID: "github", Command: "echo", PoolSize: 1, Env: "{}"})

	return s, cleanup
}

func testUserDEK(t *testing.T) []byte {
	t.Helper()
	dek, err := crypto.GenerateRandomKey()
	require.NoError(t, err)
	return dek
}

func testWrongDEK(t *testing.T) []byte {
	t.Helper()
	return testUserDEK(t)
}

func TestExtractFailsWithWrongPassword(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	userDEK := testUserDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	_, err := ExtractToken(s, "u1", "github", "API_TOKEN", testWrongDEK(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user-DEK unwrap failed")
}

func TestExtractSucceedsWithCorrectPassword(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	userDEK := testUserDEK(t)
	require.NoError(t, s.SetUserTokenWithUserDEK("u1", "github", "API_TOKEN", "ghp_secret", userDEK))

	got, err := ExtractToken(s, "u1", "github", "API_TOKEN", userDEK)
	require.NoError(t, err)
	assert.Equal(t, "ghp_secret", got)
}

func TestExtractFailsOnNoUserDEKLayer(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	encrypted, _ := s.KeyStore().EncryptSecret([]byte("legacy-secret"))
	_, err := s.DB().Exec(`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value) VALUES (?, ?, ?, '', ?)`,
		"u1", "github", "LEGACY_TOKEN", string(encrypted))
	require.NoError(t, err)

	_, err = ExtractToken(s, "u1", "github", "LEGACY_TOKEN", testUserDEK(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no user-DEK layer")
}


