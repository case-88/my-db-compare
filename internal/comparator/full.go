package comparator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// FullCompareConfig holds configuration for full comparison
type FullCompareConfig struct {
	ChunkSize   int // Number of rows per chunk
	DelayMs     int // Delay between chunks in milliseconds
	Parallelism int // Number of parallel partition workers
	Options     ComparatorOptions
}

// DefaultFullCompareConfig returns default configuration
func DefaultFullCompareConfig() FullCompareConfig {
	return FullCompareConfig{
		ChunkSize:   2000,
		DelayMs:     10,
		Parallelism: 4,
		Options:     DefaultComparatorOptions(),
	}
}

// FullCompareProgress holds progress information for full comparison
type FullCompareProgress struct {
	Table          string
	TotalRows      int64
	ProcessedRows  int64
	MismatchCount  int64
	StartTime      time.Time
	LastReportTime time.Time
}

// GetProgress returns current progress as a formatted string
func (p *FullCompareProgress) GetProgress() (processed, total int64, mismatchCount int64, elapsed, remaining time.Duration) {
	processed = atomic.LoadInt64(&p.ProcessedRows)
	total = p.TotalRows
	mismatchCount = atomic.LoadInt64(&p.MismatchCount)
	elapsed = time.Since(p.StartTime)

	if processed > 0 && total > 0 {
		rate := float64(processed) / elapsed.Seconds()
		if rate > 0 {
			remainingRows := total - processed
			remaining = time.Duration(float64(remainingRows) / rate * float64(time.Second))
		}
	}
	return
}

// FullCompareResult holds the result of full comparison for a table
type FullCompareResult struct {
	Table           string
	TotalRows       int64
	ProcessedRows   int64
	MatchCount      int64
	MismatchCount   int64
	MissingInTarget int64
	MissingInSource int64
	Different       int64
	Duration        time.Duration
	Error           error
}

// FullComparer performs full table comparison
type FullComparer struct {
	srcConn    *db.Connection
	targetConn *db.Connection
	config     FullCompareConfig
	logger     *zap.Logger
}

// NewFullComparer creates a new FullComparer
func NewFullComparer(srcConn, targetConn *db.Connection, config FullCompareConfig, logger *zap.Logger) *FullComparer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FullComparer{
		srcConn:    srcConn,
		targetConn: targetConn,
		config:     config,
		logger:     logger,
	}
}

// PartitionRange represents a range of data to compare
type PartitionRange struct {
	Index    int
	MinValue interface{}
	MaxValue interface{}
}

// ChunkResult holds the result of comparing a single chunk
type ChunkResult struct {
	ProcessedRows int
	Mismatches    []types.Mismatch
	Error         error
}

// MismatchHandler is called for each chunk's mismatches
// This allows immediate logging and memory release
type MismatchHandler func(mismatches []types.Mismatch) error

// CompareTable performs full comparison of a table
// target is the table to read on the target side (differs only for mappings)
// Progress is reported via progressCh (can be nil)
// Mismatches are passed to handler for immediate processing
func (fc *FullComparer) CompareTable(
	ctx context.Context,
	table types.TableMeta,
	target types.TableMeta,
	columns []string,
	handler MismatchHandler,
	progressCh chan<- *FullCompareProgress,
) (*FullCompareResult, error) {
	start := time.Now()
	result := &FullCompareResult{
		Table:     table.FullName(),
		TotalRows: table.EstimatedRows,
	}

	keyColumns := table.KeyColumns()
	if len(keyColumns) == 0 {
		result.Error = fmt.Errorf("table has no key columns")
		return result, result.Error
	}

	keyColumn := keyColumns[0]

	// Get min/max values
	srcMeta := db.NewMetaQuerier(fc.srcConn, fc.logger)
	minVal, maxVal, err := srcMeta.GetMinMax(ctx, table.Schema, table.Name, keyColumns)
	if err != nil {
		result.Error = fmt.Errorf("failed to get min/max: %w", err)
		return result, result.Error
	}

	if minVal == nil || maxVal == nil {
		// Empty table
		result.Duration = time.Since(start)
		return result, nil
	}

	// Create partition ranges
	partitions := fc.createPartitions(minVal, maxVal, fc.config.Parallelism)

	// Progress tracking
	progress := &FullCompareProgress{
		Table:          table.FullName(),
		TotalRows:      table.EstimatedRows,
		StartTime:      start,
		LastReportTime: start,
	}

	// Start progress reporter (every 1 minute)
	progressDone := make(chan struct{})
	if progressCh != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					progressCh <- progress
				case <-progressDone:
					return
				}
			}
		}()
	}

	// Process partitions in parallel
	var wg sync.WaitGroup
	resultCh := make(chan *partitionResult, fc.config.Parallelism)

	for _, partition := range partitions {
		wg.Add(1)
		go func(p PartitionRange) {
			defer wg.Done()
			pr := fc.comparePartition(ctx, table, target, keyColumn, columns, p, handler, progress)
			resultCh <- pr
		}(partition)
	}

	// Wait for all partitions and close result channel
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results
	for pr := range resultCh {
		if pr.Error != nil && result.Error == nil {
			result.Error = pr.Error
		}
		result.ProcessedRows += pr.ProcessedRows
		result.MatchCount += pr.MatchCount
		result.MismatchCount += pr.MismatchCount
		result.MissingInTarget += pr.MissingInTarget
		result.MissingInSource += pr.MissingInSource
		result.Different += pr.Different
	}

	// Stop progress reporter
	close(progressDone)

	result.Duration = time.Since(start)
	return result, nil
}

