package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

// Database handles Oracle database connections and queries
type Database struct {
	connString string
	db         *sql.DB
	schema     string
	readOnly   bool

	// Schema cache
	cache      *SchemaCache
	cacheMutex sync.RWMutex
	cachePath  string
}

// NewDatabase creates a new Oracle database connection
func NewDatabase(connString string, readOnly bool) (*Database, error) {
	// Open connection
	db, err := sql.Open("oracle", connString)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Get current schema
	var schema string
	row := db.QueryRowContext(ctx, "SELECT USER FROM DUAL")
	if err := row.Scan(&schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to get schema: %w", err)
	}

	// Determine cache path
	cacheDir := os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = ".cache"
	}
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s.json", strings.ToLower(schema)))

	d := &Database{
		connString: connString,
		db:         db,
		schema:     strings.ToUpper(schema),
		readOnly:   readOnly,
		cachePath:  cachePath,
	}

	return d, nil
}

// Close closes the database connection
func (d *Database) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// InitializeSchemaCache loads or builds the schema cache
func (d *Database) InitializeSchemaCache() error {
	cache, err := d.loadOrBuildCache()
	if err != nil {
		return err
	}

	d.cacheMutex.Lock()
	d.cache = cache
	d.cacheMutex.Unlock()

	return nil
}

// RebuildSchemaCache forces a rebuild of the schema cache
func (d *Database) RebuildSchemaCache() error {
	cache, err := d.buildCache()
	if err != nil {
		return err
	}

	d.cacheMutex.Lock()
	d.cache = cache
	d.cacheMutex.Unlock()

	return d.saveCache()
}

// GetAllTableNames returns all table names
func (d *Database) GetAllTableNames(ctx context.Context) ([]string, error) {
	d.cacheMutex.RLock()
	if d.cache != nil {
		tables := make([]string, 0, len(d.cache.AllTableNames))
		for table := range d.cache.AllTableNames {
			tables = append(tables, table)
		}
		d.cacheMutex.RUnlock()
		return tables, nil
	}
	d.cacheMutex.RUnlock()

	// Fallback to database query
	query := `
		SELECT table_name 
		FROM all_tables 
		WHERE owner = :1
		ORDER BY table_name
	`

	rows, err := d.db.QueryContext(ctx, query, d.schema)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}

	return tables, rows.Err()
}

// GetTableInfo returns schema information for a table
func (t *Database) GetTableInfo(ctx context.Context, tableName string) (*TableInfo, error) {
	tableName = strings.ToUpper(tableName)

	// Check cache first
	t.cacheMutex.RLock()
	if t.cache != nil {
		if tableInfo, ok := t.cache.Tables[tableName]; ok {
			if tableInfo.FullyLoaded {
				t.cacheMutex.RUnlock()
				return tableInfo, nil
			}
		}
	}
	t.cacheMutex.RUnlock()

	// Load from database
	tableInfo, err := t.loadTableDetails(ctx, tableName)
	if err != nil {
		return nil, err
	}

	// Update cache
	if tableInfo != nil {
		t.cacheMutex.Lock()
		if t.cache != nil {
			t.cache.Tables[tableName] = tableInfo
			t.cache.AllTableNames[tableName] = struct{}{}
			t.saveCache()
		}
		t.cacheMutex.Unlock()
	}

	return tableInfo, nil
}

// SearchTables searches for tables by name pattern
func (d *Database) SearchTables(ctx context.Context, searchTerm string, limit int) ([]string, error) {
	searchTerm = strings.ToUpper(searchTerm)

	// First check cache
	d.cacheMutex.RLock()
	if d.cache != nil {
		var matches []string
		for tableName := range d.cache.AllTableNames {
			if strings.Contains(tableName, searchTerm) {
				matches = append(matches, tableName)
				if len(matches) >= limit {
					break
				}
			}
		}
		d.cacheMutex.RUnlock()

		if len(matches) >= limit {
			return matches, nil
		}
	} else {
		d.cacheMutex.RUnlock()
	}

	// Query database
	query := `
		SELECT table_name 
		FROM all_tables 
		WHERE owner = :1
		AND UPPER(table_name) LIKE '%' || :2 || '%'
		ORDER BY table_name
		FETCH FIRST :3 ROWS ONLY
	`

	rows, err := d.db.QueryContext(ctx, query, d.schema, searchTerm, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		tables = append(tables, tableName)
	}

	return tables, rows.Err()
}

