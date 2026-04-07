package muxer

import (
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// testUser is a fully-populated user record used across template tests.
func testUser() *store.User {
	return &store.User{
		ID:    "abc123def456",
		Name:  "Alice Example",
		Email: "alice@Example.com",
		Role:  "user",
	}
}

// ---------- ResolveEnvTemplates ----------

func TestResolveEnvTemplates_PlainValuePassthrough(t *testing.T) {
	env := map[string]string{
		"PLAIN_KEY": "plain-value",
		"ANOTHER":   "no-template-here",
		"EMPTY_VAL": "something",
	}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for k, want := range env {
		if got[k] != want {
			t.Errorf("key %q: got %q, want %q", k, got[k], want)
		}
	}
}

func TestResolveEnvTemplates_SingleTemplateNoFunction(t *testing.T) {
	env := map[string]string{"MONGO_USER": "{{users.email}}"}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["MONGO_USER"] != "alice@Example.com" {
		t.Errorf("got %q, want %q", got["MONGO_USER"], "alice@Example.com")
	}
}

func TestResolveEnvTemplates_SingleTemplateOneFunction(t *testing.T) {
	env := map[string]string{"MONGO_DB": "{{users.email|sanitised}}"}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alice_at_example_com"
	if got["MONGO_DB"] != want {
		t.Errorf("got %q, want %q", got["MONGO_DB"], want)
	}
}

func TestResolveEnvTemplates_FunctionChaining(t *testing.T) {
	// lower then hashed — should hash the lowercased email
	env := map[string]string{"ID": "{{users.email|lower|hashed}}"}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Compute expected: lower("alice@Example.com") = "alice@example.com"
	// then hashed
	wantIntermediate := "alice@example.com"
	want := funcHashed(wantIntermediate)
	if got["ID"] != want {
		t.Errorf("got %q, want %q", got["ID"], want)
	}
}

func TestResolveEnvTemplates_MixedLiteralAndTemplate(t *testing.T) {
	env := map[string]string{"PREFIX_KEY": "prefix_{{users.id}}"}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "prefix_abc123def456"
	if got["PREFIX_KEY"] != want {
		t.Errorf("got %q, want %q", got["PREFIX_KEY"], want)
	}
}

func TestResolveEnvTemplates_MultipleTemplatesInOneValue(t *testing.T) {
	env := map[string]string{
		"COMBINED": "{{users.email|sanitised}}.{{users.id}}",
	}
	got, err := ResolveEnvTemplates(env, testUser())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "alice_at_example_com.abc123def456"
	if got["COMBINED"] != want {
		t.Errorf("got %q, want %q", got["COMBINED"], want)
	}
}

// ---------- Error cases ----------

func TestResolveEnvTemplates_UnknownSource(t *testing.T) {
	env := map[string]string{"BAD": "{{foo.bar}}"}
	_, err := ResolveEnvTemplates(env, testUser())
	if err == nil {
		t.Fatal("expected error for unknown source, got nil")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error %q does not mention source %q", err.Error(), "foo")
	}
}

func TestResolveEnvTemplates_UnknownField(t *testing.T) {
	env := map[string]string{"BAD": "{{users.nonexistent}}"}
	_, err := ResolveEnvTemplates(env, testUser())
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "users.nonexistent") {
		t.Errorf("error %q does not mention field name", err.Error())
	}
}

func TestResolveEnvTemplates_UnknownFunction(t *testing.T) {
	env := map[string]string{"BAD": "{{users.email|frobnicate}}"}
	_, err := ResolveEnvTemplates(env, testUser())
	if err == nil {
		t.Fatal("expected error for unknown function, got nil")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("error %q does not mention function name", err.Error())
	}
}