// partitionResult holds results from processing a partition
type partitionResult struct {
	ProcessedRows   int64
	MatchCount      int64
	MismatchCount   int64
	MissingInTarget int64
	MissingInSource int64
	Different       int64
	Error           error
}

// comparePartition compares all chunks in a partition
func (fc *FullComparer) comparePartition(
	ctx context.Context,
	table types.TableMeta,
	target types.TableMeta,
	keyColumn string,
	columns []string,
	partition PartitionRange,
	handler MismatchHandler,
	progress *FullCompareProgress,
) *partitionResult {
	result := &partitionResult{}

	// Column list for query
	colList := buildColumnListForQuery(columns)

	// Get comparator
	comp := NewComparatorWithOptions(table.KeyColumns(), fc.config.Options)

	// Process chunks
	currentMin := partition.MinValue
	delay := time.Duration(fc.config.DelayMs) * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		default:
		}

		// Fetch chunk from source
		srcRows, nextMin, err := fc.fetchChunk(ctx, fc.srcConn, table, keyColumn, colList, currentMin, partition.MaxValue, fc.config.ChunkSize)
		if err != nil {
			result.Error = fmt.Errorf("failed to fetch source chunk: %w", err)
			return result
		}

		if len(srcRows) == 0 {
			break // No more data in this partition
		}

		// Fetch same keys from target
		targetRows, err := fc.fetchByKeys(ctx, fc.targetConn, target, columns, srcRows, table.KeyColumns())
		if err != nil {
			result.Error = fmt.Errorf("failed to fetch target rows: %w", err)
			return result
		}

		// Compare
		compareResult := comp.CompareSamples(srcRows, targetRows)

		// Count results
		result.ProcessedRows += int64(len(srcRows))
		result.MatchCount += int64(compareResult.Matched)

		// Update progress
		atomic.AddInt64(&progress.ProcessedRows, int64(len(srcRows)))

		// Handle mismatches
		if len(compareResult.MissingInTarget) > 0 || len(compareResult.MissingInSrc) > 0 || len(compareResult.Different) > 0 {
			mismatches := comp.ToMismatches(compareResult, table, target)

			// Count by type
			for _, m := range mismatches {
				switch m.Type {
				case types.MismatchMissingTarget:
					result.MissingInTarget++
				case types.MismatchMissingSource:
					result.MissingInSource++
				case types.MismatchDataDiff:
					result.Different++
				}
			}

			result.MismatchCount += int64(len(mismatches))
			atomic.AddInt64(&progress.MismatchCount, int64(len(mismatches)))

			// Call handler for immediate processing (logging)
			if handler != nil && len(mismatches) > 0 {
				if err := handler(mismatches); err != nil {
					fc.logger.Warn("mismatch handler error", zap.Error(err))
				}
			}
		}

		// Clear for next iteration (help GC)
		srcRows = nil
		targetRows = nil

		// Check if we've reached the end
		if nextMin == nil {
			break
		}

		currentMin = nextMin

		// Delay between chunks for load management
		if delay > 0 {
			time.Sleep(delay)
		}
	}

	return result
}

// createPartitions divides the key range into partitions
func (fc *FullComparer) createPartitions(minVal, maxVal interface{}, count int) []PartitionRange {
	if count <= 0 {
		count = 1
	}

	// For now, simple approach: create count partitions
	// This is a simplified version - in reality, we'd need to handle
	// different PK types (int, string, time, etc.)
	partitions := make([]PartitionRange, count)

	switch min := minVal.(type) {
	case int64:
		max := maxVal.(int64)
		rangeSize := (max - min) / int64(count)
		for i := 0; i < count; i++ {
			partitions[i] = PartitionRange{
				Index:    i,
				MinValue: min + int64(i)*rangeSize,
				MaxValue: min + int64(i+1)*rangeSize,
			}
		}
		// Last partition extends to max
		partitions[count-1].MaxValue = max
	case uint64:
		max := maxVal.(uint64)
		rangeSize := (max - min) / uint64(count)
		for i := 0; i < count; i++ {
			partitions[i] = PartitionRange{
				Index:    i,
				MinValue: min + uint64(i)*rangeSize,
				MaxValue: min + uint64(i+1)*rangeSize,
			}
		}
		partitions[count-1].MaxValue = max
	default:
		// For non-numeric types, use single partition
		partitions = []PartitionRange{{
			Index:    0,
			MinValue: minVal,
			MaxValue: maxVal,
		}}
	}

	return partitions
}

