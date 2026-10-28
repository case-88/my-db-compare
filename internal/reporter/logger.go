package reporter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// Format represents output format
type Format string

const (
	FormatJSONL Format = "jsonl"
	FormatJSON  Format = "json"
	FormatText  Format = "text"
)

// Logger writes comparison results to output files
type Logger struct {
	outputPath string
	format     Format
	logger     *zap.Logger

	mu        sync.Mutex
	file      *os.File
	writer    *bufio.Writer
	records   []types.Mismatch // For JSON format (buffered)
	count     int
	startTime time.Time
}

// NewLogger creates a new Logger
func NewLogger(outputPath string, format Format, logger *zap.Logger) (*Logger, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Validate format
	switch format {
	case FormatJSONL, FormatJSON, FormatText:
	case "":
		format = FormatJSONL
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	return &Logger{
		outputPath: outputPath,
		format:     format,
		logger:     logger,
		startTime:  time.Now(),
	}, nil
}

// Open opens the output file for writing
func (l *Logger) Open(tableName string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		return fmt.Errorf("logger already open")
	}

	// Create output directory if needed
	if err := os.MkdirAll(l.outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate filename (no timestamp - parent directory has timestamp)
	ext := ".jsonl"
	switch l.format {
	case FormatJSON:
		ext = ".json"
	case FormatText:
		ext = ".txt"
	}

	filename := filepath.Join(l.outputPath, fmt.Sprintf("mismatch-%s%s", tableName, ext))

	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	l.file = file
	l.writer = bufio.NewWriter(file)
	l.records = nil
	l.count = 0
	l.startTime = time.Now()

	l.logger.Info("output opened",
		zap.String("path", filename),
		zap.String("format", string(l.format)),
	)

	return nil
}

// OpenGlobal opens a single output file for all tables
func (l *Logger) OpenGlobal() error {
	return l.Open("all")
}

// Write writes a single mismatch record
func (l *Logger) Write(mismatch types.Mismatch) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return fmt.Errorf("logger not open")
	}

	switch l.format {
	case FormatJSONL:
		return l.writeJSONL(mismatch)
	case FormatJSON:
		// Filter source/target data to only include diff columns
		filtered := filterMismatchData(mismatch)
		l.records = append(l.records, filtered)
		l.count++
		return nil
	case FormatText:
		return l.writeText(mismatch)
	}

	return nil
}

// WriteBatch writes multiple mismatch records
// Continues on error to ensure all valid records are written
func (l *Logger) WriteBatch(mismatches []types.Mismatch) error {
	var lastErr error
	for _, m := range mismatches {
		if err := l.Write(m); err != nil {
			// Log error but continue processing other records
			l.logger.Warn("failed to write mismatch record, skipping",
				zap.Any("pk", m.PK),
				zap.Error(err),
			)
			lastErr = err
			continue
		}
	}
	return lastErr
}

// writeJSONL writes a single record as JSON Lines
func (l *Logger) writeJSONL(mismatch types.Mismatch) error {
	// Filter source/target data to only include diff columns
	filtered := filterMismatchData(mismatch)

	data, err := json.Marshal(filtered)
	if err != nil {
		return fmt.Errorf("failed to marshal mismatch: %w", err)
	}

	if _, err := l.writer.Write(data); err != nil {
		return err
	}
	if _, err := l.writer.WriteString("\n"); err != nil {
		return err
	}

	l.count++
	return nil
}

// filterMismatchData returns a copy of mismatch with source/target data filtered to diff columns only
// Also sanitizes time.Time values that can't be marshaled to JSON
func filterMismatchData(m types.Mismatch) types.Mismatch {
	// For MISSING_IN_TARGET or MISSING_IN_SOURCE, keep all data (no diff columns)
	if m.Type != types.MismatchDataDiff || len(m.DiffColumns) == 0 {
		filtered := m
		filtered.SourceData = sanitizeRow(m.SourceData)
		filtered.TargetData = sanitizeRow(m.TargetData)
		return filtered
	}

	// Create filtered copies
	filtered := m
	if m.SourceData != nil {
		filtered.SourceData = make(types.Row)
		for _, col := range m.DiffColumns {
			if val, ok := m.SourceData[col]; ok {
				filtered.SourceData[col] = sanitizeValue(val)
			}
		}
	}
	if m.TargetData != nil {
		filtered.TargetData = make(types.Row)
		for _, col := range m.DiffColumns {
			if val, ok := m.TargetData[col]; ok {
				filtered.TargetData[col] = sanitizeValue(val)
			}
		}
	}
	return filtered
}

