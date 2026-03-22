package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type SecretInjector interface {
	PrepareSecrets(ctx context.Context, secrets map[string]string) ([]string, error)
	Cleanup() error
	SecretPath() string
}

type FileSecretInjector struct {
	basePath    string
	secretsFile map[string]string
	mu          sync.Mutex
}

func NewFileSecretInjector(basePath string) *FileSecretInjector {
	return &FileSecretInjector{
		basePath:    basePath,
		secretsFile: make(map[string]string),
	}
}

func (f *FileSecretInjector) SecretPath() string {
	return f.basePath
}

func (f *FileSecretInjector) PrepareSecrets(ctx context.Context, secrets map[string]string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var envVars []string

	for key, value := range secrets {
		filename, err := generateRandomFilename()
		if err != nil {
			return nil, fmt.Errorf("failed to generate filename: %w", err)
		}

		filePath := filepath.Join(f.basePath, filename)

		if err := os.WriteFile(filePath, []byte(value), 0600); err != nil {
			return nil, fmt.Errorf("failed to write secret file: %w", err)
		}

		f.secretsFile[key] = filePath
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, filePath))
	}

	return envVars, nil
}

func (f *FileSecretInjector) Cleanup() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, filePath := range f.secretsFile {
		os.Remove(filePath)
	}

	f.secretsFile = make(map[string]string)
	return nil
}

type EnvVarSecretInjector struct {
	envVars map[string]string
	mu      sync.Mutex
}

func NewEnvVarSecretInjector() *EnvVarSecretInjector {
	return &EnvVarSecretInjector{
		envVars: make(map[string]string),
	}
}

func (e *EnvVarSecretInjector) SetSecret(key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.envVars[key] = value
}

func (e *EnvVarSecretInjector) PrepareSecrets(ctx context.Context, secrets map[string]string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for key, value := range secrets {
		e.envVars[key] = value
	}

	var envVars []string
	for key, value := range e.envVars {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, value))
	}

	return envVars, nil
}

func (e *EnvVarSecretInjector) Cleanup() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.envVars = make(map[string]string)
	return nil
}

func (e *EnvVarSecretInjector) GetEnvForExec() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var envVars []string
	for key, value := range e.envVars {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, value))
	}
	return envVars
}

type ProcessExecutor struct {
	injector SecretInjector
	cmd      *exec.Cmd
}

func NewProcessExecutor(injector SecretInjector) *ProcessExecutor {
	return &ProcessExecutor{
		injector: injector,
	}
}

func (p *ProcessExecutor) Execute(ctx context.Context, command string, secrets map[string]string) error {
	envVars, err := p.injector.PrepareSecrets(ctx, secrets)
	if err != nil {
		return err
	}

	defer p.injector.Cleanup()

	p.cmd = exec.Command("sh", "-c", command)
	p.cmd.Env = envVars

	err = p.cmd.Start()
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		p.cmd.Process.Kill()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func generateRandomFilename() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
