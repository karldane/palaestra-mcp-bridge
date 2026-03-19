package oracle

import (
	"fmt"
	"strings"
	"time"
)

// SchemaCache holds cached schema information
type SchemaCache struct {
	Tables        map[string]*TableInfo `json:"tables"`
	AllTableNames map[string]struct{}   `json:"all_table_names"`
	LastUpdated   time.Time             `json:"last_updated"`
}

// TableInfo holds information about a database table
type TableInfo struct {
	TableName     string                        `json:"table_name"`
	Columns       []ColumnInfo                  `json:"columns"`
	Relationships map[string][]RelationshipInfo `json:"relationships"`
	FullyLoaded   bool                          `json:"fully_loaded"`
}

// ColumnInfo holds information about a table column
type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"type"`
	Nullable bool   `json:"nullable"`
}

// RelationshipInfo holds foreign key relationship information
type RelationshipInfo struct {
	LocalColumn   string `json:"local_column"`
	ForeignColumn string `json:"foreign_column"`
	Direction     string `json:"direction"`
}

// ConstraintInfo holds constraint information
type ConstraintInfo struct {
	Name       string
	Type       string
	Columns    []string
	Condition  string
	References *ReferenceInfo
}

// ReferenceInfo holds foreign key reference information
type ReferenceInfo struct {
	Table   string
	Columns []string
}

// IndexInfo holds index information
type IndexInfo struct {
	Name    string
	Unique  bool
	Columns []string
	Status  string
}

// RelatedTables holds tables related by foreign keys
type RelatedTables struct {
	ReferencedTables  []string
	ReferencingTables []string
}

// QueryResult holds the result of a SELECT query
type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}
}

// WriteResult holds the result of a DML query
type WriteResult struct {
	RowsAffected int64
	Committed    bool
}

// ExplainPlan holds query execution plan information
type ExplainPlan struct {
	Steps       []string
	Suggestions []string
}

// FormatSchema formats the table schema as a string
func (t *TableInfo) FormatSchema() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Table: %s\n", t.TableName))
	sb.WriteString(strings.Repeat("=", 60))
	sb.WriteString("\n\n")

	// Columns
	sb.WriteString("Columns:\n")
	sb.WriteString(strings.Repeat("-", 40))
	sb.WriteString("\n")

	for _, col := range t.Columns {
		nullable := "NOT NULL"
		if col.Nullable {
			nullable = "NULL"
		}
		sb.WriteString(fmt.Sprintf("  %-30s %-15s %s\n", col.Name, col.DataType, nullable))
	}

	// Relationships
	if len(t.Relationships) > 0 {
		sb.WriteString("\nRelationships:\n")
		sb.WriteString(strings.Repeat("-", 40))
		sb.WriteString("\n")

		for refTable, rels := range t.Relationships {
			for _, rel := range rels {
				direction := "->"
				if rel.Direction == "INCOMING" {
					direction = "<-"
				}
				sb.WriteString(fmt.Sprintf("  %s %s %s (%s = %s.%s)\n",
					t.TableName, direction, refTable,
					rel.LocalColumn, refTable, rel.ForeignColumn))
			}
		}
	}

	return sb.String()
}
