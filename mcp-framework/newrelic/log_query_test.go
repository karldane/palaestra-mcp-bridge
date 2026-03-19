package newrelic

import (
	"testing"
)

func TestParseLogQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "simple field value",
			input:    "level:ERROR",
			expected: "level = 'ERROR'",
		},
		{
			name:     "field with hyphen",
			input:    "service:my-app",
			expected: "service = 'my-app'",
		},
		{
			name:     "quoted string value",
			input:    `message:"error message"`,
			expected: "message = 'error message'",
		},
		{
			name:     "AND operator",
			input:    "level:ERROR AND service:myapp",
			expected: "(level = 'ERROR' AND service = 'myapp')",
		},
		{
			name:     "OR operator",
			input:    "level:ERROR OR level:WARN",
			expected: "(level = 'ERROR' OR level = 'WARN')",
		},
		{
			name:     "NOT operator",
			input:    "NOT level:DEBUG",
			expected: "NOT (level = 'DEBUG')",
		},
		{
			name:     "complex query",
			input:    "level:ERROR AND service:myapp AND NOT message:test",
			expected: "(level = 'ERROR' AND service = 'myapp' AND NOT (message = 'test'))",
		},
		{
			name:     "numeric value",
			input:    "status:500",
			expected: "status = '500'",
		},
		{
			name:     "empty query",
			input:    "",
			expected: "",
		},
		{
			name:     "simple text search",
			input:    "error",
			expected: "message LIKE '%error%'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLogQuery(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLogQuery() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("parseLogQuery() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestEscapeString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"it's", "it''s"},
		{"path\\to\\file", "path\\\\to\\\\file"},
		{"value\"quoted\"", "value\"quoted\""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeString(tt.input)
			if result != tt.expected {
				t.Errorf("escapeString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
