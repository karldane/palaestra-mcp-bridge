package secret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileSecretInjector_New(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	require.NotNil(t, injector)
	require.Equal(t, basePath, injector.SecretPath())
}

func TestFileSecretInjector_PrepareSecrets_CreatesFiles(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"GITHUB_TOKEN": "test-token-123",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	require.Len(t, envVars, 1)
	require.True(t, strings.HasPrefix(envVars[0], "GITHUB_TOKEN="))

	secretFile := strings.TrimPrefix(envVars[0], "GITHUB_TOKEN=")
	_, err = os.Stat(secretFile)
	require.NoError(t, err)
}

func TestFileSecretInjector_PrepareSecrets_FilePermissions0600(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"API_KEY": "super-secret-key",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	require.Len(t, envVars, 1)
	secretFile := strings.TrimPrefix(envVars[0], "API_KEY=")

	info, err := os.Stat(secretFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestFileSecretInjector_PrepareSecrets_FileContents(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"DATABASE_URL": "postgres://user:pass@localhost/db",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	require.Len(t, envVars, 1)
	secretFile := strings.TrimPrefix(envVars[0], "DATABASE_URL=")

	contents, err := os.ReadFile(secretFile)
	require.NoError(t, err)
	require.Equal(t, "postgres://user:pass@localhost/db", string(contents))
}

func TestFileSecretInjector_PrepareSecrets_MultipleSecrets(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"TOKEN1": "value1",
		"TOKEN2": "value2",
		"TOKEN3": "value3",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	require.Len(t, envVars, 3)

	for i := 1; i <= 3; i++ {
		key := "TOKEN" + string(rune('0'+i))
		expectedValue := "value" + string(rune('0'+i))
		found := false
		for _, envVar := range envVars {
			if strings.HasPrefix(envVar, key+"=") {
				found = true
				secretFile := strings.TrimPrefix(envVar, key+"=")
				contents, err := os.ReadFile(secretFile)
				require.NoError(t, err)
				require.Equal(t, expectedValue, string(contents))
			}
		}
		require.True(t, found, "Expected to find env var for %s", key)
	}
}

func TestFileSecretInjector_Cleanup_RemovesFiles(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"SECRET_KEY": "cleanup-test",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)

	require.Len(t, envVars, 1)
	secretFile := strings.TrimPrefix(envVars[0], "SECRET_KEY=")

	_, err = os.Stat(secretFile)
	require.NoError(t, err)

	err = injector.Cleanup()
	require.NoError(t, err)

	_, err = os.Stat(secretFile)
	require.True(t, os.IsNotExist(err), "Secret file should be removed after cleanup")
}

func TestFileSecretInjector_Cleanup_Idempotent(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"IDEMPOTENT_KEY": "test",
	}

	_, err = injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)

	err = injector.Cleanup()
	require.NoError(t, err)

	err = injector.Cleanup()
	require.NoError(t, err)

	err = injector.Cleanup()
	require.NoError(t, err)
}

func TestFileSecretInjector_PrepareSecrets_TwoCallsDifferentFiles(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)

	secrets1 := map[string]string{
		"KEY1": "value1",
	}
	envVars1, err := injector.PrepareSecrets(context.Background(), secrets1)
	require.NoError(t, err)
	defer injector.Cleanup()

	file1 := strings.TrimPrefix(envVars1[0], "KEY1=")

	contents1, err := os.ReadFile(file1)
	require.NoError(t, err)
	require.Equal(t, "value1", string(contents1))

	secrets2 := map[string]string{
		"KEY2": "value2",
	}
	envVars2, err := injector.PrepareSecrets(context.Background(), secrets2)
	require.NoError(t, err)

	file2 := strings.TrimPrefix(envVars2[0], "KEY2=")
	contents2, err := os.ReadFile(file2)
	require.NoError(t, err)
	require.Equal(t, "value2", string(contents2))

	require.NotEqual(t, file1, file2, "Second call should create a different file")

	entries, err := os.ReadDir(basePath)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestFileSecretInjector_PrepareSecrets_FileModeCreation(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"PERM_TEST": "permissions-check",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	secretFile := strings.TrimPrefix(envVars[0], "PERM_TEST=")

	info, err := os.Stat(secretFile)
	require.NoError(t, err)

	isOwnerOnly := info.Mode().Perm() == 0600 || info.Mode().Perm() == 0
	require.True(t, isOwnerOnly, "File permissions should be 0600 or less, got %o", info.Mode().Perm())
}

