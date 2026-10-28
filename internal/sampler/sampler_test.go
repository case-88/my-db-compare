package sampler

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/pkg/types"
)

func TestBuildColumnList(t *testing.T) {
	tests := []struct {
		name    string
		columns []string
		want    string
	}{
		{
			name:    "empty columns",
			columns: []string{},
			want:    "*",
		},
		{
			name:    "nil columns",
			columns: nil,
			want:    "*",
		},
		{
			name:    "single column",
			columns: []string{"id"},
			want:    "`id`",
		},
		{
			name:    "multiple columns",
			columns: []string{"id", "name", "email"},
			want:    "`id`, `name`, `email`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildColumnList(tt.columns)
			if got != tt.want {
				t.Errorf("buildColumnList() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractKeyValues(t *testing.T) {
	row := types.Row{
		"id":    int64(123),
		"name":  "test",
		"email": "test@example.com",
	}

	tests := []struct {
		name       string
		keyColumns []string
		wantLen    int
		wantKeys   []string
	}{
		{
			name:       "single key",
			keyColumns: []string{"id"},
			wantLen:    1,
			wantKeys:   []string{"id"},
		},
		{
			name:       "composite key",
			keyColumns: []string{"id", "name"},
			wantLen:    2,
			wantKeys:   []string{"id", "name"},
		},
		{
			name:       "missing key column",
			keyColumns: []string{"nonexistent"},
			wantLen:    0,
			wantKeys:   nil,
		},
		{
			name:       "empty key columns",
			keyColumns: []string{},
			wantLen:    0,
			wantKeys:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractKeyValues(row, tt.keyColumns)

			if len(got) != tt.wantLen {
				t.Errorf("len(result) = %d, want %d", len(got), tt.wantLen)
				return
			}

			for _, key := range tt.wantKeys {
				if _, ok := got[key]; !ok {
					t.Errorf("missing key %q in result", key)
				}
			}
		})
	}
}

func TestNewSampler_NilLogger(t *testing.T) {
	// Should not panic with nil logger
	sampler := NewSampler(nil, nil)
	if sampler == nil {
		t.Error("NewSampler returned nil")
	}
	if sampler.logger == nil {
		t.Error("logger should be set to nop logger, not nil")
	}
}

// Integration tests - require real database connection

func skipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

func getTestConnection(t *testing.T) *db.Connection {
	skipIfShort(t)

	host := os.Getenv("TEST_SOURCE_HOST")
	if host == "" {
		t.Skip("skipping integration test: TEST_SOURCE_HOST not set")
	}
	port := 3306
	if p, err := strconv.Atoi(os.Getenv("TEST_SOURCE_PORT")); err == nil {
		port = p
	}
	cfg := types.DBConfig{
		Host:     host,
		Port:     port,
		User:     os.Getenv("TEST_SOURCE_USER"),
		Password: os.Getenv("TEST_SOURCE_PASSWORD"),
		Database: "mysql",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := db.NewConnection(ctx, cfg, "source", db.DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	return conn
}

func TestSampler_SampleTableDirect(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema: "mysql",
		Name:   "user",
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
			{Name: "User", DataType: "char"},
		},
	}

	result, err := sampler.SampleTableDirect(ctx, table, 5)
	if err != nil {
		t.Fatalf("SampleTableDirect failed: %v", err)
	}

	if result.Total == 0 {
		t.Error("expected at least one sample")
	}

	if result.Total > 5 {
		t.Errorf("got %d samples, want <= 5", result.Total)
	}

	t.Logf("Sampled %d rows from mysql.user", result.Total)

	// Check that rows have expected columns
	if len(result.Samples) > 0 {
		row := result.Samples[0]
		if _, ok := row["Host"]; !ok {
			t.Error("row missing Host column")
		}
		if _, ok := row["User"]; !ok {
			t.Error("row missing User column")
		}
	}
}

func TestSampler_SamplePartition(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema:    "mysql",
		Name:      "user",
		PKColumns: []string{"Host"},
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
			{Name: "User", DataType: "char"},
		},
	}

	partition := Partition{
		Index:    0,
		MinValue: "%",  // Wildcard host
		MaxValue: "z",  // Up to 'z'
		Samples:  3,
	}

	result, err := sampler.SamplePartition(ctx, table, partition, []string{"Host", "User"})
	if err != nil {
		t.Fatalf("SamplePartition failed: %v", err)
	}

	t.Logf("Sampled %d rows from partition %d", len(result.Rows), partition.Index)

	if result.Error != nil {
		t.Errorf("unexpected error in result: %v", result.Error)
	}
}

func TestSampler_SampleByKey(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	// First, get a known row
	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema:    "mysql",
		Name:      "user",
		PKColumns: []string{"Host", "User"},
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
			{Name: "User", DataType: "char"},
		},
	}

	// Sample one row first
	directResult, err := sampler.SampleTableDirect(ctx, table, 1)
	if err != nil {
		t.Fatalf("failed to get sample row: %v", err)
	}

	if len(directResult.Samples) == 0 {
		t.Skip("no rows available for testing")
	}

	// Extract key values
	sampleRow := directResult.Samples[0]
	keyValues := ExtractKeyValues(sampleRow, table.PKColumns)

	t.Logf("Looking up row with keys: %v", keyValues)

	// Now lookup by key
	row, err := sampler.SampleByKey(ctx, table, keyValues)
	if err != nil {
		t.Fatalf("SampleByKey failed: %v", err)
	}

	if row == nil {
		t.Fatal("row not found")
	}

	// Verify row was found (key lookup worked)
	if row == nil {
		t.Fatal("row not found by key lookup")
	}

	// Just verify the row has the expected key columns
	for k := range keyValues {
		if _, ok := row[k]; !ok {
			t.Errorf("returned row missing key column %q", k)
		}
	}
}