func TestResolveEnvTemplates_EmptyResolvedValue(t *testing.T) {
	// Role is "user" for testUser so it's non-empty — use a user with empty role.
	emptyRoleUser := &store.User{
		ID:    "xyz",
		Email: "bob@example.com",
		Role:  "", // empty
	}
	env := map[string]string{"ROLE_VAR": "{{users.role}}"}
	_, err := ResolveEnvTemplates(env, emptyRoleUser)
	if err == nil {
		t.Fatal("expected error for empty resolved value, got nil")
	}
	if !strings.Contains(err.Error(), "empty string") {
		t.Errorf("error %q does not mention empty string", err.Error())
	}
}

func TestResolveEnvTemplates_MalformedSyntax(t *testing.T) {
	env := map[string]string{"BAD": "prefix_{{users.email"}
	_, err := ResolveEnvTemplates(env, testUser())
	if err == nil {
		t.Fatal("expected error for malformed template, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error %q does not mention malformed", err.Error())
	}
}

// ---------- All four allowlisted fields ----------

func TestResolveEnvTemplates_AllAllowedFields(t *testing.T) {
	u := &store.User{
		ID:    "id-value-123",
		Email: "field@test.com",
		Role:  "admin",
	}
	cases := []struct {
		template string
		want     string
	}{
		{"{{users.email}}", "field@test.com"},
		{"{{users.username}}", "field@test.com"}, // username == email
		{"{{users.id}}", "id-value-123"},
		{"{{users.role}}", "admin"},
	}
	for _, tc := range cases {
		env := map[string]string{"K": tc.template}
		got, err := ResolveEnvTemplates(env, u)
		if err != nil {
			t.Errorf("template %q: unexpected error: %v", tc.template, err)
			continue
		}
		if got["K"] != tc.want {
			t.Errorf("template %q: got %q, want %q", tc.template, got["K"], tc.want)
		}
	}
}

// ---------- All five built-in functions ----------

func TestResolveEnvTemplates_AllBuiltinFunctions(t *testing.T) {
	u := &store.User{
		ID:    "id1",
		Email: "Alice@Example.com",
		Role:  "user",
	}
	cases := []struct {
		fn   string
		want string
	}{
		{"sanitised", "alice_at_example_com"},
		{"lower", "alice@example.com"},
		{"upper", "ALICE@EXAMPLE.COM"},
		{"urlencoded", "Alice%40Example.com"},
		// hashed: SHA-256 of lowercase "alice@example.com", first 8 bytes, hex, prefixed "u"
		{"hashed", funcHashed("Alice@Example.com")},
	}
	for _, tc := range cases {
		tpl := "{{users.email|" + tc.fn + "}}"
		env := map[string]string{"K": tpl}
		got, err := ResolveEnvTemplates(env, u)
		if err != nil {
			t.Errorf("function %q: unexpected error: %v", tc.fn, err)
			continue
		}
		if got["K"] != tc.want {
			t.Errorf("function %q: got %q, want %q", tc.fn, got["K"], tc.want)
		}
	}
}

// ---------- Unit tests for individual transform functions ----------

func TestFuncSanitised(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice@example.com", "alice_at_example_com"},
		{"Alice@Example.com", "alice_at_example_com"},
		{"no-special", "no-special"},
		{"multi.dots.here", "multi_dots_here"},
		{"a@b.c@d.e", "a_at_b_c_at_d_e"},
	}
	for _, tc := range cases {
		got := funcSanitised(tc.in)
		if got != tc.want {
			t.Errorf("funcSanitised(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFuncHashed(t *testing.T) {
	// Should be stable across calls and lowercased before hashing.
	h1 := funcHashed("alice@example.com")
	h2 := funcHashed("ALICE@EXAMPLE.COM") // same after lowercasing
	if h1 != h2 {
		t.Errorf("funcHashed is not case-insensitive: %q != %q", h1, h2)
	}
	if !strings.HasPrefix(h1, "u") {
		t.Errorf("funcHashed result %q does not start with 'u'", h1)
	}
	// Should be 1 + 16 hex chars = 17 characters total (8 bytes → 16 hex).
	if len(h1) != 17 {
		t.Errorf("funcHashed result %q has length %d, want 17", h1, len(h1))
	}
}
