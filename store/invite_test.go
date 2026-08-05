package store

import (
	"os"
	"strings"
	"testing"
	"time"
)

func testInviteStore(t *testing.T) (*Store, string) {
	t.Helper()
	return testStore(t)
}

func TestGenerateInviteToken(t *testing.T) {
	raw1, hash1, err := GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	raw2, _, err := GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	if raw1 == "" || raw2 == "" {
		t.Error("raw tokens must be non-empty")
	}
	if raw1 == raw2 {
		t.Error("two generated tokens should differ")
	}
	if hash1 == raw1 {
		t.Error("hash must not equal the raw token")
	}
	if len(hash1) != 64 {
		t.Errorf("expected sha256 hex (64 chars), got %d", len(hash1))
	}
	// Deterministic: same raw token hashes identically
	h, _ := HashInviteToken(raw1)
	if h != hash1 {
		t.Error("HashInviteToken should be deterministic")
	}
}

func TestCreateInvite(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	inv := &Invite{
		Email:          "karl.dane@tuskerdirect.com",
		Name:           "Karl Dane",
		Role:           "admin",
		TokenHash:      strings.Repeat("a", 64),
		TokenExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
		InvitedBy:      "admin1",
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.ID == "" {
		t.Error("CreateInvite should assign an ID")
	}
	if inv.Status != InvitePending {
		t.Errorf("expected status %q, got %q", InvitePending, inv.Status)
	}
}

func TestCreateInvite_DuplicateTokenHash(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	inv := &Invite{
		Email:          "a@test.com",
		Role:           "user",
		TokenHash:      strings.Repeat("b", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	dup := &Invite{
		Email:          "c@test.com",
		Role:           "user",
		TokenHash:      strings.Repeat("b", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateInvite(dup); err == nil {
		t.Fatal("expected error for duplicate token hash")
	}
}

func TestGetInviteByTokenHash(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	inv := &Invite{
		Email:          "found@test.com",
		Role:           "admin",
		TokenHash:      strings.Repeat("c", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	got, err := s.GetInviteByTokenHash(strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("GetInviteByTokenHash: %v", err)
	}
	if got.Email != "found@test.com" {
		t.Errorf("expected email found@test.com, got %s", got.Email)
	}
	if got.Role != "admin" {
		t.Errorf("expected role admin, got %s", got.Role)
	}

	_, err = s.GetInviteByTokenHash(strings.Repeat("d", 64))
	if err == nil {
		t.Error("expected error for unknown token hash")
	}
}

func TestListInvites(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	for i := 0; i < 3; i++ {
		inv := &Invite{
			Email:          "u" + string(rune('a'+i)) + "@test.com",
			Role:           "user",
			TokenHash:      strings.Repeat(string(rune('e'+i)), 64),
			TokenExpiresAt: time.Now().UTC().Add(time.Hour),
		}
		if err := s.CreateInvite(inv); err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
	}

	invites, err := s.ListInvites()
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 3 {
		t.Errorf("expected 3 invites, got %d", len(invites))
	}
}

func TestMarkInviteAccepted(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	inv := &Invite{
		Email:          "accepted@test.com",
		Role:           "user",
		TokenHash:      strings.Repeat("f", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := s.MarkInviteAccepted(inv.ID, "user123"); err != nil {
		t.Fatalf("MarkInviteAccepted: %v", err)
	}

	got, err := s.GetInviteByTokenHash(strings.Repeat("f", 64))
	if err != nil {
		t.Fatalf("GetInviteByTokenHash: %v", err)
	}
	if got.Status != InviteAccepted {
		t.Errorf("expected status %q, got %q", InviteAccepted, got.Status)
	}
	if got.AcceptedUserID != "user123" {
		t.Errorf("expected accepted_user_id user123, got %s", got.AcceptedUserID)
	}
	if got.AcceptedAt.IsZero() {
		t.Error("expected accepted_at to be set")
	}
}

func TestRevokeInvite(t *testing.T) {
	s, dir := testInviteStore(t)
	defer os.RemoveAll(dir)
	defer s.Close()

	inv := &Invite{
		Email:          "revoke@test.com",
		Role:           "user",
		TokenHash:      strings.Repeat("g", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := s.RevokeInvite(inv.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	got, err := s.GetInviteByTokenHash(strings.Repeat("g", 64))
	if err != nil {
		t.Fatalf("GetInviteByTokenHash: %v", err)
	}
	if got.Status != InviteRevoked {
		t.Errorf("expected status %q, got %q", InviteRevoked, got.Status)
	}
}

func TestInviteExpired(t *testing.T) {
	now := time.Now().UTC()

	expired := &Invite{TokenExpiresAt: now.Add(-time.Minute)}
	if !expired.Expired(now) {
		t.Error("expected expired invite to be expired")
	}

	valid := &Invite{TokenExpiresAt: now.Add(time.Hour)}
	if valid.Expired(now) {
		t.Error("expected valid invite to not be expired")
	}

	accepted := &Invite{TokenExpiresAt: now.Add(-time.Hour), Status: InviteAccepted}
	if accepted.Expired(now) {
		t.Error("accepted invite should not be considered expired")
	}
}
