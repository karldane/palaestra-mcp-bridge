package store

import (
	"context"
	"os"
	"testing"
)

func TestBackendEnvEncryption_RoundTrip(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	origEnv := `{"API_KEY":"sk-123456","API_SECRET":"super-secret-value","URL":"https://api.example.com"}`

	b := &Backend{
		ID:      "test-be",
		Command: "echo test",
		Env:     origEnv,
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	// Verify encrypted_env column is populated in DB
	var encryptedEnv string
	err := s.db.QueryRow(`SELECT COALESCE(encrypted_env, '') FROM backends WHERE id = ?`, b.ID).Scan(&encryptedEnv)
	if err != nil {
		t.Fatalf("query encrypted_env failed: %v", err)
	}
	if encryptedEnv == "" {
		t.Error("encrypted_env should not be empty after CreateBackend with crypto")
	}

	// Verify env column was cleared (plaintext removed)
	var envCol string
	err = s.db.QueryRow(`SELECT env FROM backends WHERE id = ?`, b.ID).Scan(&envCol)
	if err != nil {
		t.Fatalf("query env column failed: %v", err)
	}
	if envCol != "{}" {
		t.Errorf("env column should be '{}' after encryption, got %q", envCol)
	}

	// Verify GetBackend returns decrypted env
	got, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.Env != origEnv {
		t.Errorf("GetBackend.Env mismatch:\n  want: %s\n  got:  %s", origEnv, got.Env)
	}
	if got.EncryptedEnv == "" {
		t.Error("GetBackend.EncryptedEnv should be non-empty")
	}
}

func TestBackendEnvEncryption_EmptyEnv(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{
		ID:      "empty-env-be",
		Command: "echo test",
		Env:     "{}",
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	var encryptedEnv string
	err := s.db.QueryRow(`SELECT COALESCE(encrypted_env, '') FROM backends WHERE id = ?`, b.ID).Scan(&encryptedEnv)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if encryptedEnv != "" {
		t.Error("encrypted_env should be empty for '{}' env")
	}

	got, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.Env != "{}" {
		t.Errorf("expected '{}', got %q", got.Env)
	}
}

func TestBackendEnvEncryption_Update(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	b := &Backend{
		ID:      "update-be",
		Command: "echo test",
		Env:     `{"OLD_KEY":"old-value"}`,
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	// Update with new env
	b.Env = `{"NEW_KEY":"new-value","ANOTHER":"value2"}`
	if err := s.UpdateBackend(b); err != nil {
		t.Fatalf("UpdateBackend failed: %v", err)
	}

	// Verify encrypted in DB, plaintext cleared
	var encryptedEnv, envCol string
	err := s.db.QueryRow(`SELECT COALESCE(encrypted_env, ''), env FROM backends WHERE id = ?`, b.ID).Scan(&encryptedEnv, &envCol)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if encryptedEnv == "" {
		t.Error("encrypted_env should be non-empty after UpdateBackend")
	}
	if envCol != "{}" {
		t.Errorf("env column should be '{}' after update, got %q", envCol)
	}

	// Verify GetBackend returns updated env
	got, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.Env != `{"NEW_KEY":"new-value","ANOTHER":"value2"}` {
		t.Errorf("GetBackend.Env mismatch:\n  want: %s\n  got:  %s", `{"NEW_KEY":"new-value","ANOTHER":"value2"}`, got.Env)
	}
}

func TestBackendEnvEncryption_UpdateKeepsExistingEncryptedEnv(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	origEnv := `{"KEY":"value"}`
	b := &Backend{
		ID:      "keep-be",
		Command: "echo test",
		Env:     origEnv,
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	// Fetch the backend (gets decrypted env), then update without changing env
	got, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}

	// Change only the command, leave Env as-is (decrypted from DB)
	got.Command = "echo updated"
	if err := s.UpdateBackend(got); err != nil {
		t.Fatalf("UpdateBackend failed: %v", err)
	}

	// Verify env still decrypts correctly
	reloaded, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend after update failed: %v", err)
	}
	if reloaded.Env != origEnv {
		t.Errorf("env changed after non-env update:\n  want: %s\n  got:  %s", origEnv, reloaded.Env)
	}
	if reloaded.Command != "echo updated" {
		t.Errorf("command not updated: got %q", reloaded.Command)
	}
}

func TestBackendEnvEncryption_ListBackends(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	backends := []*Backend{
		{ID: "be-1", Command: "cmd1", Env: `{"KEY1":"val1"}`, Enabled: true},
		{ID: "be-2", Command: "cmd2", Env: `{"KEY2":"val2"}`, Enabled: true},
		{ID: "be-3", Command: "cmd3", Env: `{"KEY3":"val3"}`, Enabled: true},
	}
	for _, b := range backends {
		if err := s.CreateBackend(b); err != nil {
			t.Fatalf("CreateBackend(%s) failed: %v", b.ID, err)
		}
	}

	list, err := s.ListBackends()
	if err != nil {
		t.Fatalf("ListBackends failed: %v", err)
	}

	if len(list) != 3 {
		t.Fatalf("expected 3 backends, got %d", len(list))
	}

	envMap := map[string]string{
		"be-1": `{"KEY1":"val1"}`,
		"be-2": `{"KEY2":"val2"}`,
		"be-3": `{"KEY3":"val3"}`,
	}
	for _, b := range list {
		wantEnv, ok := envMap[b.ID]
		if !ok {
			t.Errorf("unexpected backend: %s", b.ID)
			continue
		}
		if b.Env != wantEnv {
			t.Errorf("%s: Env mismatch:\n  want: %s\n  got:  %s", b.ID, wantEnv, b.Env)
		}
		if b.EncryptedEnv == "" {
			t.Errorf("%s: EncryptedEnv should be non-empty", b.ID)
		}
	}
}

func TestBackendEnvEncryption_LegacyFallback(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Simulate a legacy backend with env but no encrypted_env (pre-migration)
	legacyEnv := `{"LEGACY_KEY":"still-works"}`
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, enabled, env) VALUES (?, ?, ?, ?, ?)`,
		"legacy-be", "echo test", 1, 1, legacyEnv,
	)
	if err != nil {
		t.Fatalf("insert legacy backend failed: %v", err)
	}

	got, err := s.GetBackend("legacy-be")
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.Env != legacyEnv {
		t.Errorf("legacy Env mismatch:\n  want: %s\n  got:  %s", legacyEnv, got.Env)
	}
	if got.EncryptedEnv != "" {
		t.Error("legacy backend should have empty EncryptedEnv")
	}
}

func TestBackendEnvEncryption_MigrateAdminEnv(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Create legacy backends (plaintext env, no encrypted_env)
	legacyEnvs := []struct {
		id  string
		env string
	}{
		{"migrate-be-1", `{"KEY1":"val1"}`},
		{"migrate-be-2", `{"KEY2":"val2"}`},
	}
	for _, le := range legacyEnvs {
		_, err := s.db.Exec(
			`INSERT INTO backends (id, command, pool_size, enabled, env) VALUES (?, ?, ?, ?, ?)`,
			le.id, "echo test", 1, 1, le.env,
		)
		if err != nil {
			t.Fatalf("insert legacy backend %s failed: %v", le.id, err)
		}
	}

	// Also insert one that's already encrypted (should be skipped)
	alreadyEncrypted := `{"ALREADY":"done"}`
	b := &Backend{ID: "already-enc", Command: "echo test", Env: alreadyEncrypted, Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	count, err := s.MigrateAdminEnv(context.Background())
	if err != nil {
		t.Fatalf("MigrateAdminEnv failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 migrated, got %d", count)
	}

	// Verify all three backends work after migration
	for _, le := range legacyEnvs {
		var encryptedEnv, envCol string
		err := s.db.QueryRow(
			`SELECT COALESCE(encrypted_env, ''), env FROM backends WHERE id = ?`, le.id,
		).Scan(&encryptedEnv, &envCol)
		if err != nil {
			t.Fatalf("query %s failed: %v", le.id, err)
		}
		if encryptedEnv == "" {
			t.Errorf("%s: encrypted_env should be set after migration", le.id)
		}
		if envCol != "{}" {
			t.Errorf("%s: env column should be '{}' after migration, got %q", le.id, envCol)
		}

		got, err := s.GetBackend(le.id)
		if err != nil {
			t.Fatalf("GetBackend(%s) failed: %v", le.id, err)
		}
		if got.Env != le.env {
			t.Errorf("%s: env mismatch after migration:\n  want: %s\n  got:  %s", le.id, le.env, got.Env)
		}
	}

	// Already-encrypted backend still works
	got, err := s.GetBackend("already-enc")
	if err != nil {
		t.Fatalf("GetBackend(already-enc) failed: %v", err)
	}
	if got.Env != alreadyEncrypted {
		t.Errorf("already-enc env mismatch: got %q", got.Env)
	}
}

func TestBackendEnvEncryption_VerifyAdminEnv(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Create an encrypted backend
	b := &Backend{ID: "enc-be", Command: "echo", Env: `{"SECRET":"s3cr3t"}`, Enabled: true}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	// Create a legacy backend (no encrypted_env)
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, enabled, env) VALUES (?, ?, ?, ?, ?)`,
		"legacy-be", "echo test", 1, 1, `{"LEGACY":"old"}`,
	)
	if err != nil {
		t.Fatalf("insert legacy backend failed: %v", err)
	}

	success, fail, err := s.VerifyAdminEnvEncryption(context.Background())
	if err != nil {
		t.Fatalf("VerifyAdminEnvEncryption failed: %v", err)
	}
	if success != 1 {
		t.Errorf("expected 1 success, got %d", success)
	}
	if fail != 1 {
		t.Errorf("expected 1 fail (legacy), got %d", fail)
	}
}

