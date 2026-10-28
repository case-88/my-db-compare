package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// MetaQuerier queries database metadata from information_schema
type MetaQuerier struct {
	conn   *Connection
	logger *zap.Logger
}

// NewMetaQuerier creates a new MetaQuerier
func NewMetaQuerier(conn *Connection, logger *zap.Logger) *MetaQuerier {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MetaQuerier{
		conn:   conn,
		logger: logger,
	}
}

// GetTables returns all tables in the specified schema
// If schema is empty, returns all tables from all user schemas (excluding system schemas)
func (m *MetaQuerier) GetTables(ctx context.Context, schema string) ([]types.TableMeta, error) {
	var query string
	var args []interface{}

	if schema == "" {
		// Query all tables from all user schemas
		m.logger.Debug("querying all tables from all user schemas")
		query = `
			SELECT
				TABLE_SCHEMA,
				TABLE_NAME,
				COALESCE(TABLE_ROWS, 0) as TABLE_ROWS
			FROM information_schema.TABLES
			WHERE TABLE_TYPE = 'BASE TABLE'
			  AND TABLE_SCHEMA NOT IN ('mysql', 'information_schema', 'performance_schema', 'sys')
			ORDER BY TABLE_SCHEMA, TABLE_NAME
		`
	} else {
		m.logger.Debug("querying tables from schema", zap.String("schema", schema))
		query = `
			SELECT
				TABLE_SCHEMA,
				TABLE_NAME,
				COALESCE(TABLE_ROWS, 0) as TABLE_ROWS
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ?
			  AND TABLE_TYPE = 'BASE TABLE'
			ORDER BY TABLE_NAME
		`
		args = append(args, schema)
	}

	m.logger.Debug("executing query")
	rows, err := m.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()
	m.logger.Debug("query executed, scanning rows")

	var tables []types.TableMeta
	for rows.Next() {
		var t types.TableMeta
		if err := rows.Scan(&t.Schema, &t.Name, &t.EstimatedRows); err != nil {
			return nil, fmt.Errorf("failed to scan table row: %w", err)
		}
		tables = append(tables, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tables: %w", err)
	}

	m.logger.Debug("fetched tables",
		zap.String("schema", schema),
		zap.Int("count", len(tables)),
	)

	return tables, nil
}

// GetTableMeta returns complete metadata for a single table
func (m *MetaQuerier) GetTableMeta(ctx context.Context, schema, table string) (*types.TableMeta, error) {
	meta := &types.TableMeta{
		Schema: schema,
		Name:   table,
	}

	// Get estimated row count
	rowCount, err := m.getEstimatedRowCount(ctx, schema, table)
	if err != nil {
		m.logger.Warn("failed to get row count", zap.Error(err))
	}
	meta.EstimatedRows = rowCount

	// Get primary key columns
	pkColumns, err := m.getPrimaryKeyColumns(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to get primary key: %w", err)
	}
	meta.PKColumns = pkColumns

	// If no PK, try to get unique key
	if len(pkColumns) == 0 {
		ukColumns, err := m.getUniqueKeyColumns(ctx, schema, table)
		if err != nil {
			return nil, fmt.Errorf("failed to get unique key: %w", err)
		}
		meta.UKColumns = ukColumns
	}

	// Get all columns
	columns, err := m.getColumns(ctx, schema, table)
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}
	meta.Columns = columns

	m.logger.Debug("fetched table metadata",
		zap.String("table", meta.FullName()),
		zap.Int64("estimated_rows", meta.EstimatedRows),
		zap.Strings("pk_columns", meta.PKColumns),
		zap.Strings("uk_columns", meta.UKColumns),
		zap.Int("column_count", len(meta.Columns)),
	)

	return meta, nil
}

// getEstimatedRowCount returns the estimated row count from information_schema
func (m *MetaQuerier) getEstimatedRowCount(ctx context.Context, schema, table string) (int64, error) {
	query := `
		SELECT COALESCE(TABLE_ROWS, 0)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	`

	var count int64
	err := m.conn.QueryRowContext(ctx, query, schema, table).Scan(&count)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}

	return count, nil
}