// SearchColumns searches for columns by name pattern
func (d *Database) SearchColumns(ctx context.Context, searchTerm string, limit int) (map[string][]ColumnInfo, error) {
	searchTerm = strings.ToUpper(searchTerm)

	query := `
		SELECT table_name, column_name, data_type, nullable
		FROM all_tab_columns 
		WHERE owner = :1
		AND UPPER(column_name) LIKE '%' || :2 || '%'
		ORDER BY table_name, column_id
		FETCH FIRST :3 ROWS ONLY
	`

	rows, err := d.db.QueryContext(ctx, query, d.schema, searchTerm, limit*10)
	if err != nil {
		return nil, fmt.Errorf("failed to search columns: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]ColumnInfo)
	count := 0

	for rows.Next() {
		var tableName, colName, dataType, nullable string
		if err := rows.Scan(&tableName, &colName, &dataType, &nullable); err != nil {
			return nil, err
		}

		if _, ok := result[tableName]; !ok {
			if count >= limit {
				break
			}
			count++
		}

		result[tableName] = append(result[tableName], ColumnInfo{
			Name:     colName,
			DataType: dataType,
			Nullable: nullable == "Y",
		})
	}

	return result, rows.Err()
}

// GetConstraints returns constraints for a table
func (d *Database) GetConstraints(ctx context.Context, tableName string) ([]ConstraintInfo, error) {
	tableName = strings.ToUpper(tableName)

	query := `
		SELECT ac.constraint_name, ac.constraint_type, ac.search_condition
		FROM all_constraints ac
		WHERE ac.owner = :1
		AND ac.table_name = :2
	`

	rows, err := d.db.QueryContext(ctx, query, d.schema, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to get constraints: %w", err)
	}
	defer rows.Close()

	var constraints []ConstraintInfo

	for rows.Next() {
		var name, constraintType, condition sql.NullString
		if err := rows.Scan(&name, &constraintType, &condition); err != nil {
			return nil, err
		}

		info := ConstraintInfo{
			Name: name.String,
			Type: mapConstraintType(constraintType.String),
		}

		if condition.Valid {
			info.Condition = condition.String
		}

		// Get columns
		colQuery := `
			SELECT column_name
			FROM all_cons_columns
			WHERE owner = :1
			AND constraint_name = :2
			ORDER BY position
		`
		colRows, err := d.db.QueryContext(ctx, colQuery, d.schema, name.String)
		if err != nil {
			return nil, err
		}

		for colRows.Next() {
			var colName string
			if err := colRows.Scan(&colName); err != nil {
				colRows.Close()
				return nil, err
			}
			info.Columns = append(info.Columns, colName)
		}
		colRows.Close()

		// If FK, get referenced table
		if constraintType.String == "R" {
			refQuery := `
				SELECT ac.table_name, acc.column_name
				FROM all_constraints ac
				JOIN all_cons_columns acc ON ac.constraint_name = acc.constraint_name
					AND ac.owner = acc.owner
				WHERE ac.constraint_name = (
					SELECT r_constraint_name
					FROM all_constraints
					WHERE owner = :1
					AND constraint_name = :2
				)
				AND acc.owner = ac.owner
				ORDER BY acc.position
			`
			refRows, err := d.db.QueryContext(ctx, refQuery, d.schema, name.String)
			if err != nil {
				return nil, err
			}

			if refRows.Next() {
				var refTable, refCol string
				if err := refRows.Scan(&refTable, &refCol); err != nil {
					refRows.Close()
					return nil, err
				}
				info.References = &ReferenceInfo{
					Table:   refTable,
					Columns: []string{refCol},
				}
			}
			refRows.Close()
		}

		constraints = append(constraints, info)
	}

	return constraints, rows.Err()
}

// GetIndexes returns indexes for a table
func (d *Database) GetIndexes(ctx context.Context, tableName string) ([]IndexInfo, error) {
	tableName = strings.ToUpper(tableName)

	query := `
		SELECT index_name, uniqueness, status
		FROM all_indexes
		WHERE owner = :1
		AND table_name = :2
	`

	rows, err := d.db.QueryContext(ctx, query, d.schema, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to get indexes: %w", err)
	}
	defer rows.Close()

	var indexes []IndexInfo

	for rows.Next() {
		var name, uniqueness, status string
		if err := rows.Scan(&name, &uniqueness, &status); err != nil {
			return nil, err
		}

		info := IndexInfo{
			Name:   name,
			Unique: uniqueness == "UNIQUE",
			Status: status,
		}

		// Get columns
		colQuery := `
			SELECT column_name
			FROM all_ind_columns
			WHERE index_owner = :1
			AND index_name = :2
			ORDER BY column_position
		`
		colRows, err := d.db.QueryContext(ctx, colQuery, d.schema, name)
		if err != nil {
			return nil, err
		}

		for colRows.Next() {
			var colName string
			if err := colRows.Scan(&colName); err != nil {
				colRows.Close()
				return nil, err
			}
			info.Columns = append(info.Columns, colName)
		}
		colRows.Close()

		indexes = append(indexes, info)
	}

	return indexes, rows.Err()
}

// GetRelatedTables returns tables related by foreign keys
func (d *Database) GetRelatedTables(ctx context.Context, tableName string) (*RelatedTables, error) {
	tableName = strings.ToUpper(tableName)

	result := &RelatedTables{
		ReferencedTables:  []string{},
		ReferencingTables: []string{},
	}

	// Tables this table references (outgoing FKs)
	outQuery := `
		SELECT DISTINCT parent_cols.table_name
		FROM all_constraints fk
		JOIN all_constraints pk ON pk.constraint_name = fk.r_constraint_name
			AND pk.owner = fk.r_owner
		JOIN all_cons_columns parent_cols ON parent_cols.constraint_name = pk.constraint_name
			AND parent_cols.owner = pk.owner
		WHERE fk.constraint_type = 'R'
		AND fk.table_name = :1
		AND fk.owner = :2
	`

	rows, err := d.db.QueryContext(ctx, outQuery, tableName, d.schema)
	if err != nil {
		return nil, fmt.Errorf("failed to get related tables: %w", err)
	}

	for rows.Next() {
		var refTable string
		if err := rows.Scan(&refTable); err != nil {
			rows.Close()
			return nil, err
		}
		result.ReferencedTables = append(result.ReferencedTables, refTable)
	}
	rows.Close()

	// Tables that reference this table (incoming FKs)
	inQuery := `
		SELECT DISTINCT fk.table_name
		FROM all_constraints pk
		JOIN all_constraints fk ON fk.r_constraint_name = pk.constraint_name
			AND fk.r_owner = pk.owner
		WHERE pk.constraint_type IN ('P', 'U')
		AND pk.table_name = :1
		AND pk.owner = :2
		AND fk.constraint_type = 'R'
	`

	rows, err = d.db.QueryContext(ctx, inQuery, tableName, d.schema)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var refTable string
		if err := rows.Scan(&refTable); err != nil {
			rows.Close()
			return nil, err
		}
		result.ReferencingTables = append(result.ReferencingTables, refTable)
	}

	return result, rows.Close()
}

// ExecuteQuery executes a SELECT query
func (d *Database) ExecuteQuery(ctx context.Context, sql string, maxRows int) (*QueryResult, error) {
	// Add row limiting if not present
	if !strings.Contains(strings.ToUpper(sql), "FETCH FIRST") && !strings.Contains(strings.ToUpper(sql), "ROWNUM") {
		sql = fmt.Sprintf("SELECT * FROM (%s) WHERE ROWNUM <= %d", sql, maxRows)
	}

	rows, err := d.db.QueryContext(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{
		Columns: columns,
		Rows:    []map[string]interface{}{},
	}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			row[col] = values[i]
		}
		result.Rows = append(result.Rows, row)

		if len(result.Rows) >= maxRows {
			break
		}
	}

	return result, rows.Err()
}

