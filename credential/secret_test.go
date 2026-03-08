package credential

import (
	"sync"
	"testing"
)

func TestMockSecretStore_GetSet(t *testing.T) {
	store := NewMockSecretStore()

	store.Set("user1", "API_KEY", "secret123")

	value, err := store.Get("user1", "API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "secret123" {
		t.Errorf("expected secret123, got %s", value)
	}
}

func TestMockSecretStore_GetMissing(t *testing.T) {
	store := NewMockSecretStore()

	_, err := store.Get("user1", "MISSING_KEY")
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

func TestMockSecretStore_Delete(t *testing.T) {
	store := NewMockSecretStore()

	store.Set("user1", "API_KEY", "secret123")

	store.Delete("user1", "API_KEY")

	_, err := store.Get("user1", "API_KEY")
	if err == nil {
		t.Error("expected error after deleting secret")
	}
}

func TestMockSecretStore_DeleteMissing(t *testing.T) {
	store := NewMockSecretStore()

	// Should not panic on deleting non-existent key
	store.Delete("user1", "MISSING_KEY")
}

func TestMockSecretStore_UserIsolation(t *testing.T) {
	store := NewMockSecretStore()

	store.Set("user1", "API_KEY", "secret-user1")
	store.Set("user2", "API_KEY", "secret-user2")

	val1, err := store.Get("user1", "API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val1 != "secret-user1" {
		t.Errorf("expected secret-user1, got %s", val1)
	}

	val2, err := store.Get("user2", "API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val2 != "secret-user2" {
		t.Errorf("expected secret-user2, got %s", val2)
	}
}

func TestMockSecretStore_SetFromMap(t *testing.T) {
	store := NewMockSecretStore()

	envVars := map[string]string{
		"API_KEY":    "key123",
		"API_SECRET": "secret456",
		"DB_PASS":    "dbpass789",
	}

	store.SetFromMap("user1", envVars)

	for key, expected := range envVars {
		value, err := store.Get("user1", key)
		if err != nil {
			t.Errorf("unexpected error for key %s: %v", key, err)
		}
		if value != expected {
			t.Errorf("for key %s: expected %s, got %s", key, expected, value)
		}
	}

	// Ensure user2 doesn't have user1's secrets
	_, err := store.Get("user2", "API_KEY")
	if err == nil {
		t.Error("expected error for user2 accessing user1's secret")
	}
}

func TestMockSecretStore_Overwrite(t *testing.T) {
	store := NewMockSecretStore()

	store.Set("user1", "API_KEY", "old-value")
	store.Set("user1", "API_KEY", "new-value")

	value, err := store.Get("user1", "API_KEY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "new-value" {
		t.Errorf("expected new-value, got %s", value)
	}
}

func TestMockSecretStore_ConcurrentAccess(t *testing.T) {
	store := NewMockSecretStore()

	var wg sync.WaitGroup
	numGoroutines := 50

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			defer wg.Done()

			userID := "user1"
			key := "API_KEY"
			value := "value"

			store.Set(userID, key, value)
			store.Get(userID, key)
			store.Delete(userID, key)
		}(i)
	}

	wg.Wait()
}

func TestMockSecretStore_ImplementsInterface(t *testing.T) {
	var _ SecretStore = (*MockSecretStore)(nil)
}
