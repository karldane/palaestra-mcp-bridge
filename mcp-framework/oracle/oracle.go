// Package oracle provides an MCP backend for Oracle databases
// This is a Go-native port of the oracle-mcp-server with EnforcerProfile safety metadata
package oracle

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mcp-bridge/mcp-framework/framework"
)

// Server provides the Oracle MCP server functionality
type Server struct {
	*framework.Server
	db       *Database
	readOnly bool
}

// NewServer creates a new Oracle MCP server
func NewServer(connString string, readOnly bool) (*Server, error) {
	db, err := NewDatabase(connString, readOnly)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Oracle: %w", err)
	}

	config := &framework.Config{
		Name:         "oracle-mcp",
		Version:      "1.0.0",
		Instructions: "Oracle MCP Server with schema introspection and query execution tools.",
	}

	s := &Server{
		Server:   framework.NewServerWithConfig(config),
		db:       db,
		readOnly: readOnly,
	}

	s.registerTools()
	return s, nil
}

// Initialize initializes the schema cache
func (s *Server) Initialize() error {
	if err := s.db.InitializeSchemaCache(); err != nil {
		return fmt.Errorf("failed to initialize schema cache: %w", err)
	}
	s.Server.Initialize()
	return nil
}

// Close closes the database connection
func (s *Server) Close() error {
	return s.db.Close()
}

func (s *Server) registerTools() {
	// Read-only schema introspection tools
	s.RegisterTool(&ListTablesTool{db: s.db})
	s.RegisterTool(&DescribeTableTool{db: s.db})
	s.RegisterTool(&SearchTablesTool{db: s.db})
	s.RegisterTool(&SearchColumnsTool{db: s.db})
	s.RegisterTool(&GetConstraintsTool{db: s.db})
	s.RegisterTool(&GetIndexesTool{db: s.db})
	s.RegisterTool(&GetRelatedTablesTool{db: s.db})
	s.RegisterTool(&ExplainQueryTool{db: s.db})

	// Query execution tools
	s.RegisterTool(&ExecuteReadTool{db: s.db})

	// Write tool - disabled by default
	s.RegisterTool(&ExecuteWriteTool{db: s.db, server: s})
}

// IsReadOnly returns whether the server is in read-only mode
func (s *Server) IsReadOnly() bool {
	return s.readOnly
}

// RebuildSchemaCache rebuilds the schema cache
func (s *Server) RebuildSchemaCache() error {
	return s.db.RebuildSchemaCache()
}

// ListTablesTool lists all tables in the database
type ListTablesTool struct {
	db *Database
}

func (t *ListTablesTool) Name() string {
	return "oracle_list_tables"
}

func (t *ListTablesTool) Description() string {
	return "List all tables in the Oracle database schema"
}

func (t *ListTablesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of tables to return (default: 100)",
				"default":     100,
			},
		},
	}
}

func (t *ListTablesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	limit := 100
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	tables, err := t.db.GetAllTableNames(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list tables: %w", err)
	}

	if len(tables) > limit {
		tables = tables[:limit]
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Found %d tables:\n\n", len(tables)))
	for _, table := range tables {
		result.WriteString(fmt.Sprintf("- %s\n", table))
	}

	return result.String(), nil
}

func (t *ListTablesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(2),
		framework.WithPII(false),
	)
}

// DescribeTableTool returns detailed schema information for a table
type DescribeTableTool struct {
	db *Database
}

func (t *DescribeTableTool) Name() string {
	return "oracle_describe_table"
}

func (t *DescribeTableTool) Description() string {
	return "Get detailed schema information for a specific table (columns, relationships)"
}

func (t *DescribeTableTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the table to describe",
			},
		},
		Required: []string{"table_name"},
	}
}

func (t *DescribeTableTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	tableName, ok := args["table_name"].(string)
	if !ok || tableName == "" {
		return "", fmt.Errorf("table_name is required")
	}

	tableInfo, err := t.db.GetTableInfo(ctx, tableName)
	if err != nil {
		return "", err
	}

	if tableInfo == nil {
		return fmt.Sprintf("Table '%s' not found in the schema.", tableName), nil
	}

	return tableInfo.FormatSchema(), nil
}

func (t *DescribeTableTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

// SearchTablesTool searches for tables by name pattern
type SearchTablesTool struct {
	db *Database
}

func (t *SearchTablesTool) Name() string {
	return "oracle_search_tables"
}

func (t *SearchTablesTool) Description() string {
	return "Search for tables by name pattern (case-insensitive substring match)"
}

func (t *SearchTablesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"search_term": map[string]interface{}{
				"type":        "string",
				"description": "Search term to match against table names",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of results (default: 20)",
				"default":     20,
			},
		},
		Required: []string{"search_term"},
	}
}