// getPrimaryKeyColumns returns primary key columns in order
func (m *MetaQuerier) getPrimaryKeyColumns(ctx context.Context, schema, table string) ([]string, error) {
	query := `
		SELECT COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_NAME = ?
		  AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY ORDINAL_POSITION
	`

	rows, err := m.conn.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}

	return columns, rows.Err()
}

// getUniqueKeyColumns returns the first unique key columns (fallback when no PK)
func (m *MetaQuerier) getUniqueKeyColumns(ctx context.Context, schema, table string) ([]string, error) {
	// First, find the first unique index name
	indexQuery := `
		SELECT INDEX_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_NAME = ?
		  AND NON_UNIQUE = 0
		  AND INDEX_NAME != 'PRIMARY'
		GROUP BY INDEX_NAME
		ORDER BY INDEX_NAME
		LIMIT 1
	`

	var indexName string
	err := m.conn.QueryRowContext(ctx, indexQuery, schema, table).Scan(&indexName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No unique key found
		}
		return nil, err
	}

	// Then get the columns for that index
	columnsQuery := `
		SELECT COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_NAME = ?
		  AND INDEX_NAME = ?
		ORDER BY SEQ_IN_INDEX
	`

	rows, err := m.conn.QueryContext(ctx, columnsQuery, schema, table, indexName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}

	return columns, rows.Err()
}

// getColumns returns all columns for a table
func (m *MetaQuerier) getColumns(ctx context.Context, schema, table string) ([]types.ColumnMeta, error) {
	query := `
		SELECT
			COLUMN_NAME,
			DATA_TYPE,
			COLUMN_TYPE,
			IS_NULLABLE,
			COLUMN_KEY,
			ORDINAL_POSITION
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION
	`

	rows, err := m.conn.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []types.ColumnMeta
	for rows.Next() {
		var c types.ColumnMeta
		var nullable, columnKey string

		if err := rows.Scan(&c.Name, &c.DataType, &c.ColumnType, &nullable, &columnKey, &c.OrdinalPos); err != nil {
			return nil, err
		}

		c.Nullable = (nullable == "YES")
		c.IsPK = (columnKey == "PRI")
		c.IsUK = (columnKey == "UNI")

		columns = append(columns, c)
	}

	return columns, rows.Err()
}

// GetMinMax returns the min and max values of a key column
func (m *MetaQuerier) GetMinMax(ctx context.Context, schema, table string, keyColumns []string) (interface{}, interface{}, error) {
	if len(keyColumns) == 0 {
		return nil, nil, fmt.Errorf("no key columns specified")
	}

	// For single column key
	if len(keyColumns) == 1 {
		col := keyColumns[0]
		query := fmt.Sprintf("SELECT MIN(`%s`), MAX(`%s`) FROM `%s`.`%s`", col, col, schema, table)

		var minVal, maxVal interface{}
		err := m.conn.QueryRowContext(ctx, query).Scan(&minVal, &maxVal)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get min/max: %w", err)
		}

		return minVal, maxVal, nil
	}

	// For composite key, use first column
	col := keyColumns[0]
	query := fmt.Sprintf("SELECT MIN(`%s`), MAX(`%s`) FROM `%s`.`%s`", col, col, schema, table)

	var minVal, maxVal interface{}
	err := m.conn.QueryRowContext(ctx, query).Scan(&minVal, &maxVal)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get min/max: %w", err)
	}

	return minVal, maxVal, nil
}

