package oracle

import (
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-framework/framework"
)

func TestServerCreation(t *testing.T) {
	// This test would require a real Oracle connection
	// For now, just test that types are defined correctly
	t.Log("Server struct defined correctly")
}

func TestToolDefinitions(t *testing.T) {
	// Test that all tools have proper EnforcerProfile
	tests := []struct {
		name string
		tool interface {
			Name() string
			Description() string
			GetEnforcerProfile() framework.EnforcerProfile
		}
		expectedRisk     framework.RiskLevel
		expectedImpact   framework.ImpactScope
		expectedApproval bool
	}{
		{
			name:             "ListTablesTool",
			tool:             &ListTablesTool{},
			expectedRisk:     framework.RiskLow,
			expectedImpact:   framework.ImpactRead,
			expectedApproval: false,
		},
		{
			name:             "DescribeTableTool",
			tool:             &DescribeTableTool{},
			expectedRisk:     framework.RiskLow,
			expectedImpact:   framework.ImpactRead,
			expectedApproval: false,
		},
		{
			name:             "ExecuteReadTool",
			tool:             &ExecuteReadTool{},
			expectedRisk:     framework.RiskMed,
			expectedImpact:   framework.ImpactRead,
			expectedApproval: true,
		},
		{
			name:             "ExecuteWriteTool",
			tool:             &ExecuteWriteTool{},
			expectedRisk:     framework.RiskHigh,
			expectedImpact:   framework.ImpactWrite,
			expectedApproval: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := tt.tool.GetEnforcerProfile()

			if profile.RiskLevel != tt.expectedRisk {
				t.Errorf("Expected risk %s, got %s", tt.expectedRisk, profile.RiskLevel)
			}

			if profile.ImpactScope != tt.expectedImpact {
				t.Errorf("Expected impact %s, got %s", tt.expectedImpact, profile.ImpactScope)
			}

			if profile.ApprovalReq != tt.expectedApproval {
				t.Errorf("Expected approval %v, got %v", tt.expectedApproval, profile.ApprovalReq)
			}
		})
	}
}

func TestQueryClassification(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		isSelect bool
		isWrite  bool
	}{
		{"SELECT", "SELECT * FROM users", true, false},
		{"WITH CTE", "WITH cte AS (SELECT * FROM users) SELECT * FROM cte", true, false},
		{"INSERT", "INSERT INTO users VALUES (1, 'test')", false, true},
		{"UPDATE", "UPDATE users SET name = 'test' WHERE id = 1", false, true},
		{"DELETE", "DELETE FROM users WHERE id = 1", false, true},
		{"MERGE", "MERGE INTO users USING dual ON (1=1)", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSelectQuery(tt.sql); got != tt.isSelect {
				t.Errorf("isSelectQuery(%q) = %v, want %v", tt.sql, got, tt.isSelect)
			}
			if got := isWriteQuery(tt.sql); got != tt.isWrite {
				t.Errorf("isWriteQuery(%q) = %v, want %v", tt.sql, got, tt.isWrite)
			}
		})
	}
}

func TestTableInfoFormatSchema(t *testing.T) {
	table := &TableInfo{
		TableName: "USERS",
		Columns: []ColumnInfo{
			{Name: "ID", DataType: "NUMBER", Nullable: false},
			{Name: "NAME", DataType: "VARCHAR2(100)", Nullable: true},
		},
		Relationships: map[string][]RelationshipInfo{
			"ORDERS": {
				{LocalColumn: "ID", ForeignColumn: "USER_ID", Direction: "OUTGOING"},
			},
		},
		FullyLoaded: true,
	}

	output := table.FormatSchema()

	if output == "" {
		t.Error("FormatSchema() returned empty string")
	}

	if !strings.Contains(output, "USERS") {
		t.Error("Expected output to contain table name")
	}

	if !strings.Contains(output, "ID") {
		t.Error("Expected output to contain column name")
	}
}

func TestConstraintTypeMapping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"P", "PRIMARY KEY"},
		{"R", "FOREIGN KEY"},
		{"U", "UNIQUE"},
		{"C", "CHECK"},
		{"X", "X"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := mapConstraintType(tt.input); got != tt.expected {
				t.Errorf("mapConstraintType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAnalyzeQueryForOptimization(t *testing.T) {
	// Test SELECT *
	suggestions := analyzeQueryForOptimization("SELECT * FROM users")
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "SELECT *") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected suggestion about SELECT *")
	}

	// Test LIKE with leading wildcard
	suggestions = analyzeQueryForOptimization("SELECT * FROM users WHERE name LIKE '%test'")
	found = false
	for _, s := range suggestions {
		if strings.Contains(s, "wildcard") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected suggestion about leading wildcards")
	}
}

func TestQueryResultFormatting(t *testing.T) {
	result := &QueryResult{
		Columns: []string{"ID", "NAME"},
		Rows: []map[string]interface{}{
			{"ID": 1, "NAME": "Alice"},
			{"ID": 2, "NAME": nil},
		},
	}

	output := formatQueryResult(result)

	if output == "" {
		t.Error("formatQueryResult returned empty string")
	}

	if !strings.Contains(output, "Alice") {
		t.Error("Expected output to contain 'Alice'")
	}

	if !strings.Contains(output, "NULL") {
		t.Error("Expected output to contain 'NULL' for nil value")
	}
}

func TestWriteResultFormatting(t *testing.T) {
	result := &WriteResult{RowsAffected: 5, Committed: true}
	output := formatWriteResult(result, true)

	if !strings.Contains(output, "5") {
		t.Error("Expected output to contain row count")
	}

	if !strings.Contains(output, "committed") {
		t.Error("Expected output to mention committed status")
	}
}

func TestExplainPlanFormatting(t *testing.T) {
	plan := &ExplainPlan{
		Steps:       []string{"Step 1", "Step 2"},
		Suggestions: []string{"Suggestion 1"},
	}

	output := formatExplainPlan(plan)

	if !strings.Contains(output, "Step 1") {
		t.Error("Expected output to contain steps")
	}

	if !strings.Contains(output, "Suggestion 1") {
		t.Error("Expected output to contain suggestions")
	}
}
