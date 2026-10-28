package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/spf13/viper"
)

// Config holds the application configuration
type Config struct {
	Source     types.DBConfig `yaml:"source" mapstructure:"source"`
	Target     types.DBConfig `yaml:"target" mapstructure:"target"`
	Comparison Comparison     `yaml:"comparison" mapstructure:"comparison"`
	Tables     TableFilter    `yaml:"tables" mapstructure:"tables"`
	Output     Output         `yaml:"output" mapstructure:"output"`
	Mappings   []TableMapping `yaml:"mappings" mapstructure:"mappings"`
}

// TableMapping pairs a source table with a target table that has a different
// name or different column names. Columns not listed keep the same name.
type TableMapping struct {
	Source  string            `yaml:"source" mapstructure:"source"`   // source "database.table"
	Target  string            `yaml:"target" mapstructure:"target"`   // target "database.table"
	Columns map[string]string `yaml:"columns" mapstructure:"columns"` // source column -> target column
	Exclude []string          `yaml:"exclude" mapstructure:"exclude"` // source columns left out of the comparison
}

// ExcludeSet returns the excluded source columns, lowercased
func (m TableMapping) ExcludeSet() map[string]bool {
	if len(m.Exclude) == 0 {
		return nil
	}
	result := make(map[string]bool, len(m.Exclude))
	for _, col := range m.Exclude {
		result[strings.ToLower(col)] = true
	}
	return result
}

// ColumnMap returns the column mapping with lowercase source column keys
func (m TableMapping) ColumnMap() map[string]string {
	if len(m.Columns) == 0 {
		return nil
	}
	result := make(map[string]string, len(m.Columns))
	for src, target := range m.Columns {
		result[strings.ToLower(src)] = target
	}
	return result
}