func (t *SearchTablesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	searchTerm, ok := args["search_term"].(string)
	if !ok || searchTerm == "" {
		return "", fmt.Errorf("search_term is required")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	tables, err := t.db.SearchTables(ctx, searchTerm, limit)
	if err != nil {
		return "", fmt.Errorf("failed to search tables: %w", err)
	}

	if len(tables) == 0 {
		return fmt.Sprintf("No tables found matching '%s'", searchTerm), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Found %d tables matching '%s':\n\n", len(tables), searchTerm))
	for _, table := range tables {
		result.WriteString(fmt.Sprintf("- %s\n", table))
	}

	return result.String(), nil
}

func (t *SearchTablesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

// SearchColumnsTool searches for columns across all tables
type SearchColumnsTool struct {
	db *Database
}

func (t *SearchColumnsTool) Name() string {
	return "oracle_search_columns"
}

func (t *SearchColumnsTool) Description() string {
	return "Search for columns by name pattern across all tables"
}

func (t *SearchColumnsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"search_term": map[string]interface{}{
				"type":        "string",
				"description": "Search term to match against column names",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum number of tables to return (default: 50)",
				"default":     50,
			},
		},
		Required: []string{"search_term"},
	}
}

func (t *SearchColumnsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	searchTerm, ok := args["search_term"].(string)
	if !ok || searchTerm == "" {
		return "", fmt.Errorf("search_term is required")
	}

	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	columns, err := t.db.SearchColumns(ctx, searchTerm, limit)
	if err != nil {
		return "", fmt.Errorf("failed to search columns: %w", err)
	}

	if len(columns) == 0 {
		return fmt.Sprintf("No columns found matching '%s'", searchTerm), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Found columns matching '%s' in %d tables:\n\n", searchTerm, len(columns)))

	count := 0
	for tableName, cols := range columns {
		if count >= limit {
			break
		}
		result.WriteString(fmt.Sprintf("Table: %s\n", tableName))
		for _, col := range cols {
			nullable := "NOT NULL"
			if col.Nullable {
				nullable = "NULL"
			}
			result.WriteString(fmt.Sprintf("  - %s: %s (%s)\n", col.Name, col.DataType, nullable))
		}
		result.WriteString("\n")
		count++
	}

	return result.String(), nil
}

func (t *SearchColumnsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(4),
		framework.WithPII(false),
	)
}

// GetConstraintsTool returns constraints for a table
type GetConstraintsTool struct {
	db *Database
}

func (t *GetConstraintsTool) Name() string {
	return "oracle_get_constraints"
}

func (t *GetConstraintsTool) Description() string {
	return "Get all constraints (PK, FK, UNIQUE, CHECK) for a table"
}

func (t *GetConstraintsTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the table",
			},
		},
		Required: []string{"table_name"},
	}
}

func (t *GetConstraintsTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	tableName, ok := args["table_name"].(string)
	if !ok || tableName == "" {
		return "", fmt.Errorf("table_name is required")
	}

	constraints, err := t.db.GetConstraints(ctx, tableName)
	if err != nil {
		return "", fmt.Errorf("failed to get constraints: %w", err)
	}

	if len(constraints) == 0 {
		return fmt.Sprintf("No constraints found for table '%s'", tableName), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Constraints for table '%s':\n\n", tableName))

	for _, c := range constraints {
		result.WriteString(fmt.Sprintf("%s Constraint: %s\n", c.Type, c.Name))
		if len(c.Columns) > 0 {
			result.WriteString(fmt.Sprintf("  Columns: %s\n", strings.Join(c.Columns, ", ")))
		}
		if c.References != nil {
			result.WriteString(fmt.Sprintf("  References: %s(%s)\n", c.References.Table, strings.Join(c.References.Columns, ", ")))
		}
		if c.Condition != "" {
			result.WriteString(fmt.Sprintf("  Condition: %s\n", c.Condition))
		}
		result.WriteString("\n")
	}

	return result.String(), nil
}

func (t *GetConstraintsTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

// GetIndexesTool returns indexes for a table
type GetIndexesTool struct {
	db *Database
}

func (t *GetIndexesTool) Name() string {
	return "oracle_get_indexes"
}

func (t *GetIndexesTool) Description() string {
	return "Get all indexes for a table"
}

func (t *GetIndexesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the table",
			},
		},
		Required: []string{"table_name"},
	}
}

