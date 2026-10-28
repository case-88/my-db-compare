package db

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

func TestDefaultPoolConfig(t *testing.T) {
	cfg := DefaultPoolConfig()

	if cfg.MaxOpenConns != 10 {
		t.Errorf("MaxOpenConns = %d, want 10", cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns != 5 {
		t.Errorf("MaxIdleConns = %d, want 5", cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime != 5*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 5m", cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime != 1*time.Minute {
		t.Errorf("ConnMaxIdleTime = %v, want 1m", cfg.ConnMaxIdleTime)
	}
}

// testDBConfig reads integration test connection settings from
// <prefix>_HOST, <prefix>_PORT, <prefix>_USER and <prefix>_PASSWORD.
// The test is skipped when <prefix>_HOST is not set.
func testDBConfig(t *testing.T, prefix string, defaultPort int) *types.DBConfig {
	t.Helper()
	host := os.Getenv(prefix + "_HOST")
	if host == "" {
		t.Skipf("skipping integration test: %s_HOST not set", prefix)
	}
	port := defaultPort
	if p, err := strconv.Atoi(os.Getenv(prefix + "_PORT")); err == nil {
		port = p
	}
	return &types.DBConfig{
		Host:     host,
		Port:     port,
		User:     os.Getenv(prefix + "_USER"),
		Password: os.Getenv(prefix + "_PASSWORD"),
		Database: "information_schema",
	}
}

func getTestSourceConfig(t *testing.T) *types.DBConfig {
	return testDBConfig(t, "TEST_SOURCE", 3306)
}

func getTestTargetConfig(t *testing.T) *types.DBConfig {
	return testDBConfig(t, "TEST_TARGET", 4000)
}

func skipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

func TestNewConnection_Source(t *testing.T) {
	skipIfShort(t)

	cfg := getTestSourceConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "source", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect to source DB: %v", err)
	}
	defer conn.Close()

	// Verify connection is alive
	if err := conn.Ping(ctx); err != nil {
		t.Errorf("ping failed: %v", err)
	}

	// Check connection name
	if conn.Name != "source" {
		t.Errorf("connection name = %q, want %q", conn.Name, "source")
	}
}

func TestNewConnection_Target(t *testing.T) {
	skipIfShort(t)

	cfg := getTestTargetConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "target", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect to target DB: %v", err)
	}
	defer conn.Close()

	// Verify connection is alive
	if err := conn.Ping(ctx); err != nil {
		t.Errorf("ping failed: %v", err)
	}

	// Check connection name
	if conn.Name != "target" {
		t.Errorf("connection name = %q, want %q", conn.Name, "target")
	}
}

func TestNewConnection_InvalidHost(t *testing.T) {
	cfg := types.DBConfig{
		Host:     "invalid-host-that-does-not-exist.example.com",
		Port:     3306,
		User:     "test",
		Password: "test",
		Database: "test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := NewConnection(ctx, cfg, "test", DefaultPoolConfig(), nil)
	if err == nil {
		t.Error("expected error for invalid host, got nil")
	}
}

func TestConnectionPair(t *testing.T) {
	skipIfShort(t)

	srcCfg := getTestSourceConfig(t)
	targetCfg := getTestTargetConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pair, err := NewConnectionPair(ctx, *srcCfg, *targetCfg, DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to create connection pair: %v", err)
	}
	defer pair.Close()

	// Verify both connections
	if err := pair.Source.Ping(ctx); err != nil {
		t.Errorf("source ping failed: %v", err)
	}
	if err := pair.Target.Ping(ctx); err != nil {
		t.Errorf("target ping failed: %v", err)
	}
}

func TestConnection_Stats(t *testing.T) {
	skipIfShort(t)

	cfg := getTestSourceConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "source", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	stats := conn.Stats()

	// MaxOpenConnections should match pool config
	if stats.MaxOpenConnections != 10 {
		t.Errorf("MaxOpenConnections = %d, want 10", stats.MaxOpenConnections)
	}
}

func TestConnection_QueryContext(t *testing.T) {
	skipIfShort(t)

	cfg := getTestSourceConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "source", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	// Simple query test
	rows, err := conn.QueryContext(ctx, "SELECT 1 AS num")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("expected one row")
	}

	var num int
	if err := rows.Scan(&num); err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	if num != 1 {
		t.Errorf("num = %d, want 1", num)
	}
}

func TestConnection_QueryRowContext(t *testing.T) {
	skipIfShort(t)

	cfg := getTestSourceConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := NewConnection(ctx, *cfg, "source", DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer conn.Close()

	var version string
	err = conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version)
	if err != nil {
		t.Fatalf("query row failed: %v", err)
	}

	if version == "" {
		t.Error("version should not be empty")
	}

	t.Logf("Database version: %s", version)
}
