package db

import (
	"context"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

func TestFilterTables(t *testing.T) {
	tables := []types.TableMeta{
		{Schema: "test", Name: "users"},
		{Schema: "test", Name: "orders"},
		{Schema: "test", Name: "order_items"},
		{Schema: "test", Name: "products"},
		{Schema: "test", Name: "tmp_backup"},
		{Schema: "test", Name: "log_events"},
	}

	tests := []struct {
		name    string
		include []string
		exclude []string
		want    []string
	}{
		{
			name:    "no filters",
			include: nil,
			exclude: nil,
			want:    []string{"users", "orders", "order_items", "products", "tmp_backup", "log_events"},
		},
		{
			name:    "include exact match",
			include: []string{"test.users", "test.orders"},
			exclude: nil,
			want:    []string{"users", "orders"},
		},
		{
			name:    "include all tables in database",
			include: []string{"test.*"},
			exclude: nil,
			want:    []string{"users", "orders", "order_items", "products", "tmp_backup", "log_events"},
		},
		{
			name:    "include prefix wildcard",
			include: []string{"test.order*"},
			exclude: nil,
			want:    []string{"orders", "order_items"},
		},
		{
			name:    "include suffix wildcard",
			include: []string{"test.*items"},
			exclude: nil,
			want:    []string{"order_items"},
		},
		{
			name:    "include contains wildcard",
			include: []string{"test.*order*"},
			exclude: nil,
			want:    []string{"orders", "order_items"},
		},
		{
			name:    "exclude prefix wildcard",
			include: []string{"*.*"},
			exclude: []string{"*.tmp_*"},
			want:    []string{"users", "orders", "order_items", "products", "log_events"},
		},
		{
			name:    "exclude suffix wildcard",
			include: []string{"*.*"},
			exclude: []string{"*.*_backup"},
			want:    []string{"users", "orders", "order_items", "products", "log_events"},
		},
		{
			name:    "include and exclude combined",
			include: []string{"test.*"},
			exclude: []string{"test.tmp_*", "test.log_*"},
			want:    []string{"users", "orders", "order_items", "products"},
		},
		{
			name:    "exclude takes precedence",
			include: []string{"test.users", "test.tmp_backup"},
			exclude: []string{"test.tmp_*"},
			want:    []string{"users"},
		},
		{
			name:    "invalid pattern without dot is ignored",
			include: []string{"users"},
			exclude: nil,
			want:    []string{}, // No match because pattern must be database.table
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterTables(tables, tt.include, tt.exclude)

			if len(got) != len(tt.want) {
				t.Errorf("FilterTables() returned %d tables, want %d", len(got), len(tt.want))
				return
			}

			for i, table := range got {
				if table.Name != tt.want[i] {
					t.Errorf("FilterTables()[%d] = %q, want %q", i, table.Name, tt.want[i])
				}
			}
		})
	}
}

func TestFilterTables_QualifiedPattern(t *testing.T) {
	// Multi-database tables
	tables := []types.TableMeta{
		{Schema: "app", Name: "users"},
		{Schema: "app", Name: "orders"},
		{Schema: "app_archive", Name: "users"},
		{Schema: "app_archive", Name: "orders"},
		{Schema: "ad", Name: "campaigns"},
		{Schema: "ad", Name: "impressions"},
		{Schema: "shop", Name: "products"},
		{Schema: "shop", Name: "orders"},
	}

	tests := []struct {
		name    string
		include []string
		exclude []string
		want    []string // format: "schema.table"
	}{
		{
			name:    "all tables with *.*",
			include: []string{"*.*"},
			exclude: nil,
			want:    []string{"app.users", "app.orders", "app_archive.users", "app_archive.orders", "ad.campaigns", "ad.impressions", "shop.products", "shop.orders"},
		},
		{
			name:    "exact database match",
			include: []string{"ad.*"},
			exclude: nil,
			want:    []string{"ad.campaigns", "ad.impressions"},
		},
		{
			name:    "database prefix wildcard",
			include: []string{"app*.*"},
			exclude: nil,
			want:    []string{"app.users", "app.orders", "app_archive.users", "app_archive.orders"},
		},
		{
			name:    "table prefix wildcard",
			include: []string{"*.order*"},
			exclude: nil,
			want:    []string{"app.orders", "app_archive.orders", "shop.orders"},
		},
		{
			name:    "exact database.table match",
			include: []string{"shop.products"},
			exclude: nil,
			want:    []string{"shop.products"},
		},
		{
			name:    "multiple patterns",
			include: []string{"ad.*", "shop.products"},
			exclude: nil,
			want:    []string{"ad.campaigns", "ad.impressions", "shop.products"},
		},
		{
			name:    "exclude database",
			include: []string{"*.*"},
			exclude: []string{"app_archive.*"},
			want:    []string{"app.users", "app.orders", "ad.campaigns", "ad.impressions", "shop.products", "shop.orders"},
		},
		{
			name:    "include and exclude combined",
			include: []string{"app*.*"},
			exclude: []string{"*_archive.*"},
			want:    []string{"app.users", "app.orders"},
		},
		{
			name:    "exclude specific table pattern",
			include: []string{"*.*"},
			exclude: []string{"*.orders"},
			want:    []string{"app.users", "app_archive.users", "ad.campaigns", "ad.impressions", "shop.products"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterTables(tables, tt.include, tt.exclude)

			if len(got) != len(tt.want) {
				var gotNames []string
				for _, table := range got {
					gotNames = append(gotNames, table.Schema+"."+table.Name)
				}
				t.Errorf("FilterTables() returned %v, want %v", gotNames, tt.want)
				return
			}

			for i, table := range got {
				fullName := table.Schema + "." + table.Name
				if fullName != tt.want[i] {
					t.Errorf("FilterTables()[%d] = %q, want %q", i, fullName, tt.want[i])
				}
			}
		})
	}
}

