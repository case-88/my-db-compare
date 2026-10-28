package runner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/internal/config"
	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// testDBConfig reads integration test connection settings from
// <prefix>_HOST, <prefix>_PORT, <prefix>_USER and <prefix>_PASSWORD.
func testDBConfig(prefix string, defaultPort int) types.DBConfig {
	port := defaultPort
	if p, err := strconv.Atoi(os.Getenv(prefix + "_PORT")); err == nil {
		port = p
	}
	return types.DBConfig{
		Host:     os.Getenv(prefix + "_HOST"),
		Port:     port,
		User:     os.Getenv(prefix + "_USER"),
		Password: os.Getenv(prefix + "_PASSWORD"),
		Database: "mysql",
	}
}

func getTestConfig() *config.Config {
	return &config.Config{
		Source: testDBConfig("TEST_SOURCE", 3306),
		Target: testDBConfig("TEST_TARGET", 4000),
		Comparison: config.Comparison{
			SampleSize:  100,
			Parallelism: 2,
		},
		Tables: config.TableFilter{
			Include: []string{},
			Exclude: []string{},
		},
	}
}

func skipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("TEST_SOURCE_HOST") == "" || os.Getenv("TEST_TARGET_HOST") == "" {
		t.Skip("skipping integration test: TEST_SOURCE_HOST / TEST_TARGET_HOST not set")
	}
}

func TestNew(t *testing.T) {
	cfg := getTestConfig()
	logger := zap.NewNop()

	r, err := New(cfg, "./test-output", "sample", "", logger)
	require.NoError(t, err)
	assert.NotNil(t, r)
	assert.Equal(t, cfg, r.cfg)
	assert.Equal(t, "./test-output", r.outputDir)
}

func TestNew_NilLogger(t *testing.T) {
	cfg := getTestConfig()

	r, err := New(cfg, "./test-output", "sample", "", nil)
	require.NoError(t, err)
	assert.NotNil(t, r)
	assert.NotNil(t, r.logger) // Should use nop logger
}

func TestRunner_Connect(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	logger, _ := zap.NewDevelopment()

	r, err := New(cfg, "./test-output", "sample", "", logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Test connection
	err = r.connect(ctx)
	require.NoError(t, err)

	// Verify connections are established
	assert.NotNil(t, r.srcConn)
	assert.NotNil(t, r.targetConn)
	assert.NotNil(t, r.srcMeta)
	assert.NotNil(t, r.targetMeta)

	// Cleanup
	r.disconnect()
}

func TestRunner_GetTablesToCompare(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	// Include specific table for test
	cfg.Tables.Include = []string{cfg.Source.Database + ".user"}

	logger, _ := zap.NewDevelopment()

	r, err := New(cfg, "./test-output", "sample", "", logger)
	require.NoError(t, err)

	ctx := context.Background()

	err = r.connect(ctx)
	require.NoError(t, err)
	defer r.disconnect()

	tables, err := r.getTablesToCompare(ctx)
	require.NoError(t, err)

	// Should find at least one table
	assert.NotEmpty(t, tables)

	// Should be the user table
	found := false
	for _, tbl := range tables {
		if tbl.Name == "user" {
			found = true
			assert.NotEmpty(t, tbl.Columns)
			break
		}
	}
	assert.True(t, found, "user table should be found")
}

func TestRunner_DryRun(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	cfg.Tables.Include = []string{cfg.Source.Database + ".user"}

	logger, _ := zap.NewDevelopment()

	r, err := New(cfg, "./test-output", "sample", "", logger)
	require.NoError(t, err)

	ctx := context.Background()

	// Dry run should not fail
	err = r.DryRun(ctx)
	require.NoError(t, err)
}

func TestRunner_Run(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	cfg.Tables.Include = []string{cfg.Source.Database + ".user"}
	cfg.Comparison.SampleSize = 10 // Small sample for test

	logger, _ := zap.NewDevelopment()

	// Create temp output dir
	outputDir := filepath.Join(os.TempDir(), "db-compare-test-"+time.Now().Format("20060102150405"))
	defer os.RemoveAll(outputDir)

	r, err := New(cfg, outputDir, "sample", "", logger)
	require.NoError(t, err)

	ctx := context.Background()

	result, err := r.Run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Summary)

	// Verify summary has results
	assert.GreaterOrEqual(t, result.Summary.TotalTables, 1)
}

func TestRunner_Run_WithCancellation(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	cfg.Tables.Include = []string{cfg.Source.Database + ".*"} // All tables
	cfg.Comparison.SampleSize = 1000                          // Larger to take time

	logger, _ := zap.NewDevelopment()

	outputDir := filepath.Join(os.TempDir(), "db-compare-cancel-test")
	defer os.RemoveAll(outputDir)

	r, err := New(cfg, outputDir, "sample", "", logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	// Should handle cancellation gracefully
	_, err = r.Run(ctx)
	// May or may not error depending on timing
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	}
}

func TestRunner_CompareTable(t *testing.T) {
	skipIfShort(t)

	cfg := getTestConfig()
	cfg.Comparison.SampleSize = 10

	logger, _ := zap.NewDevelopment()

	outputDir := filepath.Join(os.TempDir(), "db-compare-table-test")
	defer os.RemoveAll(outputDir)

	r, err := New(cfg, outputDir, "sample", "", logger)
	require.NoError(t, err)

	ctx := context.Background()

	err = r.connect(ctx)
	require.NoError(t, err)
	defer r.disconnect()

	// Initialize components
	r.srcSampler = nil // Will be initialized in compareTable path
	r.targetSampler = nil

	// Get a table to compare
	tables, err := r.getTablesToCompare(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tables)

	// Find a table with a key
	var tableWithKey *types.TableMeta
	for i := range tables {
		if tables[i].HasKey() {
			tableWithKey = &tables[i]
			break
		}
	}

	if tableWithKey == nil {
		t.Skip("No table with key found for testing")
	}

	// Need to initialize samplers
	r.srcSampler = nil
	r.targetSampler = nil

	// Test skipping table without key
	tableNoKey := types.TableMeta{
		Schema: cfg.Source.Database,
		Name:   "no_key_table",
	}
	result := r.compareTable(ctx, tableNoKey)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.SkipReason, "no primary key")
}