// SplitTableName splits "database.table" into its parts.
// Wildcards are not allowed.
func SplitTableName(name string) (schema, table string, ok bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(name, "*") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// MappingFor returns the mapping whose source is the given "database.table", or nil
func (c *Config) MappingFor(source string) *TableMapping {
	for i := range c.Mappings {
		if strings.EqualFold(c.Mappings[i].Source, source) {
			return &c.Mappings[i]
		}
	}
	return nil
}

// CompareMode represents the comparison mode
type CompareMode string

const (
	ModeSampling CompareMode = "sampling"
	ModeFull     CompareMode = "full"
)

// Comparison holds comparison-related settings
type Comparison struct {
	Mode                      string `yaml:"mode" mapstructure:"mode"` // sampling / full (default: sampling)
	SampleSize                int    `yaml:"sample_size" mapstructure:"sample_size"`
	Parallelism               int    `yaml:"parallelism" mapstructure:"parallelism"`
	IgnoreDatetimePrecision   *bool  `yaml:"ignore_datetime_precision" mapstructure:"ignore_datetime_precision"`     // nil = true (default: compare at second precision)
	IgnoreZeroDatetime        *bool  `yaml:"ignore_zero_datetime" mapstructure:"ignore_zero_datetime"`               // nil = true (default: treat 0000-00-00 00:00:00 as NULL)
	IgnoreJSONKeyOrder        *bool  `yaml:"ignore_json_key_order" mapstructure:"ignore_json_key_order"`             // nil = true (default: compare JSON semantically)
	IgnoreLargeColumns        *bool  `yaml:"ignore_large_columns" mapstructure:"ignore_large_columns"`               // nil = false (default: include all columns)
	GenerateCorrectionQueries *bool  `yaml:"generate_correction_queries" mapstructure:"generate_correction_queries"` // nil = false (default: don't generate)

	// Full mode specific options
	FullModeChunkSize   int `yaml:"full_mode_chunk_size" mapstructure:"full_mode_chunk_size"`   // chunk size for full mode (default: 2000)
	FullModeDelayMs     int `yaml:"full_mode_delay_ms" mapstructure:"full_mode_delay_ms"`       // delay between chunks in ms (default: 10)
	FullModeParallelism int `yaml:"full_mode_parallelism" mapstructure:"full_mode_parallelism"` // partition parallelism within table (default: 4)
}

// ShouldIgnoreDatetimePrecision returns whether to ignore datetime precision (default: true)
// When true, datetime values are compared at second precision (microseconds ignored)
func (c *Comparison) ShouldIgnoreDatetimePrecision() bool {
	if c.IgnoreDatetimePrecision == nil {
		return true // default: ignore precision (compare at seconds)
	}
	return *c.IgnoreDatetimePrecision
}

// ShouldIgnoreZeroDatetime returns whether to treat zero datetime as NULL (default: true)
// When true, '0000-00-00 00:00:00' in source equals NULL in target (and vice versa)
// This is useful for MySQL to TiDB migration where zero datetime is converted to NULL
func (c *Comparison) ShouldIgnoreZeroDatetime() bool {
	if c.IgnoreZeroDatetime == nil {
		return true // default: treat zero datetime as NULL
	}
	return *c.IgnoreZeroDatetime
}

// ShouldIgnoreJSONKeyOrder returns whether to ignore JSON key order (default: true)
// When true, JSON objects are compared semantically (key order ignored)
// This is useful for MySQL vs TiDB comparison where TiDB normalizes JSON keys alphabetically
func (c *Comparison) ShouldIgnoreJSONKeyOrder() bool {
	if c.IgnoreJSONKeyOrder == nil {
		return true // default: ignore key order (semantic comparison)
	}
	return *c.IgnoreJSONKeyOrder
}

// ShouldIgnoreLargeColumns returns whether to ignore large columns from comparison (default: false)
// When true, TEXT, MEDIUMTEXT, LONGTEXT, BLOB, MEDIUMBLOB, LONGBLOB, JSON columns are excluded
// This improves performance by reducing data transfer for large column types
func (c *Comparison) ShouldIgnoreLargeColumns() bool {
	if c.IgnoreLargeColumns == nil {
		return false // default: include all columns
	}
	return *c.IgnoreLargeColumns
}

// ShouldGenerateCorrectionQueries returns whether to generate correction queries (default: false)
// When true, generates UPDATE/INSERT/DELETE queries to fix target data
func (c *Comparison) ShouldGenerateCorrectionQueries() bool {
	if c.GenerateCorrectionQueries == nil {
		return false // default: don't generate correction queries
	}
	return *c.GenerateCorrectionQueries
}

// GetMode returns the comparison mode (default: sampling)
func (c *Comparison) GetMode() CompareMode {
	if c.Mode == "" || c.Mode == "sampling" {
		return ModeSampling
	}
	if c.Mode == "full" {
		return ModeFull
	}
	return ModeSampling
}

// IsFullMode returns true if full comparison mode is enabled
func (c *Comparison) IsFullMode() bool {
	return c.GetMode() == ModeFull
}

// GetFullModeChunkSize returns chunk size for full mode (default: 2000)
func (c *Comparison) GetFullModeChunkSize() int {
	if c.FullModeChunkSize <= 0 {
		return 2000
	}
	return c.FullModeChunkSize
}

// GetFullModeDelayMs returns delay between chunks in ms (default: 10)
func (c *Comparison) GetFullModeDelayMs() int {
	if c.FullModeDelayMs < 0 {
		return 10
	}
	return c.FullModeDelayMs
}

// GetFullModeParallelism returns partition parallelism for full mode (default: 4)
func (c *Comparison) GetFullModeParallelism() int {
	if c.FullModeParallelism <= 0 {
		return 4
	}
	return c.FullModeParallelism
}

// LargeColumnTypes returns the list of column types considered "large"
func LargeColumnTypes() []string {
	return []string{
		"text", "mediumtext", "longtext",
		"blob", "mediumblob", "longblob",
		"json",
	}
}

// IsLargeColumnType checks if the given column type is considered large
// colType should be the MySQL column type (e.g., "text", "mediumtext", "json")
func IsLargeColumnType(colType string) bool {
	colTypeLower := strings.ToLower(colType)
	for _, largeType := range LargeColumnTypes() {
		if strings.HasPrefix(colTypeLower, largeType) || strings.Contains(colTypeLower, largeType) {
			return true
		}
	}
	return false
}

// TableFilter holds table include/exclude filters
// Patterns use database.table format with wildcards (e.g., "mydb.*", "*.users")
type TableFilter struct {
	Include []string `yaml:"include" mapstructure:"include"` // database.table patterns to include
	Exclude []string `yaml:"exclude" mapstructure:"exclude"` // database.table patterns to exclude
}

// Output holds output configuration
type Output struct {
	Format   string `yaml:"format" mapstructure:"format"` // jsonl, json, text
	Path     string `yaml:"path" mapstructure:"path"`
	LogLevel string `yaml:"log_level" mapstructure:"log_level"` // debug, info, warn, error (default: info)
}

// DefaultConfig returns a config with default values
func DefaultConfig() *Config {
	return &Config{
		Source: types.DBConfig{
			Port: 3306,
		},
		Target: types.DBConfig{
			Port: 3306,
		},
		Comparison: Comparison{
			Mode:                "sampling",
			SampleSize:          1000,
			Parallelism:         5,
			FullModeChunkSize:   2000,
			FullModeDelayMs:     10,
			FullModeParallelism: 4,
		},
		Tables: TableFilter{
			Include: []string{},
			Exclude: []string{},
		},
		Output: Output{
			Format: "jsonl",
			Path:   "./results",
		},
	}
}

// LoadConfig is an alias for Load
func LoadConfig(configPath string) (*Config, error) {
	return Load(configPath)
}

// Load loads configuration from file and environment
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.db-compare")
	}

	// Environment variable support
	viper.SetEnvPrefix("DBCOMPARE")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
		// Config file not found is OK if using CLI flags
	}

	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand environment variables in passwords
	cfg.Source.Password = expandEnv(cfg.Source.Password)
	cfg.Target.Password = expandEnv(cfg.Target.Password)

	return cfg, nil
}