func (t *GetIndexesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	tableName, ok := args["table_name"].(string)
	if !ok || tableName == "" {
		return "", fmt.Errorf("table_name is required")
	}

	indexes, err := t.db.GetIndexes(ctx, tableName)
	if err != nil {
		return "", fmt.Errorf("failed to get indexes: %w", err)
	}

	if len(indexes) == 0 {
		return fmt.Sprintf("No indexes found for table '%s'", tableName), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Indexes for table '%s':\n\n", tableName))

	for _, idx := range indexes {
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		result.WriteString(fmt.Sprintf("%sIndex: %s\n", unique, idx.Name))
		result.WriteString(fmt.Sprintf("  Columns: %s\n", strings.Join(idx.Columns, ", ")))
		if idx.Status != "" {
			result.WriteString(fmt.Sprintf("  Status: %s\n", idx.Status))
		}
		result.WriteString("\n")
	}

	return result.String(), nil
}

func (t *GetIndexesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

// GetRelatedTablesTool returns tables related by foreign keys
type GetRelatedTablesTool struct {
	db *Database
}

func (t *GetRelatedTablesTool) Name() string {
	return "oracle_get_related_tables"
}

func (t *GetRelatedTablesTool) Description() string {
	return "Get tables related to the specified table through foreign keys"
}

func (t *GetRelatedTablesTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"table_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the table",
			},
		},
		Required: []string{"table_name"},
	}
}

func (t *GetRelatedTablesTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	tableName, ok := args["table_name"].(string)
	if !ok || tableName == "" {
		return "", fmt.Errorf("table_name is required")
	}

	related, err := t.db.GetRelatedTables(ctx, tableName)
	if err != nil {
		return "", fmt.Errorf("failed to get related tables: %w", err)
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Tables related to '%s':\n\n", tableName))

	if len(related.ReferencedTables) > 0 {
		result.WriteString("Tables referenced by this table (outgoing foreign keys):\n")
		for _, table := range related.ReferencedTables {
			result.WriteString(fmt.Sprintf("  - %s\n", table))
		}
		result.WriteString("\n")
	}

	if len(related.ReferencingTables) > 0 {
		result.WriteString("Tables that reference this table (incoming foreign keys):\n")
		for _, table := range related.ReferencingTables {
			result.WriteString(fmt.Sprintf("  - %s\n", table))
		}
	}

	if len(related.ReferencedTables) == 0 && len(related.ReferencingTables) == 0 {
		result.WriteString("No related tables found.\n")
	}

	return result.String(), nil
}

func (t *GetRelatedTablesTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(3),
		framework.WithPII(false),
	)
}

// ExecuteReadTool executes SELECT queries
type ExecuteReadTool struct {
	db *Database
}

func (t *ExecuteReadTool) Name() string {
	return "oracle_execute_read"
}

func (t *ExecuteReadTool) Description() string {
	return "Execute a read-only SELECT query (limited to 100 rows by default)"
}

func (t *ExecuteReadTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"sql": map[string]interface{}{
				"type":        "string",
				"description": "SELECT SQL query to execute",
			},
			"max_rows": map[string]interface{}{
				"type":        "number",
				"description": "Maximum rows to return (default: 100, max: 1000)",
				"default":     100,
			},
		},
		Required: []string{"sql"},
	}
}

func (t *ExecuteReadTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	sql, ok := args["sql"].(string)
	if !ok || sql == "" {
		return "", fmt.Errorf("sql is required")
	}

	maxRows := 100
	if mr, ok := args["max_rows"].(float64); ok {
		maxRows = int(mr)
		if maxRows > 1000 {
			maxRows = 1000
		}
	}

	// Ensure it's a SELECT query
	if !isSelectQuery(sql) {
		return "", fmt.Errorf("only SELECT queries are allowed with oracle_execute_read. Use oracle_execute_write for DML statements.")
	}

	result, err := t.db.ExecuteQuery(ctx, sql, maxRows)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}

	return formatQueryResult(result), nil
}

func (t *ExecuteReadTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskMed),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(8),
		framework.WithPII(true),
		framework.WithApprovalReq(true),
	)
}

// ExecuteWriteTool executes DML queries (INSERT, UPDATE, DELETE)
type ExecuteWriteTool struct {
	db     *Database
	server *Server
}

func (t *ExecuteWriteTool) Name() string {
	return "oracle_execute_write"
}

func (t *ExecuteWriteTool) Description() string {
	return "Execute a write query (INSERT, UPDATE, DELETE). Disabled without --write-enabled flag."
}

func (t *ExecuteWriteTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"sql": map[string]interface{}{
				"type":        "string",
				"description": "DML SQL query to execute (INSERT, UPDATE, DELETE)",
			},
			"commit": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to commit the transaction (default: false for safety)",
				"default":     false,
			},
		},
		Required: []string{"sql"},
	}
}