func TestEnvVarSecretInjector_SetSecret(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	injector.SetSecret("TEST_KEY", "test_value")

	require.NotNil(t, injector.envVars)
	require.Equal(t, "test_value", injector.envVars["TEST_KEY"])
}

func TestEnvVarSecretInjector_GetEnvForExec(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	injector.SetSecret("SECRET1", "value1")
	injector.SetSecret("SECRET2", "value2")

	envVars := injector.GetEnvForExec()

	require.Len(t, envVars, 2)

	envMap := make(map[string]string)
	for _, env := range envVars {
		parts := strings.SplitN(env, "=", 2)
		require.Len(t, parts, 2)
		envMap[parts[0]] = parts[1]
	}

	require.Equal(t, "value1", envMap["SECRET1"])
	require.Equal(t, "value2", envMap["SECRET2"])
}

func TestEnvVarSecretInjector_PrepareSecrets_CopiesSecrets(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	secrets := map[string]string{
		"TOKEN": "secret-token",
		"KEY":   "secret-key",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)

	envMap := make(map[string]string)
	for _, env := range envVars {
		parts := strings.SplitN(env, "=", 2)
		require.Len(t, parts, 2)
		envMap[parts[0]] = parts[1]
	}

	require.Equal(t, "secret-token", envMap["TOKEN"])
	require.Equal(t, "secret-key", envMap["KEY"])
}

func TestEnvVarSecretInjector_Cleanup_ClearsSecrets(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	secrets := map[string]string{
		"TO_CLEAN": "cleanup-value",
	}

	_, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)

	err = injector.Cleanup()
	require.NoError(t, err)

	envVars := injector.GetEnvForExec()
	require.Len(t, envVars, 0)
}

func TestProcessExecutor_Execute_WithSecrets(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	executor := NewProcessExecutor(injector)

	secrets := map[string]string{
		"TEST_SECRET": "secret-value",
	}

	err = executor.Execute(context.Background(), "echo test", secrets)
	require.NoError(t, err)
}

func TestProcessExecutor_Execute_NoSecrets(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	executor := NewProcessExecutor(injector)

	secrets := map[string]string{}

	err = executor.Execute(context.Background(), "echo test", secrets)
	require.NoError(t, err)
}

func TestProcessExecutor_Execute_CommandNotFound(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	executor := NewProcessExecutor(injector)

	secrets := map[string]string{
		"UNUSED_SECRET": "value",
	}

	err = executor.Execute(context.Background(), "nonexistent-command-xyz", secrets)
	require.Error(t, err)
}

func TestFileSecretInjector_SecretsNotInEnv(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	secrets := map[string]string{
		"SUPER_SECRET": "my-super-secret-value-12345",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	selfEnv := os.Getenv("SUPER_SECRET")
	require.Empty(t, selfEnv, "Secret should not be in process environment")

	for _, envVar := range envVars {
		require.NotContains(t, envVar, "my-super-secret-value-12345",
			"Secret value should not appear in env var string")
	}
}

func TestFileSecretInjector_SecretPath_Tmpfs(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	require.Equal(t, basePath, injector.SecretPath())

	isTmpfs := strings.HasPrefix(basePath, "/tmp") ||
		strings.HasPrefix(basePath, "/var/folders") ||
		strings.Contains(basePath, ".tmp")

	require.True(t, isTmpfs, "Secret path should be in a temp filesystem")
}

func TestFileSecretInjector_ConcurrentAccess(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			secrets := map[string]string{
				"SECRET_" + string(rune('A'+id)): "value",
			}
			_, err := injector.PrepareSecrets(context.Background(), secrets)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}

	injector.Cleanup()
}

