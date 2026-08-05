package usertypes

import "testing"

func TestValidRoles(t *testing.T) {
	roles := ValidRoles()
	if len(roles) != 2 {
		t.Fatalf("expected 2 valid roles, got %d", len(roles))
	}
	seen := map[string]bool{}
	for _, r := range roles {
		seen[r.ID] = true
		if r.ID == "" {
			t.Error("role ID must not be empty")
		}
		if r.Label == "" {
			t.Errorf("role %q missing label", r.ID)
		}
	}
	if !seen["user"] {
		t.Error("expected 'user' role")
	}
	if !seen["admin"] {
		t.Error("expected 'admin' role")
	}
}

func TestIsValidRole(t *testing.T) {
	valid := []string{"user", "admin", "User", "ADMIN"}
	for _, r := range valid {
		if !IsValidRole(r) {
			t.Errorf("expected %q to be valid", r)
		}
	}
	invalid := []string{"superadmin", "", "root", "owner"}
	for _, r := range invalid {
		if IsValidRole(r) {
			t.Errorf("expected %q to be invalid", r)
		}
	}
}

func TestRoleLabel(t *testing.T) {
	if got := RoleLabel("user"); got != "User" {
		t.Errorf("RoleLabel(user) = %q, want User", got)
	}
	if got := RoleLabel("admin"); got != "Admin" {
		t.Errorf("RoleLabel(admin) = %q, want Admin", got)
	}
	if got := RoleLabel("nonexistent"); got != "nonexistent" {
		t.Errorf("RoleLabel(nonexistent) = %q, want nonexistent", got)
	}
}

func TestNormalizeRole(t *testing.T) {
	if got := NormalizeRole("ADMIN"); got != "admin" {
		t.Errorf("NormalizeRole(ADMIN) = %q, want admin", got)
	}
	if got := NormalizeRole("user"); got != "user" {
		t.Errorf("NormalizeRole(user) = %q, want user", got)
	}
	if got := NormalizeRole("superadmin"); got != "user" {
		t.Errorf("NormalizeRole(superadmin) = %q, want user", got)
	}
	if got := NormalizeRole(""); got != "user" {
		t.Errorf("NormalizeRole() = %q, want user", got)
	}
}