func (t *ExecuteWriteTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	sql, ok := args["sql"].(string)
	if !ok || sql == "" {
		return "", fmt.Errorf("sql is required")
	}

	commit := false
	if c, ok := args["commit"].(bool); ok {
		commit = c
	}

	// Check if it's a write operation
	if !isWriteQuery(sql) {
		return "", fmt.Errorf("only INSERT, UPDATE, DELETE queries are allowed with oracle_execute_write")
	}

	// Check read-only mode
	if t.server.readOnly {
		return "", fmt.Errorf("server is in read-only mode. Set ORACLE_READ_ONLY=false to enable write operations.")
	}

	result, err := t.db.ExecuteWrite(ctx, sql, commit)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}

	return formatWriteResult(result, commit), nil
}

func (t *ExecuteWriteTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskHigh),
		framework.WithImpact(framework.ImpactWrite),
		framework.WithResourceCost(8),
		framework.WithPII(true),
		framework.WithApprovalReq(true),
	)
}

// ExplainQueryTool explains a query execution plan
type ExplainQueryTool struct {
	db *Database
}

func (t *ExplainQueryTool) Name() string {
	return "oracle_explain_query"
}

func (t *ExplainQueryTool) Description() string {
	return "Get the execution plan for a SELECT query"
}

func (t *ExplainQueryTool) Schema() mcp.ToolInputSchema {
	return mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"sql": map[string]interface{}{
				"type":        "string",
				"description": "SELECT SQL query to explain",
			},
		},
		Required: []string{"sql"},
	}
}

func (t *ExplainQueryTool) Handle(ctx context.Context, args map[string]interface{}) (string, error) {
	sql, ok := args["sql"].(string)
	if !ok || sql == "" {
		return "", fmt.Errorf("sql is required")
	}

	if !isSelectQuery(sql) {
		return "", fmt.Errorf("only SELECT queries can be explained")
	}

	plan, err := t.db.ExplainQuery(ctx, sql)
	if err != nil {
		return "", fmt.Errorf("failed to explain query: %w", err)
	}

	return formatExplainPlan(plan), nil
}

func (t *ExplainQueryTool) GetEnforcerProfile() framework.EnforcerProfile {
	return framework.NewEnforcerProfile(
		framework.WithRisk(framework.RiskLow),
		framework.WithImpact(framework.ImpactRead),
		framework.WithResourceCost(4),
		framework.WithPII(false),
	)
}

// Helper functions

func isSelectQuery(sql string) bool {
	sql = strings.TrimSpace(strings.ToUpper(sql))
	return strings.HasPrefix(sql, "SELECT") || strings.HasPrefix(sql, "WITH")
}

func isWriteQuery(sql string) bool {
	sql = strings.TrimSpace(strings.ToUpper(sql))
	writePrefixes := []string{"INSERT", "UPDATE", "DELETE", "MERGE", "DROP", "CREATE", "ALTER", "TRUNCATE", "GRANT", "REVOKE"}
	for _, prefix := range writePrefixes {
		if strings.HasPrefix(sql, prefix) {
			return true
		}
	}
	return false
}

func formatQueryResult(result *QueryResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Rows: %d\n\n", len(result.Rows)))

	if len(result.Columns) > 0 {
		sb.WriteString(strings.Join(result.Columns, " | "))
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("-", 60))
		sb.WriteString("\n")
	}

	for _, row := range result.Rows {
		values := make([]string, len(result.Columns))
		for i, col := range result.Columns {
			if val, ok := row[col]; ok && val != nil {
				values[i] = fmt.Sprintf("%v", val)
			} else {
				values[i] = "NULL"
			}
		}
		sb.WriteString(strings.Join(values, " | "))
		sb.WriteString("\n")
	}

	return sb.String()
}

func formatWriteResult(result *WriteResult, committed bool) string {
	status := "executed"
	if committed {
		status = "committed"
	} else {
		status = "executed (not committed - use commit=true to persist)"
	}
	return fmt.Sprintf("Query %s successfully. %d row(s) affected.", status, result.RowsAffected)
}

func formatExplainPlan(plan *ExplainPlan) string {
	var sb strings.Builder
	sb.WriteString("Execution Plan:\n\n")

	for _, step := range plan.Steps {
		sb.WriteString(fmt.Sprintf("  %s\n", step))
	}

	if len(plan.Suggestions) > 0 {
		sb.WriteString("\nOptimization Suggestions:\n")
		for _, suggestion := range plan.Suggestions {
			sb.WriteString(fmt.Sprintf("  - %s\n", suggestion))
		}
	}

	return sb.String()
}
