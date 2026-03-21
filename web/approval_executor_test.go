package web

import (
	"strings"
	"testing"
)

func TestStripToolPrefix(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		prefix   string
		expected string
	}{
		{"empty prefix", "delete_file", "", "delete_file"},
		{"underscore prefix", "github_delete_file", "github", "delete_file"},
		{"slash prefix", "github/delete_file", "github", "delete_file"},
		{"no match", "other_delete_file", "github", "other_delete_file"},
		{"exact match prefix", "github", "github", "github"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripToolPrefix(tt.toolName, tt.prefix)
			if result != tt.expected {
				t.Errorf("stripToolPrefix(%q, %q) = %q, want %q", tt.toolName, tt.prefix, result, tt.expected)
			}
		})
	}
}

func TestStripPrefixFromRequestBody(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		prefix           string
		expectedToolName string // Tool name that should appear in result
	}{
		{
			name:             "strip github prefix",
			body:             `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github_delete_file","arguments":{}}}`,
			prefix:           "github",
			expectedToolName: "delete_file",
		},
		{
			name:             "no prefix needed",
			body:             `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{}}}`,
			prefix:           "github",
			expectedToolName: "delete_file",
		},
		{
			name:             "empty prefix",
			body:             `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{}}}`,
			prefix:           "",
			expectedToolName: "delete_file",
		},
		{
			name:             "invalid json",
			body:             `not json`,
			prefix:           "github",
			expectedToolName: "", // Should return unchanged - empty means no check
		},
		{
			name:             "no params",
			body:             `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
			prefix:           "github",
			expectedToolName: "", // Should return unchanged
		},
		{
			name:             "atlassian prefix",
			body:             `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"atlassian_create_issue","arguments":{}}}`,
			prefix:           "atlassian",
			expectedToolName: "create_issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripPrefixFromRequestBody(tt.body, tt.prefix)

			if tt.expectedToolName == "" {
				// Should return unchanged
				if result != tt.body {
					t.Errorf("stripPrefixFromRequestBody(%q, %q) = %q, want unchanged %q", tt.body, tt.prefix, result, tt.body)
				}
				return
			}

			// Check that the expected tool name is in the result
			expectedJSON := `"name":"` + tt.expectedToolName + `"`
			if !strings.Contains(result, expectedJSON) {
				t.Errorf("stripPrefixFromRequestBody(%q, %q) = %q, want to contain %q", tt.body, tt.prefix, result, expectedJSON)
			}
		})
	}
}