// FilterTables filters tables based on include/exclude patterns
// Patterns MUST use database.table format with wildcards:
//   - "app*.*" - all tables in databases starting with "app"
//   - "ad.*" - all tables in database "ad"
//   - "*.*" - all tables in all databases
//   - "mydb.users*" - tables starting with "users" in "mydb"
//   - "*.orders" - all "orders" tables in any database
func FilterTables(tables []types.TableMeta, include, exclude []string) []types.TableMeta {
	if len(include) == 0 && len(exclude) == 0 {
		return tables
	}

	var result []types.TableMeta
	for _, t := range tables {
		fullName := t.Schema + "." + t.Name

		// Check exclude first
		if matchesAnyQualified(fullName, exclude) {
			continue
		}

		// If include is specified, table must match
		if len(include) > 0 && !matchesAnyQualified(fullName, include) {
			continue
		}

		result = append(result, t)
	}

	return result
}

// matchesAnyQualified checks if fullName (db.table) matches any pattern
func matchesAnyQualified(fullName string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchQualifiedPattern(fullName, pattern) {
			return true
		}
	}
	return false
}

// matchQualifiedPattern matches database.table patterns
// Pattern MUST contain a dot (database.table format)
// Returns false for invalid patterns without dot
func matchQualifiedPattern(fullName, pattern string) bool {
	// Pattern must contain a dot
	dotIdx := strings.Index(pattern, ".")
	if dotIdx == -1 {
		return false // Invalid pattern: must be database.table format
	}

	// Split pattern into database and table parts
	dbPattern := pattern[:dotIdx]
	tablePattern := pattern[dotIdx+1:]

	// Split fullName into database and table parts
	parts := strings.SplitN(fullName, ".", 2)
	if len(parts) != 2 {
		return false
	}
	dbName := parts[0]
	tblName := parts[1]

	// Match both database and table patterns
	return matchPattern(dbName, dbPattern) && matchPattern(tblName, tablePattern)
}

// matchPattern checks if name matches pattern (supports * wildcard at start/end)
func matchPattern(name, pattern string) bool {
	if pattern == "*" {
		return true
	}

	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		// *contains*
		return strings.Contains(name, pattern[1:len(pattern)-1])
	}

	if strings.HasPrefix(pattern, "*") {
		// *suffix
		return strings.HasSuffix(name, pattern[1:])
	}

	if strings.HasSuffix(pattern, "*") {
		// prefix*
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}

	// exact match
	return name == pattern
}

// GetTableMetaBatch fetches metadata for multiple tables in batch (3 queries instead of N*4)
func (m *MetaQuerier) GetTableMetaBatch(ctx context.Context, tables []types.TableMeta) ([]types.TableMeta, error) {
	if len(tables) == 0 {
		return nil, nil
	}

	// Build schema set for filtering
	schemaSet := make(map[string]bool)
	tableSet := make(map[string]bool) // key: "schema.table"
	for _, t := range tables {
		schemaSet[t.Schema] = true
		tableSet[t.Schema+"."+t.Name] = true
	}

	schemas := make([]string, 0, len(schemaSet))
	for s := range schemaSet {
		schemas = append(schemas, s)
	}

	m.logger.Debug("batch fetching metadata",
		zap.Int("tables", len(tables)),
		zap.Int("schemas", len(schemas)),
	)

	// 1. Fetch all PK columns for these schemas
	pkMap, err := m.batchGetPKColumns(ctx, schemas, tableSet)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get PK columns: %w", err)
	}

	// 2. Fetch all UK columns for tables without PK
	ukMap, err := m.batchGetUKColumns(ctx, schemas, tableSet, pkMap)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get UK columns: %w", err)
	}

	// 3. Fetch all columns for these schemas
	colMap, err := m.batchGetColumns(ctx, schemas, tableSet)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get columns: %w", err)
	}

	// 4. Assemble results
	result := make([]types.TableMeta, 0, len(tables))
	for _, t := range tables {
		key := t.Schema + "." + t.Name
		meta := types.TableMeta{
			Schema:        t.Schema,
			Name:          t.Name,
			EstimatedRows: t.EstimatedRows,
			PKColumns:     pkMap[key],
			UKColumns:     ukMap[key],
			Columns:       colMap[key],
		}
		result = append(result, meta)
	}

	return result, nil
}