// sanitizeRow sanitizes all values in a Row to ensure they can be marshaled to JSON
func sanitizeRow(row types.Row) types.Row {
	if row == nil {
		return nil
	}
	sanitized := make(types.Row, len(row))
	for k, v := range row {
		sanitized[k] = sanitizeValue(v)
	}
	return sanitized
}

// sanitizeValue converts values that can't be marshaled to JSON into safe representations
func sanitizeValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case time.Time:
		// time.Time with year outside 0-9999 can't be marshaled to JSON
		if val.Year() < 0 || val.Year() > 9999 {
			return val.Format("2006-01-02 15:04:05") + " (invalid year)"
		}
		return val
	default:
		return v
	}
}

// writeText writes a single record as human-readable multi-line text
func (l *Logger) writeText(mismatch types.Mismatch) error {
	var sb strings.Builder

	// Header with separator
	sb.WriteString("----------------------------------------\n")
	sb.WriteString(fmt.Sprintf("Table: %s\n", mismatch.Table))
	sb.WriteString(fmt.Sprintf("Type:  %s\n", mismatch.Type))
	sb.WriteString(fmt.Sprintf("Time:  %s\n", mismatch.Timestamp.Format(time.RFC3339)))

	// PK
	sb.WriteString("PK:    ")
	pkParts := make([]string, 0, len(mismatch.PK))
	for k, v := range mismatch.PK {
		pkParts = append(pkParts, fmt.Sprintf("%s=%v", k, formatValueForText(v)))
	}
	sb.WriteString(strings.Join(pkParts, ", "))
	sb.WriteString("\n")

	// Diff details based on type
	switch mismatch.Type {
	case types.MismatchDataDiff:
		sb.WriteString("Diff:\n")
		for _, col := range mismatch.DiffColumns {
			srcVal := formatValueForText(mismatch.SourceData[col])
			targetVal := formatValueForText(mismatch.TargetData[col])
			sb.WriteString(fmt.Sprintf("  %s:\n", col))
			sb.WriteString(fmt.Sprintf("    source: %s\n", srcVal))
			sb.WriteString(fmt.Sprintf("    target: %s\n", targetVal))
		}
	case types.MismatchMissingTarget:
		sb.WriteString("Status: row exists in source only\n")
	case types.MismatchMissingSource:
		sb.WriteString("Status: row exists in target only\n")
	}

	// Query
	sb.WriteString(fmt.Sprintf("Query: %s\n", mismatch.Query))
	if mismatch.TargetQuery != "" {
		sb.WriteString(fmt.Sprintf("Target Query: %s\n", mismatch.TargetQuery))
	}

	if _, err := l.writer.WriteString(sb.String()); err != nil {
		return err
	}

	l.count++
	return nil
}

// formatValueForText formats a value for text output
func formatValueForText(v interface{}) string {
	if v == nil {
		return "<NULL>"
	}
	switch val := v.(type) {
	case []byte:
		s := string(val)
		if len(s) > 100 {
			return fmt.Sprintf("%s... (%d bytes)", s[:100], len(s))
		}
		return s
	case string:
		if len(val) > 100 {
			return fmt.Sprintf("%s... (%d chars)", val[:100], len(val))
		}
		return val
	case time.Time:
		// Handle MySQL zero date: Go's zero time (0001-01-01) represents MySQL's 0000-00-00
		if val.IsZero() || (val.Year() == 1 && val.Month() == 1 && val.Day() == 1) {
			return "0000-00-00 00:00:00"
		}
		return val.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// Flush flushes buffered data to disk
func (l *Logger) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.writer == nil {
		return nil
	}

	return l.writer.Flush()
}

// Close closes the logger and finalizes the output file
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}

	// For JSON format, write the array now
	if l.format == FormatJSON {
		data, err := json.MarshalIndent(l.records, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal records: %w", err)
		}
		if _, err := l.writer.Write(data); err != nil {
			return err
		}
	}

	if err := l.writer.Flush(); err != nil {
		return err
	}

	l.logger.Info("output closed",
		zap.Int("records", l.count),
		zap.Duration("duration", time.Since(l.startTime)),
	)

	err := l.file.Close()
	l.file = nil
	l.writer = nil
	l.records = nil

	return err
}

// Count returns the number of records written
func (l *Logger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// OutputPath returns the output directory path
func (l *Logger) OutputPath() string {
	return l.outputPath
}

// Format returns the output format
func (l *Logger) Format() Format {
	return l.format
}

// WriteResult writes a CompareResult's mismatches
func (l *Logger) WriteResult(result *types.CompareResult) error {
	if result == nil {
		return nil
	}
	return l.WriteBatch(result.Mismatches)
}