func TestMatchQualifiedPattern(t *testing.T) {
	tests := []struct {
		name     string
		fullName string
		pattern  string
		want     bool
	}{
		// database.table format tests
		{"all match", "test.users", "*.*", true},
		{"exact db.table match", "test.users", "test.users", true},
		{"exact db.table mismatch", "test.users", "prod.users", false},
		{"db wildcard match", "test.users", "test.*", true},
		{"db wildcard mismatch", "test.users", "prod.*", false},
		{"table wildcard match", "test.users", "*.users", true},
		{"table wildcard mismatch", "test.users", "*.orders", false},
		{"db prefix wildcard", "app_archive.users", "app*.*", true},
		{"db prefix wildcard mismatch", "shop.users", "app*.*", false},
		{"table prefix wildcard", "test.order_items", "*.order*", true},
		{"combined patterns", "app_v2.users_backup", "app*.*_backup", true},

		// Invalid patterns (no dot) should return false
		{"invalid pattern no dot", "test.users", "users", false},
		{"invalid pattern wildcard no dot", "test.users", "user*", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchQualifiedPattern(tt.fullName, tt.pattern)
			if got != tt.want {
				t.Errorf("matchQualifiedPattern(%q, %q) = %v, want %v",
					tt.fullName, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		pattern string
		want    bool
	}{
		{"exact match", "users", "users", true},
		{"exact mismatch", "users", "orders", false},
		{"star matches all", "anything", "*", true},
		{"prefix wildcard match", "backup_users", "*_users", true},
		{"prefix wildcard mismatch", "backup_orders", "*_users", false},
		{"suffix wildcard match", "users_backup", "users_*", true},
		{"suffix wildcard mismatch", "orders_backup", "users_*", false},
		{"contains wildcard match", "old_users_backup", "*users*", true},
		{"contains wildcard mismatch", "old_orders_backup", "*users*", false},
		{"empty pattern", "users", "", false},
		{"empty name", "", "users", false},
		{"empty both", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPattern(tt.input, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.input, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchesAnyQualified(t *testing.T) {
	tests := []struct {
		name     string
		fullName string
		patterns []string
		want     bool
	}{
		{"empty patterns", "test.users", nil, false},
		{"single match", "test.users", []string{"test.users"}, true},
		{"single mismatch", "test.users", []string{"test.orders"}, false},
		{"multiple patterns first match", "test.users", []string{"test.*", "prod.*"}, true},
		{"multiple patterns second match", "prod.orders", []string{"test.*", "prod.*"}, true},
		{"multiple patterns no match", "dev.products", []string{"test.*", "prod.*"}, false},
		{"wildcard match", "test.users_backup", []string{"*.*_backup"}, true},
		{"mixed patterns", "app.orders", []string{"ad.*", "app*.*"}, true},
		{"invalid pattern ignored", "test.users", []string{"users"}, false},
		{"all tables pattern", "any.table", []string{"*.*"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesAnyQualified(tt.fullName, tt.patterns)
			if got != tt.want {
				t.Errorf("matchesAnyQualified(%q, %v) = %v, want %v",
					tt.fullName, tt.patterns, got, tt.want)
			}
		})
	}
}

// Integration tests - require real database connection
// Skip with -short flag

func getTestConnection(t *testing.T) *Connection {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cfg := getTestSourceConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "source", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	return conn
}

func TestMetaQuerier_GetTables(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Query tables from mysql schema (always exists)
	tables, err := mq.GetTables(ctx, "mysql")
	if err != nil {
		t.Fatalf("GetTables failed: %v", err)
	}

	if len(tables) == 0 {
		t.Error("expected at least one table in mysql schema")
	}

	// Check that user table exists (standard MySQL table)
	var foundUser bool
	for _, table := range tables {
		if table.Name == "user" {
			foundUser = true
			if table.Schema != "mysql" {
				t.Errorf("user table schema = %q, want %q", table.Schema, "mysql")
			}
			break
		}
	}

	if !foundUser {
		t.Log("'user' table not found in mysql schema (may vary by DB version)")
	}

	t.Logf("Found %d tables in mysql schema", len(tables))
}

func TestMetaQuerier_GetTableMeta(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get metadata for mysql.user table (has primary key)
	meta, err := mq.GetTableMeta(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("GetTableMeta failed: %v", err)
	}

	if meta.Schema != "mysql" {
		t.Errorf("schema = %q, want %q", meta.Schema, "mysql")
	}
	if meta.Name != "user" {
		t.Errorf("name = %q, want %q", meta.Name, "user")
	}

	// mysql.user should have columns
	if len(meta.Columns) == 0 {
		t.Error("expected columns for mysql.user table")
	}

	// Should have PK columns (Host, User)
	if len(meta.PKColumns) == 0 {
		t.Log("no primary key columns found (may vary by DB)")
	} else {
		t.Logf("PK columns: %v", meta.PKColumns)
	}

	t.Logf("Table %s has %d columns", meta.FullName(), len(meta.Columns))
}

func TestMetaQuerier_GetColumns(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	columns, err := mq.getColumns(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("getColumns failed: %v", err)
	}

	if len(columns) == 0 {
		t.Fatal("expected columns for mysql.user table")
	}

	// Check first column has expected fields
	col := columns[0]
	if col.Name == "" {
		t.Error("column name should not be empty")
	}
	if col.DataType == "" {
		t.Error("column data type should not be empty")
	}
	if col.OrdinalPos < 1 {
		t.Errorf("ordinal position should be >= 1, got %d", col.OrdinalPos)
	}

	t.Logf("Found %d columns, first: %s (%s)", len(columns), col.Name, col.DataType)
}

func TestMetaQuerier_GetPrimaryKeyColumns(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// mysql.user has composite PK (Host, User)
	pkCols, err := mq.getPrimaryKeyColumns(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("getPrimaryKeyColumns failed: %v", err)
	}

	if len(pkCols) == 0 {
		t.Log("no PK columns found for mysql.user (may vary by DB)")
	} else {
		t.Logf("PK columns for mysql.user: %v", pkCols)
	}
}

func TestMetaQuerier_GetUniqueKeyColumns(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try to get unique key columns (may or may not exist)
	ukCols, err := mq.getUniqueKeyColumns(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("getUniqueKeyColumns failed: %v", err)
	}

	// UK columns may be empty if table only has PK
	t.Logf("UK columns for mysql.user: %v (may be empty)", ukCols)
}

func TestMetaQuerier_GetEstimatedRowCount(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	count, err := mq.getEstimatedRowCount(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("getEstimatedRowCount failed: %v", err)
	}

	// mysql.user should have at least one row (root user)
	t.Logf("Estimated row count for mysql.user: %d", count)
}

func TestMetaQuerier_GetMinMax(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get min/max for Host column in mysql.user
	minVal, maxVal, err := mq.GetMinMax(ctx, "mysql", "user", []string{"Host"})
	if err != nil {
		t.Fatalf("GetMinMax failed: %v", err)
	}

	t.Logf("Min/Max for mysql.user.Host: min=%v, max=%v", minVal, maxVal)
}

func TestMetaQuerier_GetMinMax_NoKeyColumns(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _, err := mq.GetMinMax(ctx, "mysql", "user", []string{})
	if err == nil {
		t.Error("expected error for empty key columns")
	}
}

func TestNewMetaQuerier_NilLogger(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	// Should not panic with nil logger
	mq := NewMetaQuerier(conn, nil)
	if mq == nil {
		t.Error("NewMetaQuerier returned nil")
	}
	if mq.logger == nil {
		t.Error("logger should be set to nop logger, not nil")
	}
}

// Test with target
func TestMetaQuerier_Target(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cfg := getTestTargetConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "target", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect to target: %v", err)
	}
	defer conn.Close()

	mq := NewMetaQuerier(conn, nil)

	// Query tables from mysql schema
	tables, err := mq.GetTables(ctx, "mysql")
	if err != nil {
		t.Fatalf("GetTables on target failed: %v", err)
	}

	t.Logf("Found %d tables in target mysql schema", len(tables))

	// target should have user table too
	for _, table := range tables {
		if table.Name == "user" {
			t.Logf("Found user table in target with estimated %d rows", table.EstimatedRows)
			return
		}
	}
	t.Log("user table not found in target mysql schema (may vary)")
}