func TestSampler_SampleByKey_NotFound(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema:    "mysql",
		Name:      "user",
		PKColumns: []string{"Host", "User"},
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
			{Name: "User", DataType: "char"},
		},
	}

	// Use non-existent key values
	keyValues := map[string]interface{}{
		"Host": "nonexistent-host-12345",
		"User": "nonexistent-user-12345",
	}

	row, err := sampler.SampleByKey(ctx, table, keyValues)
	if err != nil {
		t.Fatalf("SampleByKey failed: %v", err)
	}

	if row != nil {
		t.Error("expected nil row for non-existent key")
	}
}

func TestSampler_SampleByKey_MissingKeyValue(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema:    "mysql",
		Name:      "user",
		PKColumns: []string{"Host", "User"},
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
			{Name: "User", DataType: "char"},
		},
	}

	// Missing User key
	keyValues := map[string]interface{}{
		"Host": "localhost",
	}

	_, err := sampler.SampleByKey(ctx, table, keyValues)
	if err == nil {
		t.Error("expected error for missing key value")
	}
}

func TestSampler_SampleByKey_NoKeyColumns(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	sampler := NewSampler(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := types.TableMeta{
		Schema:    "mysql",
		Name:      "user",
		PKColumns: []string{}, // No key columns
		Columns: []types.ColumnMeta{
			{Name: "Host", DataType: "char"},
		},
	}

	keyValues := map[string]interface{}{}

	_, err := sampler.SampleByKey(ctx, table, keyValues)
	if err == nil {
		t.Error("expected error for no key columns")
	}
}

func TestSampler_SampleTable(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	// First get table metadata
	mq := db.NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tableMeta, err := mq.GetTableMeta(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("failed to get table meta: %v", err)
	}

	if len(tableMeta.PKColumns) == 0 {
		t.Skip("mysql.user has no PK columns in this DB")
	}

	// Get min/max for partitioning
	keyCol := tableMeta.PKColumns[0]
	minVal, maxVal, err := mq.GetMinMax(ctx, "mysql", "user", []string{keyCol})
	if err != nil {
		t.Fatalf("failed to get min/max: %v", err)
	}

	t.Logf("Key column %s: min=%v, max=%v", keyCol, minVal, maxVal)

	// Create partition plan
	cfg := PartitionConfig{
		SampleSize:       20,
		MinPartitions:    2,
		MaxPartitions:    10,
		LargeTableThresh: 1000000,
	}

	plan, err := CreatePartitionPlan(*tableMeta, keyCol, minVal, maxVal, cfg)
	if err != nil {
		t.Fatalf("failed to create partition plan: %v", err)
	}

	t.Logf("Created plan with %d partitions", len(plan.Partitions))

	// Sample using the plan
	sampler := NewSampler(conn, nil)
	result, err := sampler.SampleTable(ctx, plan)
	if err != nil {
		t.Fatalf("SampleTable failed: %v", err)
	}

	t.Logf("Sampled %d rows total from %d partitions", result.Total, len(plan.Partitions))

	// Verify we got some samples
	if result.Total == 0 {
		t.Error("expected at least one sample")
	}
}

func TestSampler_SampleTable_NilPlan(t *testing.T) {
	sampler := NewSampler(nil, nil)
	ctx := context.Background()

	_, err := sampler.SampleTable(ctx, nil)
	if err == nil {
		t.Error("expected error for nil plan")
	}
}

func TestSampler_SampleTableWithColumnsParallel_NilPlan(t *testing.T) {
	sampler := NewSampler(nil, nil)
	ctx := context.Background()

	_, err := sampler.SampleTableWithColumnsParallel(ctx, nil, nil, 5)
	if err == nil {
		t.Error("expected error for nil plan")
	}
}

func TestSampler_SampleTableWithColumnsParallel_ZeroParallelism(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	// Get table metadata
	mq := db.NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tableMeta, err := mq.GetTableMeta(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("failed to get table meta: %v", err)
	}

	if len(tableMeta.PKColumns) == 0 {
		t.Skip("mysql.user has no PK columns in this DB")
	}

	// Get min/max for partitioning
	keyCol := tableMeta.PKColumns[0]
	minVal, maxVal, err := mq.GetMinMax(ctx, "mysql", "user", []string{keyCol})
	if err != nil {
		t.Fatalf("failed to get min/max: %v", err)
	}

	// Create partition plan
	cfg := PartitionConfig{
		SampleSize:        20,
		MinPartitions:     4,
		MaxPartitions:     10,
		DisableRandomness: true, // For deterministic testing
	}

	plan, err := CreatePartitionPlan(*tableMeta, keyCol, minVal, maxVal, cfg)
	if err != nil {
		t.Fatalf("failed to create partition plan: %v", err)
	}

	// Test with zero parallelism (should use default)
	sampler := NewSampler(conn, nil)
	columns := []string{"Host", "User"}

	result, err := sampler.SampleTableWithColumnsParallel(ctx, plan, columns, 0)
	if err != nil {
		t.Fatalf("SampleTableWithColumnsParallel failed: %v", err)
	}

	t.Logf("Sampled %d rows with zero parallelism (used default)", result.Total)

	if result.Total == 0 {
		t.Error("expected at least one sample")
	}
}

func TestSampler_SampleTableWithColumnsParallel_HighParallelism(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := db.NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tableMeta, err := mq.GetTableMeta(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("failed to get table meta: %v", err)
	}

	if len(tableMeta.PKColumns) == 0 {
		t.Skip("mysql.user has no PK columns in this DB")
	}

	keyCol := tableMeta.PKColumns[0]
	minVal, maxVal, err := mq.GetMinMax(ctx, "mysql", "user", []string{keyCol})
	if err != nil {
		t.Fatalf("failed to get min/max: %v", err)
	}

	// Create partition plan with few partitions
	cfg := PartitionConfig{
		SampleSize:        20,
		MinPartitions:     3,
		MaxPartitions:     5,
		DisableRandomness: true,
	}

	plan, err := CreatePartitionPlan(*tableMeta, keyCol, minVal, maxVal, cfg)
	if err != nil {
		t.Fatalf("failed to create partition plan: %v", err)
	}

	// Test with parallelism higher than partition count (should be limited)
	sampler := NewSampler(conn, nil)
	columns := []string{"Host", "User"}

	result, err := sampler.SampleTableWithColumnsParallel(ctx, plan, columns, 100)
	if err != nil {
		t.Fatalf("SampleTableWithColumnsParallel failed: %v", err)
	}

	t.Logf("Sampled %d rows with high parallelism (limited to partition count)", result.Total)

	if result.Total == 0 {
		t.Error("expected at least one sample")
	}
}

func TestSampler_SampleTableWithColumnsParallel_ContextCancellation(t *testing.T) {
	conn := getTestConnection(t)
	defer conn.Close()

	mq := db.NewMetaQuerier(conn, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tableMeta, err := mq.GetTableMeta(ctx, "mysql", "user")
	if err != nil {
		t.Fatalf("failed to get table meta: %v", err)
	}

	if len(tableMeta.PKColumns) == 0 {
		t.Skip("mysql.user has no PK columns in this DB")
	}

	keyCol := tableMeta.PKColumns[0]
	minVal, maxVal, err := mq.GetMinMax(ctx, "mysql", "user", []string{keyCol})
	if err != nil {
		t.Fatalf("failed to get min/max: %v", err)
	}

	cfg := PartitionConfig{
		SampleSize:        20,
		MinPartitions:     5,
		MaxPartitions:     10,
		DisableRandomness: true,
	}

	plan, err := CreatePartitionPlan(*tableMeta, keyCol, minVal, maxVal, cfg)
	if err != nil {
		t.Fatalf("failed to create partition plan: %v", err)
	}

	// Create a context that's already cancelled
	cancelledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn() // Cancel immediately

	sampler := NewSampler(conn, nil)
	columns := []string{"Host", "User"}

	_, err = sampler.SampleTableWithColumnsParallel(cancelledCtx, plan, columns, 5)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestDefaultPartitionParallelism(t *testing.T) {
	// Verify the default parallelism constant is reasonable
	if DefaultPartitionParallelism <= 0 {
		t.Errorf("DefaultPartitionParallelism should be positive, got %d", DefaultPartitionParallelism)
	}
	if DefaultPartitionParallelism > 10 {
		t.Errorf("DefaultPartitionParallelism should not exceed connection pool size, got %d", DefaultPartitionParallelism)
	}
}

func TestBuildSelectList(t *testing.T) {
	plain := types.TableMeta{}
	mapped := types.TableMeta{ColumnMap: map[string]string{"id": "member_id", "name": "full_name"}}

	tests := []struct {
		name    string
		table   types.TableMeta
		columns []string
		want    string
	}{
		{"empty columns", plain, nil, "*"},
		{"no mapping", plain, []string{"id", "name"}, "`id`, `name`"},
		{"mapped columns are aliased", mapped, []string{"id", "name", "email"}, "`member_id` AS `id`, `full_name` AS `name`, `email`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildSelectList(tt.table, tt.columns); got != tt.want {
				t.Errorf("buildSelectList() = %q, want %q", got, tt.want)
			}
		})
	}
}
