package store

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// testDEK returns a fixed 32-byte user-derived key for store tests.
func testDEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestUser2FA_TableExists(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	var name string
	err := s.db.QueryRow(`SELECT name FROM pragma_table_info('user_2fa') WHERE name='user_id'`).Scan(&name)
	if err == sql.ErrNoRows {
		t.Error("user_2fa table should exist with user_id column")
	} else if err != nil {
		t.Fatalf("unexpected error checking column: %v", err)
	}
}

func TestSetGetUser2FA_NoConfig(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	method, enabled, err := s.GetUser2FA("nonexistent")
	if err != nil {
		t.Fatalf("GetUser2FA: %v", err)
	}
	if enabled || method != "" {
		t.Errorf("expected no 2FA, got method=%q enabled=%v", method, enabled)
	}
}

func TestSetGetUser2FA_RoundTrip(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	dek := testDEK()
	if err := s.SetUser2FA("u1", "totp", []byte("JBSWY3DPEHPK3PXP"), dek); err != nil {
		t.Fatalf("SetUser2FA: %v", err)
	}

	method, enabled, err := s.GetUser2FA("u1")
	if err != nil {
		t.Fatalf("GetUser2FA: %v", err)
	}
	if !enabled || method != "totp" {
		t.Errorf("expected totp enabled, got method=%q enabled=%v", method, enabled)
	}

	secret, err := s.GetUser2FASecret("u1", dek)
	if err != nil {
		t.Fatalf("GetUser2FASecret: %v", err)
	}
	if secret != "JBSWY3DPEHPK3PXP" {
		t.Errorf("unexpected decrypted secret %q", secret)
	}
}

func TestGetUser2FASecret_WrongDEK(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	dek := testDEK()
	s.SetUser2FA("u1", "totp", []byte("JBSWY3DPEHPK3PXP"), dek)

	wrong := make([]byte, 32)
	for i := range wrong {
		wrong[i] = byte(i + 1)
	}
	if _, err := s.GetUser2FASecret("u1", wrong); err == nil {
		t.Error("expected error for wrong DEK (no master-only fallback)")
	}
}

func TestSetUser2FA_NilDEK(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	if err := s.SetUser2FA("u1", "totp", []byte("SECRET"), nil); err == nil {
		t.Error("expected error when userDEK is nil")
	}
}

func TestSetUser2FA_UpdateExisting(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	dek := testDEK()
	if err := s.SetUser2FA("u1", "totp", []byte("OLD"), dek); err != nil {
		t.Fatalf("first set: %v", err)
	}
	// Reconfigure with a new secret.
	if err := s.SetUser2FA("u1", "totp", []byte("NEWSECRET"), dek); err != nil {
		t.Fatalf("second set: %v", err)
	}
	secret, _ := s.GetUser2FASecret("u1", dek)
	if secret != "NEWSECRET" {
		t.Errorf("expected updated secret, got %q", secret)
	}
}

func TestDeleteUser2FA_RemovesConfig(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	dek := testDEK()
	s.SetUser2FA("u1", "totp", []byte("JBSWY3DPEHPK3PXP"), dek)

	if err := s.DeleteUser2FA("u1"); err != nil {
		t.Fatalf("DeleteUser2FA: %v", err)
	}

	method, enabled, _ := s.GetUser2FA("u1")
	if enabled || method != "" {
		t.Errorf("expected 2FA removed, got method=%q enabled=%v", method, enabled)
	}
	if _, err := s.GetUser2FASecret("u1", dek); err == nil {
		t.Error("expected error after delete")
	}
}

func TestRevokeWebSessions(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)
	u2 := &User{ID: "u2", Name: "B", Email: "b@x.com", Password: "pw"}
	s.CreateUser(u2)

	s.CreateWebSession(&WebSession{UserID: "u1", ExpiresAt: time.Now().Add(time.Hour).UTC()})
	s.CreateWebSession(&WebSession{UserID: "u1", ExpiresAt: time.Now().Add(time.Hour).UTC()})
	s.CreateWebSession(&WebSession{UserID: "u2", ExpiresAt: time.Now().Add(time.Hour).UTC()})

	if err := s.RevokeWebSessions("u1"); err != nil {
		t.Fatalf("RevokeWebSessions: %v", err)
	}

	var remaining int
	s.db.QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE user_id='u1'`).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("expected u1 sessions revoked, got %d", remaining)
	}
	var u2count int
	s.db.QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE user_id='u2'`).Scan(&u2count)
	if u2count != 1 {
		t.Errorf("expected u2 sessions untouched, got %d", u2count)
	}
}

func TestUser2FA_CascadeOnUserDelete(t *testing.T) {
	s, dir, _ := testStoreWithCrypto(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	u := &User{ID: "u1", Name: "A", Email: "a@x.com", Password: "pw"}
	s.CreateUser(u)

	dek := testDEK()
	s.SetUser2FA("u1", "totp", []byte("JBSWY3DPEHPK3PXP"), dek)

	if err := s.DeleteUser("u1"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM user_2fa WHERE user_id='u1'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected user_2fa row cascaded away, got %d", count)
	}
}
