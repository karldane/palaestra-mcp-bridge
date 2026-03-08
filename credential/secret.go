package credential

import (
	"fmt"
	"sync"
)

type SecretStore interface {
	Get(userID, envKey string) (string, error)
	Set(userID, envKey, value string)
	Delete(userID, envKey string)
}

type MockSecretStore struct {
	mu      sync.RWMutex
	secrets map[string]string
}

func NewMockSecretStore() *MockSecretStore {
	return &MockSecretStore{
		secrets: make(map[string]string),
	}
}

func (s *MockSecretStore) key(userID, envKey string) string {
	return fmt.Sprintf("%s:%s", userID, envKey)
}

func (s *MockSecretStore) Get(userID, envKey string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.key(userID, envKey)
	if value, ok := s.secrets[key]; ok {
		return value, nil
	}
	return "", fmt.Errorf("secret not found for user=%s key=%s", userID, envKey)
}

func (s *MockSecretStore) Set(userID, envKey, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.secrets[s.key(userID, envKey)] = value
}

func (s *MockSecretStore) Delete(userID, envKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.secrets, s.key(userID, envKey))
}

func (s *MockSecretStore) SetFromMap(userID string, envVars map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, value := range envVars {
		s.secrets[s.key(userID, key)] = value
	}
}
