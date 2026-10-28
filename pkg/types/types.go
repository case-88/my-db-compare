package types

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MismatchType represents the type of data mismatch
type MismatchType string

const (
	MismatchDataDiff      MismatchType = "DATA_MISMATCH"
	MismatchMissingTarget MismatchType = "MISSING_IN_TARGET"
	MismatchMissingSource MismatchType = "MISSING_IN_SOURCE"
)

// DBConfig holds database connection configuration
type DBConfig struct {
	Host     string `yaml:"host" mapstructure:"host"`
	Port     int    `yaml:"port" mapstructure:"port"`
	User     string `yaml:"user" mapstructure:"user"`
	Password string `yaml:"password" mapstructure:"password"`
	Database string `yaml:"database" mapstructure:"database"`
}

// DSN returns the MySQL DSN string
func (c DBConfig) DSN() string {
	return c.User + ":" + c.Password + "@tcp(" + c.Host + ":" + itoa(c.Port) + ")/" + c.Database + "?parseTime=true&charset=utf8mb4"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

// ColumnMeta holds column metadata from information_schema
type ColumnMeta struct {
	Name       string
	DataType   string
	ColumnType string
	Nullable   bool
	IsPK       bool
	IsUK       bool
	OrdinalPos int
}

// TableMeta holds table metadata
type TableMeta struct {
	Schema        string
	Name          string
	PKColumns     []string     // Primary key columns in order
	UKColumns     []string     // Unique key columns (fallback if no PK)
	Columns       []ColumnMeta // All columns
	EstimatedRows int64        // Estimated row count from information_schema
}

// FullName returns schema.table format
func (t TableMeta) FullName() string {
	return t.Schema + "." + t.Name
}

// KeyColumns returns PK columns, or UK columns if PK is empty
func (t TableMeta) KeyColumns() []string {
	if len(t.PKColumns) > 0 {
		return t.PKColumns
	}
	return t.UKColumns
}

// HasKey returns true if table has PK or UK
func (t TableMeta) HasKey() bool {
	return len(t.PKColumns) > 0 || len(t.UKColumns) > 0
}

// Partition represents a data partition for sampling
type Partition struct {
	Index    int
	MinValue interface{}
	MaxValue interface{}
}

// Row represents a database row as a map
type Row map[string]interface{}

// SampledRow holds a sampled row with its key values
type SampledRow struct {
	KeyValues map[string]interface{}
	Data      Row
	Hash      uint64
}

// Mismatch represents a data mismatch between source and target
type Mismatch struct {
	Table           string                 `json:"table"`
	PK              map[string]interface{} `json:"pk"`
	Type            MismatchType           `json:"type"`
	DiffColumns     []string               `json:"diff_columns,omitempty"`
	SourceData      Row                    `json:"source,omitempty"`
	TargetData      Row                    `json:"target,omitempty"`
	Query           string                 `json:"query"`                      // SELECT query to lookup this row
	CorrectionQuery string                 `json:"correction_query,omitempty"` // UPDATE/INSERT/DELETE query to fix target
	Timestamp       time.Time              `json:"timestamp"`
}

// CompareResult holds the comparison result for a single table
type CompareResult struct {
	Table           string        `json:"table"`
	TotalSampled    int           `json:"total_sampled"`
	MatchCount      int           `json:"match_count"`
	MismatchCount   int           `json:"mismatch_count"`
	MissingInTarget int           `json:"missing_in_target"`
	MissingInSource int           `json:"missing_in_source"`
	Mismatches      []Mismatch    `json:"mismatches,omitempty"`
	Duration        time.Duration `json:"duration"`
	Error           string        `json:"error,omitempty"`
}

// Summary holds the overall comparison summary
type Summary struct {
	StartTime       time.Time       `json:"start_time"`
	EndTime         time.Time       `json:"end_time"`
	Duration        time.Duration   `json:"duration"`
	TablesCompared  int             `json:"tables_compared"`
	TablesSkipped   int             `json:"tables_skipped"`
	TotalSampled    int             `json:"total_sampled"`
	TotalMatched    int             `json:"total_matched"`
	TotalMismatched int             `json:"total_mismatched"`
	Results         []CompareResult `json:"results"`
}

// BuildLookupQuery generates a SELECT query to lookup a row by its primary key
// Table should be in "schema.table" format
// Example: SELECT * FROM `mydb`.`users` WHERE `id` = 123;
func BuildLookupQuery(table string, pk map[string]interface{}) string {
	return BuildLookupQueryWithColumns(table, pk, nil)
}

// BuildLookupQueryWithColumns generates a SELECT query with specific columns
// If columns is nil or empty, selects all columns (*)
// Otherwise selects only PK columns + specified columns
// Example: SELECT `id`, `status`, `updated_at` FROM `mydb`.`users` WHERE `id` = 123;
func BuildLookupQueryWithColumns(table string, pk map[string]interface{}, columns []string) string {
	if len(pk) == 0 {
		return ""
	}

	// Parse schema.table
	parts := strings.SplitN(table, ".", 2)
	var quotedTable string
	if len(parts) == 2 {
		quotedTable = fmt.Sprintf("`%s`.`%s`", parts[0], parts[1])
	} else {
		quotedTable = fmt.Sprintf("`%s`", table)
	}

	// Build column list
	var columnList string
	if len(columns) == 0 {
		columnList = "*"
	} else {
		// Include PK columns + diff columns (deduplicated)
		colSet := make(map[string]struct{})
		var cols []string

		// Add PK columns first
		pkKeys := make([]string, 0, len(pk))
		for k := range pk {
			pkKeys = append(pkKeys, k)
		}
		sort.Strings(pkKeys)
		for _, k := range pkKeys {
			if _, exists := colSet[k]; !exists {
				colSet[k] = struct{}{}
				cols = append(cols, fmt.Sprintf("`%s`", k))
			}
		}

		// Add diff columns
		for _, c := range columns {
			if _, exists := colSet[c]; !exists {
				colSet[c] = struct{}{}
				cols = append(cols, fmt.Sprintf("`%s`", c))
			}
		}
		columnList = strings.Join(cols, ", ")
	}

	// Build WHERE clause with sorted keys for deterministic output
	keys := make([]string, 0, len(pk))
	for k := range pk {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	conditions := make([]string, len(keys))
	for i, k := range keys {
		conditions[i] = fmt.Sprintf("`%s` = %s", k, formatValue(pk[k]))
	}

	return fmt.Sprintf("SELECT %s FROM %s WHERE %s;", columnList, quotedTable, strings.Join(conditions, " AND "))
}

// formatValue formats a value for SQL query
func formatValue(v interface{}) string {
	if v == nil {
		return "NULL"
	}

	switch val := v.(type) {
	case string:
		// Escape single quotes
		escaped := strings.ReplaceAll(val, "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	case []byte:
		escaped := strings.ReplaceAll(string(val), "'", "''")
		return fmt.Sprintf("'%s'", escaped)
	case time.Time:
		return fmt.Sprintf("'%s'", val.Format("2006-01-02 15:04:05"))
	case bool:
		if val {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// BuildCorrectionQuery generates a correction query based on mismatch type
// For DATA_MISMATCH: UPDATE query
// For MISSING_IN_TARGET: INSERT query
// For MISSING_IN_SOURCE: DELETE query
func BuildCorrectionQuery(mismatchType MismatchType, table string, pk map[string]interface{}, sourceData Row, diffColumns []string) string {
	switch mismatchType {
	case MismatchDataDiff:
		return BuildUpdateQuery(table, pk, sourceData, diffColumns)
	case MismatchMissingTarget:
		return BuildInsertQuery(table, sourceData)
	case MismatchMissingSource:
		return BuildDeleteQuery(table, pk)
	default:
		return ""
	}
}

// BuildUpdateQuery generates an UPDATE query to fix target data
// Example: UPDATE `mydb`.`users` SET `status` = 'active', `updated_at` = '2024-01-01 00:00:00' WHERE `id` = 123;
func BuildUpdateQuery(table string, pk map[string]interface{}, sourceData Row, diffColumns []string) string {
	if len(pk) == 0 || len(diffColumns) == 0 {
		return ""
	}

	quotedTable := quoteTable(table)

	// Build SET clause with only diff columns
	setClauses := make([]string, 0, len(diffColumns))
	for _, col := range diffColumns {
		if val, ok := sourceData[col]; ok {
			setClauses = append(setClauses, fmt.Sprintf("`%s` = %s", col, formatValue(val)))
		}
	}
	if len(setClauses) == 0 {
		return ""
	}

	// Build WHERE clause
	whereClause := buildWhereClause(pk)

	return fmt.Sprintf("UPDATE %s SET %s WHERE %s;", quotedTable, strings.Join(setClauses, ", "), whereClause)
}

// BuildInsertQuery generates an INSERT query to add missing row to target
// Example: INSERT INTO `mydb`.`users` (`id`, `name`, `status`) VALUES (123, 'John', 'active');
func BuildInsertQuery(table string, sourceData Row) string {
	if len(sourceData) == 0 {
		return ""
	}

	quotedTable := quoteTable(table)

	// Sort columns for deterministic output
	cols := make([]string, 0, len(sourceData))
	for col := range sourceData {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	// Build column list and values
	quotedCols := make([]string, len(cols))
	values := make([]string, len(cols))
	for i, col := range cols {
		quotedCols[i] = fmt.Sprintf("`%s`", col)
		values[i] = formatValue(sourceData[col])
	}

	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);", quotedTable, strings.Join(quotedCols, ", "), strings.Join(values, ", "))
}

// BuildDeleteQuery generates a DELETE query to remove extra row from target
// Example: DELETE FROM `mydb`.`users` WHERE `id` = 123;
func BuildDeleteQuery(table string, pk map[string]interface{}) string {
	if len(pk) == 0 {
		return ""
	}

	quotedTable := quoteTable(table)
	whereClause := buildWhereClause(pk)

	return fmt.Sprintf("DELETE FROM %s WHERE %s;", quotedTable, whereClause)
}

// quoteTable quotes a table name (handles schema.table format)
func quoteTable(table string) string {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) == 2 {
		return fmt.Sprintf("`%s`.`%s`", parts[0], parts[1])
	}
	return fmt.Sprintf("`%s`", table)
}

// buildWhereClause builds a WHERE clause from PK values
func buildWhereClause(pk map[string]interface{}) string {
	keys := make([]string, 0, len(pk))
	for k := range pk {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	conditions := make([]string, len(keys))
	for i, k := range keys {
		conditions[i] = fmt.Sprintf("`%s` = %s", k, formatValue(pk[k]))
	}
	return strings.Join(conditions, " AND ")
}
