package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 3306, cfg.Source.Port)
	assert.Equal(t, 3306, cfg.Target.Port)
	assert.Equal(t, 1000, cfg.Comparison.SampleSize)
	assert.Equal(t, 5, cfg.Comparison.Parallelism)
	assert.Equal(t, "jsonl", cfg.Output.Format)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr string
	}{
		{
			name:    "valid config",
			modify:  func(c *Config) {},
			wantErr: "",
		},
		{
			name: "missing source host",
			modify: func(c *Config) {
				c.Source.Host = ""
			},
			wantErr: "source host is required",
		},
		{
			name: "missing source user",
			modify: func(c *Config) {
				c.Source.User = ""
			},
			wantErr: "source user is required",
		},
		{
			name: "missing target host",
			modify: func(c *Config) {
				c.Target.Host = ""
			},
			wantErr: "target host is required",
		},
		{
			name: "missing target user",
			modify: func(c *Config) {
				c.Target.User = ""
			},
			wantErr: "target user is required",
		},
		{
			name: "invalid sample size",
			modify: func(c *Config) {
				c.Comparison.SampleSize = 0
			},
			wantErr: "sample_size must be at least 1",
		},
		{
			name: "invalid parallelism",
			modify: func(c *Config) {
				c.Comparison.Parallelism = 0
			},
			wantErr: "parallelism must be at least 1",
		},
		{
			name: "valid with empty include and database",
			modify: func(c *Config) {
				// Both empty is now valid - will scan all user databases
				c.Tables.Include = []string{}
				c.Source.Database = ""
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExpandEnv(t *testing.T) {
	os.Setenv("TEST_PASSWORD", "secret123")
	defer os.Unsetenv("TEST_PASSWORD")

	tests := []struct {
		input    string
		expected string
	}{
		{"${TEST_PASSWORD}", "secret123"},
		{"$TEST_PASSWORD", "secret123"},
		{"plaintext", "plaintext"},
		{"${NONEXISTENT}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := expandEnv(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetSchemas(t *testing.T) {
	t.Run("extracts schemas from include patterns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tables.Include = []string{"mydb.*", "analytics.events*"}

		schemas := cfg.GetSchemas()

		assert.Len(t, schemas, 2)
		assert.Contains(t, schemas, "mydb")
		assert.Contains(t, schemas, "analytics")
	})

	t.Run("returns nil if include empty to scan all databases", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tables.Include = []string{}
		cfg.Source.Database = "mydb" // database is only for connection, not table discovery

		schemas := cfg.GetSchemas()

		assert.Nil(t, schemas) // nil means scan all user databases
	})

	t.Run("returns nil for wildcard db patterns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tables.Include = []string{"*.*"}

		schemas := cfg.GetSchemas()

		assert.Nil(t, schemas)
	})

	t.Run("returns nil for prefix wildcard patterns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tables.Include = []string{"app*.*"}

		schemas := cfg.GetSchemas()

		assert.Nil(t, schemas)
	})

	t.Run("returns nil if include empty and database empty", func(t *testing.T) {
		cfg := validConfig()
		cfg.Tables.Include = []string{}
		cfg.Source.Database = ""

		schemas := cfg.GetSchemas()

		assert.Nil(t, schemas)
	})
}

func TestComparisonMode(t *testing.T) {
	t.Run("default mode is sampling", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, ModeSampling, cfg.Comparison.GetMode())
		assert.False(t, cfg.Comparison.IsFullMode())
	})

	t.Run("explicit sampling mode", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.Mode = "sampling"
		assert.Equal(t, ModeSampling, cfg.Comparison.GetMode())
		assert.False(t, cfg.Comparison.IsFullMode())
	})

	t.Run("full mode", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.Mode = "full"
		assert.Equal(t, ModeFull, cfg.Comparison.GetMode())
		assert.True(t, cfg.Comparison.IsFullMode())
	})

	t.Run("unknown mode defaults to sampling", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.Mode = "unknown"
		assert.Equal(t, ModeSampling, cfg.Comparison.GetMode())
		assert.False(t, cfg.Comparison.IsFullMode())
	})
}

func TestFullModeConfig(t *testing.T) {
	t.Run("default chunk size", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, 2000, cfg.Comparison.GetFullModeChunkSize())
	})

	t.Run("custom chunk size", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeChunkSize = 10000
		assert.Equal(t, 10000, cfg.Comparison.GetFullModeChunkSize())
	})

	t.Run("zero chunk size returns default", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeChunkSize = 0
		assert.Equal(t, 2000, cfg.Comparison.GetFullModeChunkSize())
	})

	t.Run("negative chunk size returns default", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeChunkSize = -1
		assert.Equal(t, 2000, cfg.Comparison.GetFullModeChunkSize())
	})

	t.Run("default delay ms", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, 10, cfg.Comparison.GetFullModeDelayMs())
	})

	t.Run("custom delay ms", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeDelayMs = 100
		assert.Equal(t, 100, cfg.Comparison.GetFullModeDelayMs())
	})

	t.Run("zero delay ms is valid", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeDelayMs = 0
		assert.Equal(t, 0, cfg.Comparison.GetFullModeDelayMs())
	})

	t.Run("negative delay ms returns default", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeDelayMs = -1
		assert.Equal(t, 10, cfg.Comparison.GetFullModeDelayMs())
	})

	t.Run("default parallelism", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, 4, cfg.Comparison.GetFullModeParallelism())
	})

	t.Run("custom parallelism", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeParallelism = 8
		assert.Equal(t, 8, cfg.Comparison.GetFullModeParallelism())
	})

	t.Run("zero parallelism returns default", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Comparison.FullModeParallelism = 0
		assert.Equal(t, 4, cfg.Comparison.GetFullModeParallelism())
	})
}

func TestIsLargeColumnType(t *testing.T) {
	tests := []struct {
		colType  string
		expected bool
	}{
		{"text", true},
		{"TEXT", true},
		{"mediumtext", true},
		{"MEDIUMTEXT", true},
		{"longtext", true},
		{"blob", true},
		{"mediumblob", true},
		{"longblob", true},
		{"json", true},
		{"JSON", true},
		{"varchar(255)", false},
		{"int", false},
		{"bigint", false},
		{"datetime", false},
		{"decimal(10,2)", false},
	}

	for _, tt := range tests {
		t.Run(tt.colType, func(t *testing.T) {
			result := IsLargeColumnType(tt.colType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// validConfig returns a valid configuration for testing
func validConfig() *Config {
	cfg := DefaultConfig()
	cfg.Source.Host = "localhost"
	cfg.Source.User = "root"
	cfg.Source.Database = "testdb"
	cfg.Target.Host = "localhost"
	cfg.Target.User = "root"
	cfg.Target.Database = "testdb"
	return cfg
}