// expandEnv expands ${VAR} or $VAR in string
func expandEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		envVar := s[2 : len(s)-1]
		return os.Getenv(envVar)
	}
	if strings.HasPrefix(s, "$") {
		return os.Getenv(s[1:])
	}
	return s
}

// Validate validates the configuration
func (c *Config) ValidateForMode(runMode string) error {
	if c.Source.Host == "" {
		return fmt.Errorf("source host is required")
	}
	if c.Source.User == "" {
		return fmt.Errorf("source user is required")
	}
	if c.Target.Host == "" {
		return fmt.Errorf("target host is required")
	}
	if c.Target.User == "" {
		return fmt.Errorf("target user is required")
	}

	if runMode != "retry" {
		if c.Comparison.SampleSize < 1 {
			return fmt.Errorf("sample_size must be at least 1")
		}
		if c.Comparison.Parallelism < 1 {
			return fmt.Errorf("parallelism must be at least 1")
		}
	}
	if err := c.validateMappings(); err != nil {
		return err
	}
	// Note: database field is only for connection.
	// If include patterns are empty, all user databases will be scanned.
	return nil
}

func (c *Config) validateMappings() error {
	if len(c.Mappings) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	for _, m := range c.Mappings {
		if _, _, ok := SplitTableName(m.Source); !ok {
			return fmt.Errorf("mapping source %q must be \"database.table\" without wildcards", m.Source)
		}
		if _, _, ok := SplitTableName(m.Target); !ok {
			return fmt.Errorf("mapping target %q must be \"database.table\" without wildcards", m.Target)
		}
		key := strings.ToLower(m.Source)
		if seen[key] {
			return fmt.Errorf("mapping source %q is listed more than once", m.Source)
		}
		seen[key] = true
		for src, target := range m.Columns {
			if src == "" || target == "" {
				return fmt.Errorf("mapping %q has an empty column name", m.Source)
			}
		}
		excluded := m.ExcludeSet()
		for _, col := range m.Exclude {
			if col == "" {
				return fmt.Errorf("mapping %q has an empty column name in exclude", m.Source)
			}
		}
		for src := range m.Columns {
			if excluded[strings.ToLower(src)] {
				return fmt.Errorf("mapping %q: column %q is in both columns and exclude", m.Source, src)
			}
		}
	}
	return nil
}

// Validate validates the configuration for sample/full modes.
func (c *Config) Validate() error {
	return c.ValidateForMode("sample")
}

// GetSchemas extracts schema names from include patterns
// Note: database field is only for connection, not for table discovery
// Returns nil if no include patterns or wildcard database patterns (to query all schemas)
// Returns specific schemas if include patterns have exact database names
func (c *Config) GetSchemas() []string {
	if len(c.Tables.Include) == 0 {
		// No include patterns: scan all user databases
		return nil
	}

	schemas := make(map[string]struct{})
	for _, pattern := range c.Tables.Include {
		dotIdx := strings.Index(pattern, ".")
		if dotIdx == -1 {
			continue // Invalid pattern without dot
		}
		dbPattern := pattern[:dotIdx]

		// If db pattern contains wildcard, caller should query all schemas
		if strings.Contains(dbPattern, "*") {
			return nil
		}
		schemas[dbPattern] = struct{}{}
	}
	for _, m := range c.Mappings {
		if schema, _, ok := SplitTableName(m.Source); ok {
			schemas[schema] = struct{}{}
		}
	}

	result := make([]string, 0, len(schemas))
	for schema := range schemas {
		result = append(result, schema)
	}
	return result
}