// fetchChunk fetches a chunk of rows from the database
// Returns rows, next min value (for pagination), and error
func (fc *FullComparer) fetchChunk(
	ctx context.Context,
	conn *db.Connection,
	table types.TableMeta,
	keyColumn string,
	colList string,
	minVal, maxVal interface{},
	limit int,
) ([]types.Row, interface{}, error) {
	query := fmt.Sprintf(
		"SELECT %s FROM `%s`.`%s` WHERE `%s` >= ? AND `%s` <= ? ORDER BY `%s` LIMIT ?",
		colList, table.Schema, table.Name, keyColumn, keyColumn, keyColumn,
	)

	rows, err := conn.QueryContext(ctx, query, minVal, maxVal, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	var result []types.Row
	var lastKeyVal interface{}

	for rows.Next() {
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, nil, err
		}

		row := make(types.Row, len(colNames))
		for i, col := range colNames {
			row[col] = values[i]
			if col == keyColumn {
				lastKeyVal = values[i]
			}
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// If we got exactly limit rows, there may be more
	var nextMin interface{}
	if len(result) == limit && lastKeyVal != nil {
		// Next query should start after the last key
		nextMin = incrementKey(lastKeyVal)
	}

	return result, nextMin, nil
}

// fetchByKeys fetches rows by their key values
func (fc *FullComparer) fetchByKeys(
	ctx context.Context,
	conn *db.Connection,
	table types.TableMeta,
	columns []string,
	srcRows []types.Row,
	keyColumns []string,
) ([]types.Row, error) {
	if len(srcRows) == 0 {
		return nil, nil
	}

	colList := table.SelectList(columns)

	// Build key values list
	keyValuesList := make([]map[string]interface{}, len(srcRows))
	for i, row := range srcRows {
		kv := make(map[string]interface{})
		for _, col := range keyColumns {
			kv[col] = row[col]
		}
		keyValuesList[i] = kv
	}

	// Use batch query (same as sampler.SampleByKeys)
	var query string
	var args []interface{}

	if len(keyColumns) == 1 {
		keyCol := keyColumns[0]
		placeholders := make([]string, len(keyValuesList))
		args = make([]interface{}, len(keyValuesList))
		for i, kv := range keyValuesList {
			placeholders[i] = "?"
			args[i] = kv[keyCol]
		}
		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` WHERE `%s` IN (%s)",
			colList, table.Schema, table.Name, table.PhysicalColumn(keyCol),
			joinStrings(placeholders, ", "),
		)
	} else {
		// Composite PK
		colParts := make([]string, len(keyColumns))
		for i, col := range keyColumns {
			colParts[i] = fmt.Sprintf("`%s`", table.PhysicalColumn(col))
		}

		tuplePlaceholders := make([]string, len(keyValuesList))
		args = make([]interface{}, 0, len(keyValuesList)*len(keyColumns))
		singleTuple := "(" + repeatString("?", len(keyColumns), ", ") + ")"

		for i, kv := range keyValuesList {
			tuplePlaceholders[i] = singleTuple
			for _, col := range keyColumns {
				args = append(args, kv[col])
			}
		}

		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` WHERE (%s) IN (%s)",
			colList, table.Schema, table.Name,
			joinStrings(colParts, ", "),
			joinStrings(tuplePlaceholders, ", "),
		)
	}

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []types.Row
	for rows.Next() {
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(types.Row, len(colNames))
		for i, col := range colNames {
			row[col] = values[i]
		}
		result = append(result, row)
	}

	return result, rows.Err()
}

// incrementKey returns the next value for pagination
func incrementKey(v interface{}) interface{} {
	switch val := v.(type) {
	case int64:
		return val + 1
	case uint64:
		return val + 1
	case int:
		return val + 1
	case int32:
		return val + 1
	case uint32:
		return val + 1
	default:
		// For non-numeric, return as-is (will need different pagination strategy)
		return v
	}
}

// buildColumnListForQuery builds a comma-separated list of quoted column names
func buildColumnListForQuery(columns []string) string {
	if len(columns) == 0 {
		return "*"
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = fmt.Sprintf("`%s`", col)
	}
	return joinStrings(quoted, ", ")
}

// joinStrings joins strings with separator
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

// repeatString repeats a string n times with separator
func repeatString(s string, n int, sep string) string {
	if n <= 0 {
		return ""
	}
	result := s
	for i := 1; i < n; i++ {
		result += sep + s
	}
	return result
}
