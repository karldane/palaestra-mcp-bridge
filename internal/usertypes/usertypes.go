// Package usertypes is the single source of truth for the set of user roles
// (user types) supported by the system. It exists so that new roles added in
// future (e.g. read-only, auditor) flow through every UI that reads from it.
package usertypes

import "strings"

// Role describes a user type that can be assigned to an account.
type Role struct {
	ID    string // canonical, lowercase identifier stored in the DB
	Label string // human-readable label for UI display
}

// roles is the canonical, ordered list of user types.
var roles = []Role{
	{ID: "user", Label: "User"},
	{ID: "admin", Label: "Admin"},
}

// ValidRoles returns a copy of the supported user types.
func ValidRoles() []Role {
	out := make([]Role, len(roles))
	copy(out, roles)
	return out
}

// IsValidRole reports whether the given role identifier (case-insensitive) is
// one of the supported user types.
func IsValidRole(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, r := range roles {
		if r.ID == id {
			return true
		}
	}
	return false
}

// NormalizeRole returns the canonical lowercase role ID for the given input,
// falling back to the default "user" role when the input is not a supported
// user type.
func NormalizeRole(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, r := range roles {
		if r.ID == id {
			return r.ID
		}
	}
	return roles[0].ID
}

// RoleLabel returns the display label for a role ID. If the role is unknown,
// the input is returned unchanged.
func RoleLabel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, r := range roles {
		if r.ID == id {
			return r.Label
		}
	}
	return id
}
