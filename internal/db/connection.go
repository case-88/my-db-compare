package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
	_ "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
)

// PoolConfig holds connection pool configuration
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig returns default connection pool settings
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 1 * time.Minute,
	}
}

// Connection wraps a database connection with metadata
type Connection struct {
	DB     *sql.DB
	Config types.DBConfig
	Name   string // "source" or "target"
	logger *zap.Logger
}

// NewConnection creates a new database connection
func NewConnection(ctx context.Context, cfg types.DBConfig, name string, poolCfg PoolConfig, logger *zap.Logger) (*Connection, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	dsn := cfg.DSN()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(poolCfg.MaxOpenConns)
	db.SetMaxIdleConns(poolCfg.MaxIdleConns)
	db.SetConnMaxLifetime(poolCfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(poolCfg.ConnMaxIdleTime)

	// Verify connection
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger.Info("db connected",
		zap.String("name", name),
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("database", cfg.Database),
	)

	return &Connection{
		DB:     db,
		Config: cfg,
		Name:   name,
		logger: logger,
	}, nil
}

// Close closes the database connection
func (c *Connection) Close() error {
	if c.DB != nil {
		c.logger.Info("db closing", zap.String("name", c.Name))
		return c.DB.Close()
	}
	return nil
}

// Ping checks if the connection is alive
func (c *Connection) Ping(ctx context.Context) error {
	return c.DB.PingContext(ctx)
}

// QueryContext executes a query and returns rows
func (c *Connection) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return c.DB.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query and returns a single row
func (c *Connection) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return c.DB.QueryRowContext(ctx, query, args...)
}

// ExecContext executes a query without returning rows
func (c *Connection) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.DB.ExecContext(ctx, query, args...)
}

// Stats returns database connection pool statistics
func (c *Connection) Stats() sql.DBStats {
	return c.DB.Stats()
}

// ConnectionPair holds source and target connections
type ConnectionPair struct {
	Source *Connection
	Target *Connection
	logger *zap.Logger
}

// NewConnectionPair creates connections to both source and target databases
func NewConnectionPair(ctx context.Context, srcCfg, targetCfg types.DBConfig, poolCfg PoolConfig, logger *zap.Logger) (*ConnectionPair, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	src, err := NewConnection(ctx, srcCfg, "source", poolCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to source: %w", err)
	}

	target, err := NewConnection(ctx, targetCfg, "target", poolCfg, logger)
	if err != nil {
		src.Close()
		return nil, fmt.Errorf("failed to connect to target: %w", err)
	}

	return &ConnectionPair{
		Source: src,
		Target: target,
		logger: logger,
	}, nil
}

// Close closes both connections
func (cp *ConnectionPair) Close() error {
	var errs []error

	if err := cp.Source.Close(); err != nil {
		errs = append(errs, fmt.Errorf("source close error: %w", err))
	}

	if err := cp.Target.Close(); err != nil {
		errs = append(errs, fmt.Errorf("target close error: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
}