func TestBackendEnvEncryption_NoKeyFallback(t *testing.T) {
	s, dir := testStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	// Without encryption key, env should be stored as plaintext
	envJSON := `{"NO_ENCRYPTION":"plaintext-stored"}`
	b := &Backend{
		ID:      "no-key-be",
		Command: "echo test",
		Env:     envJSON,
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	// Verify encrypted_env is empty
	var encryptedEnv string
	err := s.db.QueryRow(`SELECT COALESCE(encrypted_env, '') FROM backends WHERE id = ?`, b.ID).Scan(&encryptedEnv)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if encryptedEnv != "" {
		t.Error("encrypted_env should be empty when no encryption key is available")
	}

	// Verify env column has the plaintext
	var envCol string
	err = s.db.QueryRow(`SELECT env FROM backends WHERE id = ?`, b.ID).Scan(&envCol)
	if err != nil {
		t.Fatalf("query env column failed: %v", err)
	}
	if envCol != envJSON {
		t.Errorf("env column mismatch:\n  want: %s\n  got:  %s", envJSON, envCol)
	}

	// GetBackend should still return the correct env
	got, err := s.GetBackend(b.ID)
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.Env != envJSON {
		t.Errorf("GetBackend.Env mismatch:\n  want: %s\n  got:  %s", envJSON, got.Env)
	}
}

func TestBackendEnvEncryption_CreateThenGet(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	envJSON := `{"HOST":"db.example.com","PORT":"5432","PASSWORD":"s3cret!"}`
	b := &Backend{
		ID:      "full-be",
		Command: "echo full",
		Env:     envJSON,
		Enabled: true,
	}
	if err := s.CreateBackend(b); err != nil {
		t.Fatalf("CreateBackend failed: %v", err)
	}

	got, err := s.GetBackend("full-be")
	if err != nil {
		t.Fatalf("GetBackend failed: %v", err)
	}
	if got.ID != "full-be" {
		t.Errorf("ID mismatch: got %q", got.ID)
	}
	if got.Env != envJSON {
		t.Errorf("Env mismatch:\n  want: %s\n  got:  %s", envJSON, got.Env)
	}
	if got.EncryptedEnv == "" {
		t.Error("EncryptedEnv should be non-empty")
	}
}
