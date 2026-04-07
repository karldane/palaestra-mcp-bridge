package muxer

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// templatePattern matches {{...}} expressions anywhere in a string.
// The inner content must not contain a closing brace.
var templatePattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// templateFuncs is the complete registry of pipe functions available in
// template expressions. Add new entries here — no other code changes needed.
var templateFuncs = map[string]func(string) string{
	"sanitised":  funcSanitised,
	"hashed":     funcHashed,
	"lower":      strings.ToLower,
	"upper":      strings.ToUpper,
	"urlencoded": url.QueryEscape,
}

// allowedFields is the complete allowlist of template source fields.
// Only fields listed here may be referenced in a {{...}} expression.
// "users.username" intentionally maps to Email — usernames in this bridge
// are always the email address.
var allowedFields = map[string]func(*store.User) string{
	"users.email":    func(u *store.User) string { return u.Email },
	"users.username": func(u *store.User) string { return u.Email },
	"users.id":       func(u *store.User) string { return u.ID },
	"users.role":     func(u *store.User) string { return u.Role },
}

// ResolveEnvTemplates replaces all {{...}} expressions in the env map values
// with values derived from the given user record. Non-template values are
// passed through unchanged.
//
// Resolution rules:
//   - {{source.field}} — field value from the allowlist
//   - {{source.field|fn}} — field value passed through fn
//   - {{source.field|fn1|fn2}} — functions applied left-to-right
//   - Literal text surrounding expressions is preserved
//
// Returns an error if any template cannot be fully resolved. A literal
// {{...}} string must never reach the backend process.
func ResolveEnvTemplates(env map[string]string, user *store.User) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		resolved, err := resolveValue(k, v, user)
		if err != nil {
			return nil, err
		}
		out[k] = resolved
	}
	return out, nil
}

// resolveValue resolves all template expressions in a single env var value.
// envKey is only used to produce descriptive error messages.
func resolveValue(envKey, val string, user *store.User) (string, error) {
	// Fast path: no template syntax present.
	if !strings.Contains(val, "{{") {
		return val, nil
	}

	// Check for malformed (unclosed) template.
	open := strings.Count(val, "{{")
	close := strings.Count(val, "}}")
	if open != close {
		return "", fmt.Errorf("env var %q: malformed template expression (mismatched {{ }})", envKey)
	}

	var resolveErr error
	result := templatePattern.ReplaceAllStringFunc(val, func(match string) string {
		if resolveErr != nil {
			return ""
		}
		inner := strings.TrimSpace(match[2 : len(match)-2]) // strip {{ }}
		parts := strings.Split(inner, "|")
		fieldKey := strings.TrimSpace(parts[0])

		// Validate source prefix (everything before the first dot).
		dotIdx := strings.Index(fieldKey, ".")
		if dotIdx < 0 {
			resolveErr = fmt.Errorf("env var %q: malformed template expression %q (missing source prefix)", envKey, match)
			return ""
		}
		source := fieldKey[:dotIdx]
		if source != "users" {
			resolveErr = fmt.Errorf("env var %q: unknown template source %q", envKey, source)
			return ""
		}

		extractor, ok := allowedFields[fieldKey]
		if !ok {
			resolveErr = fmt.Errorf("env var %q: unknown template field %q", envKey, fieldKey)
			return ""
		}

		resolved := extractor(user)
		if resolved == "" {
			resolveErr = fmt.Errorf("env var %q: template {{%s}} resolved to empty string", envKey, fieldKey)
			return ""
		}

		for _, fn := range parts[1:] {
			fn = strings.TrimSpace(fn)
			f, ok := templateFuncs[fn]
			if !ok {
				resolveErr = fmt.Errorf("env var %q: unknown template function %q", envKey, fn)
				return ""
			}
			resolved = f(resolved)
		}
		return resolved
	})
	if resolveErr != nil {
		return "", resolveErr
	}
	return result, nil
}

// envMapHasTemplates returns true if any value in the map contains a
// potential {{...}} template expression. Used as a cheap fast-path check
// to avoid a DB round-trip when no templates are configured.
func envMapHasTemplates(env map[string]string) bool {
	for _, v := range env {
		if strings.Contains(v, "{{") {
			return true
		}
	}
	return false
}

// funcSanitised produces a string safe for use as a database/resource name:
// lowercase, @ replaced with _at_, dots replaced with underscores.
func funcSanitised(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "@", "_at_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

// funcHashed returns a short, stable, opaque identifier derived from the
// input: SHA-256, first 8 bytes, lowercase hex, prefixed with "u".
func funcHashed(s string) string {
	h := sha256.Sum256([]byte(strings.ToLower(s)))
	return fmt.Sprintf("u%x", h[:8])
}