// batchGetPKColumns fetches PK columns for all tables in given schemas
func (m *MetaQuerier) batchGetPKColumns(ctx context.Context, schemas []string, tableSet map[string]bool) (map[string][]string, error) {
	if len(schemas) == 0 {
		return make(map[string][]string), nil
	}

	placeholders := make([]string, len(schemas))
	args := make([]interface{}, len(schemas))
	for i, s := range schemas {
		placeholders[i] = "?"
		args[i] = s
	}

	query := fmt.Sprintf(`
		SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA IN (%s)
		  AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
	`, strings.Join(placeholders, ","))

	rows, err := m.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var schema, table, column string
		if err := rows.Scan(&schema, &table, &column); err != nil {
			return nil, err
		}
		key := schema + "." + table
		if tableSet[key] {
			result[key] = append(result[key], column)
		}
	}

	return result, rows.Err()
}

// batchGetUKColumns fetches UK columns for tables without PK
func (m *MetaQuerier) batchGetUKColumns(ctx context.Context, schemas []string, tableSet map[string]bool, pkMap map[string][]string) (map[string][]string, error) {
	if len(schemas) == 0 {
		return make(map[string][]string), nil
	}

	placeholders := make([]string, len(schemas))
	args := make([]interface{}, len(schemas))
	for i, s := range schemas {
		placeholders[i] = "?"
		args[i] = s
	}

	// Get first unique index for each table
	query := fmt.Sprintf(`
		SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA IN (%s)
		  AND NON_UNIQUE = 0
		  AND INDEX_NAME != 'PRIMARY'
		ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX
	`, strings.Join(placeholders, ","))

	rows, err := m.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Map of table -> first index name -> columns
	indexMap := make(map[string]map[string][]string)
	for rows.Next() {
		var schema, table, indexName, column string
		if err := rows.Scan(&schema, &table, &indexName, &column); err != nil {
			return nil, err
		}
		key := schema + "." + table
		if !tableSet[key] {
			continue
		}
		// Skip tables that have PK
		if len(pkMap[key]) > 0 {
			continue
		}
		if indexMap[key] == nil {
			indexMap[key] = make(map[string][]string)
		}
		indexMap[key][indexName] = append(indexMap[key][indexName], column)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Pick first index for each table
	result := make(map[string][]string)
	for key, indexes := range indexMap {
		for _, cols := range indexes {
			result[key] = cols
			break // Take first index only
		}
	}

	return result, nil
}

// batchGetColumns fetches all columns for tables in given schemas
func (m *MetaQuerier) batchGetColumns(ctx context.Context, schemas []string, tableSet map[string]bool) (map[string][]types.ColumnMeta, error) {
	if len(schemas) == 0 {
		return make(map[string][]types.ColumnMeta), nil
	}

	placeholders := make([]string, len(schemas))
	args := make([]interface{}, len(schemas))
	for i, s := range schemas {
		placeholders[i] = "?"
		args[i] = s
	}

	query := fmt.Sprintf(`
		SELECT
			TABLE_SCHEMA,
			TABLE_NAME,
			COLUMN_NAME,
			DATA_TYPE,
			COLUMN_TYPE,
			IS_NULLABLE,
			COLUMN_KEY,
			ORDINAL_POSITION
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA IN (%s)
		ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
	`, strings.Join(placeholders, ","))

	rows, err := m.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]types.ColumnMeta)
	for rows.Next() {
		var schema, table string
		var c types.ColumnMeta
		var nullable, columnKey string

		if err := rows.Scan(&schema, &table, &c.Name, &c.DataType, &c.ColumnType, &nullable, &columnKey, &c.OrdinalPos); err != nil {
			return nil, err
		}

		key := schema + "." + table
		if !tableSet[key] {
			continue
		}

		c.Nullable = (nullable == "YES")
		c.IsPK = (columnKey == "PRI")
		c.IsUK = (columnKey == "UNI")

		result[key] = append(result[key], c)
	}

	return result, rows.Err()
}