// ExecuteWrite executes a DML query
func (d *Database) ExecuteWrite(ctx context.Context, sql string, commit bool) (*WriteResult, error) {
	if d.readOnly {
		return nil, fmt.Errorf("database is in read-only mode")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()

	if commit {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit: %w", err)
		}
	}

	return &WriteResult{
		RowsAffected: rowsAffected,
		Committed:    commit,
	}, nil
}

// ExplainQuery gets the execution plan for a query
func (d *Database) ExplainQuery(ctx context.Context, sql string) (*ExplainPlan, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Generate explain plan
	_, err = tx.ExecContext(ctx, fmt.Sprintf("EXPLAIN PLAN FOR %s", sql))
	if err != nil {
		return nil, fmt.Errorf("failed to generate explain plan: %w", err)
	}

	// Retrieve plan
	rows, err := tx.QueryContext(ctx, `
		SELECT 
			LPAD(' ', 2*LEVEL-2) || operation || ' ' || 
			options || ' ' || object_name || 
			CASE 
				WHEN cost IS NOT NULL THEN ' (Cost: ' || cost || ')'
				ELSE ''
			END || 
			CASE 
				WHEN cardinality IS NOT NULL THEN ' (Rows: ' || cardinality || ')'
				ELSE ''
			END as execution_plan_step
		FROM plan_table
		START WITH id = 0
		CONNECT BY PRIOR id = parent_id
		ORDER SIBLINGS BY position
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve explain plan: %w", err)
	}
	defer rows.Close()

	plan := &ExplainPlan{
		Steps:       []string{},
		Suggestions: []string{},
	}

	for rows.Next() {
		var step string
		if err := rows.Scan(&step); err != nil {
			return nil, err
		}
		plan.Steps = append(plan.Steps, step)
	}

	// Clean up plan table
	tx.ExecContext(ctx, "DELETE FROM plan_table")
	tx.Commit()

	// Add basic suggestions
	plan.Suggestions = analyzeQueryForOptimization(sql)

	return plan, nil
}

// Helper functions

func (d *Database) loadOrBuildCache() (*SchemaCache, error) {
	// Try to load from disk
	if _, err := os.Stat(d.cachePath); err == nil {
		cache, err := d.loadCacheFromDisk()
		if err == nil {
			return cache, nil
		}
	}

	// Build new cache
	return d.buildCache()
}

func (d *Database) loadCacheFromDisk() (*SchemaCache, error) {
	// Implementation would load from JSON file
	// For now, return nil to trigger rebuild
	return nil, fmt.Errorf("cache loading not implemented")
}

func (d *Database) buildCache() (*SchemaCache, error) {
	ctx := context.Background()
	tables, err := d.GetAllTableNames(ctx)
	if err != nil {
		return nil, err
	}

	cache := &SchemaCache{
		Tables:        make(map[string]*TableInfo),
		AllTableNames: make(map[string]struct{}),
		LastUpdated:   time.Now(),
	}

	for _, table := range tables {
		cache.AllTableNames[table] = struct{}{}
		cache.Tables[table] = &TableInfo{
			TableName:     table,
			Columns:       []ColumnInfo{},
			Relationships: make(map[string][]RelationshipInfo),
			FullyLoaded:   false,
		}
	}

	d.saveCache()
	return cache, nil
}

func (d *Database) saveCache() error {
	// Implementation would save to JSON file
	return nil
}

func (d *Database) loadTableDetails(ctx context.Context, tableName string) (*TableInfo, error) {
	// Check if table exists
	var count int
	err := d.db.QueryRowContext(ctx, `
		SELECT COUNT(*) 
		FROM all_tables 
		WHERE owner = :1 AND table_name = :2
	`, d.schema, tableName).Scan(&count)

	if err != nil {
		return nil, err
	}

	if count == 0 {
		return nil, nil
	}

	// Get columns
	colRows, err := d.db.QueryContext(ctx, `
		SELECT column_name, data_type, nullable
		FROM all_tab_columns
		WHERE owner = :1 AND table_name = :2
		ORDER BY column_id
	`, d.schema, tableName)

	if err != nil {
		return nil, err
	}
	defer colRows.Close()

	info := &TableInfo{
		TableName:     tableName,
		Columns:       []ColumnInfo{},
		Relationships: make(map[string][]RelationshipInfo),
		FullyLoaded:   true,
	}

	for colRows.Next() {
		var col ColumnInfo
		var nullable string
		if err := colRows.Scan(&col.Name, &col.DataType, &nullable); err != nil {
			return nil, err
		}
		col.Nullable = nullable == "Y"
		info.Columns = append(info.Columns, col)
	}

	// Get relationships
	relRows, err := d.db.QueryContext(ctx, `
		SELECT 'OUTGOING' AS direction, acc.column_name, rcc.table_name, rcc.column_name
		FROM all_constraints ac
		JOIN all_cons_columns acc ON acc.constraint_name = ac.constraint_name AND acc.owner = ac.owner
		JOIN all_cons_columns rcc ON rcc.constraint_name = ac.r_constraint_name AND rcc.owner = ac.r_owner
		WHERE ac.constraint_type = 'R'
		AND ac.owner = :1
		AND ac.table_name = :2

		UNION ALL

		SELECT 'INCOMING' AS direction, rcc.column_name, ac.table_name, acc.column_name
		FROM all_constraints ac
		JOIN all_cons_columns acc ON acc.constraint_name = ac.constraint_name AND acc.owner = ac.owner
		JOIN all_cons_columns rcc ON rcc.constraint_name = ac.r_constraint_name AND rcc.owner = ac.r_owner
		WHERE ac.constraint_type = 'R'
		AND ac.r_owner = :1
		AND ac.r_constraint_name IN (
			SELECT constraint_name 
			FROM all_constraints
			WHERE owner = :1
			AND table_name = :2
			AND constraint_type IN ('P', 'U')
		)
	`, d.schema, tableName)

	if err != nil {
		return nil, err
	}
	defer relRows.Close()

	for relRows.Next() {
		var direction, localCol, refTable, refCol string
		if err := relRows.Scan(&direction, &localCol, &refTable, &refCol); err != nil {
			return nil, err
		}

		info.Relationships[refTable] = append(info.Relationships[refTable], RelationshipInfo{
			LocalColumn:   localCol,
			ForeignColumn: refCol,
			Direction:     direction,
		})
	}

	return info, nil
}

func mapConstraintType(t string) string {
	switch t {
	case "P":
		return "PRIMARY KEY"
	case "R":
		return "FOREIGN KEY"
	case "U":
		return "UNIQUE"
	case "C":
		return "CHECK"
	default:
		return t
	}
}

func analyzeQueryForOptimization(sql string) []string {
	sql = strings.ToUpper(sql)
	suggestions := []string{}

	if strings.Contains(sql, "SELECT *") {
		suggestions = append(suggestions, "Consider selecting only needed columns instead of SELECT *")
	}

	if strings.Contains(sql, " LIKE '%") {
		suggestions = append(suggestions, "Leading wildcards in LIKE predicates prevent index usage")
	}

	if strings.Contains(sql, " IN (SELECT ") && !strings.Contains(sql, " EXISTS") {
		suggestions = append(suggestions, "Consider using EXISTS instead of IN with subqueries for better performance")
	}

	if strings.Contains(sql, " OR ") {
		suggestions = append(suggestions, "OR conditions may prevent index usage. Consider UNION ALL of separated queries")
	}

	joinCount := strings.Count(sql, " JOIN ")
	if joinCount > 2 {
		suggestions = append(suggestions, fmt.Sprintf("Query joins %d tables - consider reviewing join order and conditions", joinCount+1))
	}

	return suggestions
}