func TestFileSecretInjector_EmptySecrets(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)

	envVars, err := injector.PrepareSecrets(context.Background(), map[string]string{})
	require.NoError(t, err)
	require.Len(t, envVars, 0)

	err = injector.Cleanup()
	require.NoError(t, err)
}

func TestFileSecretInjector_SecretPathExists(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	require.Equal(t, basePath, injector.SecretPath())

	info, err := os.Stat(basePath)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestEnvVarSecretInjector_ConcurrentSetSecret(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			injector.SetSecret("KEY", "value")
		}(i)
	}

	wg.Wait()

	require.NotPanics(t, func() {
		_ = injector.GetEnvForExec()
	})
}

func TestEnvVarSecretInjector_MultipleSetSecret(t *testing.T) {
	injector := NewEnvVarSecretInjector()

	injector.SetSecret("KEY1", "value1")
	injector.SetSecret("KEY2", "value2")
	injector.SetSecret("KEY1", "overwritten")

	envVars := injector.GetEnvForExec()
	require.Len(t, envVars, 2)

	envMap := make(map[string]string)
	for _, env := range envVars {
		parts := strings.SplitN(env, "=", 2)
		envMap[parts[0]] = parts[1]
	}

	require.Equal(t, "overwritten", envMap["KEY1"])
	require.Equal(t, "value2", envMap["KEY2"])
}

func TestProcessExecutor_Execute_Success(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	executor := NewProcessExecutor(injector)

	secrets := map[string]string{
		"ECHO_TEST": "hello-world",
	}

	err = executor.Execute(context.Background(), "echo test", secrets)
	require.NoError(t, err)
}

func TestProcessExecutor_Execute_ContextCancellation(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)
	executor := NewProcessExecutor(injector)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = executor.Execute(ctx, "sleep 60", map[string]string{
		"TEST": "value",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "context")
}

func TestFileSecretInjector_SecretInSubdirectory(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	subDir := filepath.Join(basePath, "secrets")
	err = os.MkdirAll(subDir, 0700)
	require.NoError(t, err)

	injector := NewFileSecretInjector(subDir)
	secrets := map[string]string{
		"NESTED_SECRET": "nested-value",
	}

	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	require.NoError(t, err)
	defer injector.Cleanup()

	require.Len(t, envVars, 1)
	secretFile := strings.TrimPrefix(envVars[0], "NESTED_SECRET=")

	require.True(t, strings.HasPrefix(secretFile, subDir),
		"Secret file should be in the specified subdirectory")

	contents, err := os.ReadFile(secretFile)
	require.NoError(t, err)
	require.Equal(t, "nested-value", string(contents))
}

func TestFileSecretInjector_SecretFilenameUnpredictable(t *testing.T) {
	basePath, err := os.MkdirTemp("", "secret-test")
	require.NoError(t, err)
	defer os.RemoveAll(basePath)

	injector := NewFileSecretInjector(basePath)

	secrets1 := map[string]string{"KEY": "value1"}
	envVars1, err := injector.PrepareSecrets(context.Background(), secrets1)
	require.NoError(t, err)

	file1 := strings.TrimPrefix(envVars1[0], "KEY=")
	injector.Cleanup()

	secrets2 := map[string]string{"KEY": "value2"}
	envVars2, err := injector.PrepareSecrets(context.Background(), secrets2)
	require.NoError(t, err)

	file2 := strings.TrimPrefix(envVars2[0], "KEY=")

	require.NotEqual(t, file1, file2,
		"Secret filenames should be unpredictable/different between calls")
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
