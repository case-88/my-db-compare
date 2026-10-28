package sampler

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// DefaultPartitionParallelism is the default number of partitions to sample in parallel
// Limited to avoid connection pool exhaustion (MaxOpenConns=10, with table parallelism=2)
const DefaultPartitionParallelism = 5

// Sampler extracts random samples from database tables
type Sampler struct {
	conn   *db.Connection
	logger *zap.Logger
}

// NewSampler creates a new Sampler
func NewSampler(conn *db.Connection, logger *zap.Logger) *Sampler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Sampler{
		conn:   conn,
		logger: logger,
	}
}

// SampleResult holds sampled rows from a partition
type SampleResult struct {
	Partition Partition
	Rows      []types.Row
	Error     error
}

// TableSampleResult holds all samples for a table
type TableSampleResult struct {
	Table     types.TableMeta
	KeyColumn string
	Samples   []types.Row
	Total     int
}

// SamplePartition extracts random samples from a single partition
// Uses ORDER BY RAND() with LIMIT for random sampling
func (s *Sampler) SamplePartition(ctx context.Context, table types.TableMeta, partition Partition, columns []string) (*SampleResult, error) {
	if partition.Samples <= 0 {
		return &SampleResult{Partition: partition, Rows: nil}, nil
	}

	keyColumn := table.KeyColumns()[0] // Use first key column for partitioning

	// Build column list
	colList := buildColumnList(columns)

	// Build query with partition range
	// Format: SELECT cols FROM table WHERE key >= min AND key < max ORDER BY key_column LIMIT n
	// Note: ORDER BY key_column instead of RAND() for performance optimization
	// - RAND() causes full table scan + random value calculation for each row
	// - Partitions are already distributed across PK ranges, so overall sampling is distributed
	query := fmt.Sprintf(
		"SELECT %s FROM `%s`.`%s` WHERE `%s` >= ? AND `%s` <= ? ORDER BY `%s` LIMIT ?",
		colList,
		table.Schema,
		table.Name,
		keyColumn,
		keyColumn,
		keyColumn,
	)

	s.logger.Debug("sampling partition",
		zap.String("table", table.FullName()),
		zap.Int("partition", partition.Index),
		zap.Any("min", partition.MinValue),
		zap.Any("max", partition.MaxValue),
		zap.Int("samples", partition.Samples),
	)

	rows, err := s.conn.QueryContext(ctx, query, partition.MinValue, partition.MaxValue, partition.Samples)
	if err != nil {
		return &SampleResult{Partition: partition, Error: err}, err
	}
	defer rows.Close()

	// Get column names from result
	colNames, err := rows.Columns()
	if err != nil {
		return &SampleResult{Partition: partition, Error: err}, err
	}

	var samples []types.Row
	for rows.Next() {
		// Create slice of interface{} to scan into
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return &SampleResult{Partition: partition, Error: err}, err
		}

		// Build Row map
		row := make(types.Row, len(colNames))
		for i, col := range colNames {
			row[col] = values[i]
		}

		samples = append(samples, row)
	}

	if err := rows.Err(); err != nil {
		return &SampleResult{Partition: partition, Error: err}, err
	}

	return &SampleResult{
		Partition: partition,
		Rows:      samples,
	}, nil
}

// SampleTable extracts random samples from all partitions of a table
func (s *Sampler) SampleTable(ctx context.Context, plan *PartitionPlan) (*TableSampleResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("partition plan is nil")
	}

	// Get column names
	columns := make([]string, len(plan.Table.Columns))
	for i, col := range plan.Table.Columns {
		columns[i] = col.Name
	}

	return s.SampleTableWithColumns(ctx, plan, columns)
}

// SampleTableWithColumns extracts random samples with specified columns
// Useful for excluding large columns (TEXT, BLOB, JSON) from comparison
// Note: Uses sequential partition processing to avoid connection pool exhaustion
// when combined with table-level parallelism (cfg.Comparison.Parallelism)
func (s *Sampler) SampleTableWithColumns(ctx context.Context, plan *PartitionPlan, columns []string) (*TableSampleResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("partition plan is nil")
	}

	// Sequential processing to avoid connection pool exhaustion
	// Table-level parallelism is already handled by runner.go (cfg.Comparison.Parallelism)
	// Adding partition-level parallelism would exceed MaxOpenConns
	var allSamples []types.Row

	for _, partition := range plan.Partitions {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		result, err := s.SamplePartition(ctx, plan.Table, partition, columns)
		if err != nil {
			s.logger.Warn("partition sampling failed",
				zap.String("table", plan.Table.FullName()),
				zap.Int("partition", partition.Index),
				zap.Error(err),
			)
			continue // Continue with other partitions
		}

		allSamples = append(allSamples, result.Rows...)
	}

	s.logger.Info("sampling completed",
		zap.String("table", plan.Table.FullName()),
		zap.Int("partitions", len(plan.Partitions)),
		zap.Int("samples", len(allSamples)),
	)

	return &TableSampleResult{
		Table:     plan.Table,
		KeyColumn: plan.KeyColumn,
		Samples:   allSamples,
		Total:     len(allSamples),
	}, nil
}

// SampleTableWithColumnsParallel extracts samples with specified parallelism
// parallelism controls the number of concurrent partition queries
func (s *Sampler) SampleTableWithColumnsParallel(ctx context.Context, plan *PartitionPlan, columns []string, parallelism int) (*TableSampleResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("partition plan is nil")
	}

	if parallelism <= 0 {
		parallelism = DefaultPartitionParallelism
	}

	// Limit parallelism to actual partition count
	if parallelism > len(plan.Partitions) {
		parallelism = len(plan.Partitions)
	}

	// Create semaphore for limiting concurrent queries
	sem := make(chan struct{}, parallelism)

	// Results channel
	resultsChan := make(chan *SampleResult, len(plan.Partitions))

	// WaitGroup for goroutines
	var wg sync.WaitGroup

	s.logger.Debug("starting parallel partition sampling",
		zap.String("table", plan.Table.FullName()),
		zap.Int("partitions", len(plan.Partitions)),
		zap.Int("parallelism", parallelism),
	)

	// Launch goroutines for each partition
	for _, partition := range plan.Partitions {
		wg.Add(1)
		go func(p Partition) {
			defer wg.Done()

			// Check context before acquiring semaphore
			select {
			case <-ctx.Done():
				resultsChan <- &SampleResult{Partition: p, Error: ctx.Err()}
				return
			case sem <- struct{}{}: // Acquire semaphore
				defer func() { <-sem }() // Release semaphore
			}

			// Check context again after acquiring semaphore
			select {
			case <-ctx.Done():
				resultsChan <- &SampleResult{Partition: p, Error: ctx.Err()}
				return
			default:
			}

			result, err := s.SamplePartition(ctx, plan.Table, p, columns)
			if err != nil {
				s.logger.Warn("partition sampling failed",
					zap.String("table", plan.Table.FullName()),
					zap.Int("partition", p.Index),
					zap.Error(err),
				)
				resultsChan <- &SampleResult{Partition: p, Error: err}
				return
			}
			resultsChan <- result
		}(partition)
	}

	// Wait for all goroutines and close results channel
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	var allSamples []types.Row
	var firstErr error
	var ctxErr error // Track context errors separately
	successCount := 0

	for result := range resultsChan {
		if result.Error != nil {
			// Track context errors separately (they should be propagated)
			if result.Error == context.Canceled || result.Error == context.DeadlineExceeded {
				if ctxErr == nil {
					ctxErr = result.Error
				}
			} else if firstErr == nil {
				firstErr = result.Error
			}
			continue
		}
		allSamples = append(allSamples, result.Rows...)
		successCount++
	}

	s.logger.Info("sampling completed",
		zap.String("table", plan.Table.FullName()),
		zap.Int("partitions", len(plan.Partitions)),
		zap.Int("successful_partitions", successCount),
		zap.Int("samples", len(allSamples)),
	)

	// If all partitions failed, return the error (prefer context error)
	if successCount == 0 {
		if ctxErr != nil {
			return nil, ctxErr
		}
		if firstErr != nil {
			return nil, fmt.Errorf("all partitions failed: %w", firstErr)
		}
	}

	return &TableSampleResult{
		Table:     plan.Table,
		KeyColumn: plan.KeyColumn,
		Samples:   allSamples,
		Total:     len(allSamples),
	}, nil
}

// SampleTableDirect extracts random samples without partition plan
// Uses a single query with ORDER BY RAND() LIMIT
func (s *Sampler) SampleTableDirect(ctx context.Context, table types.TableMeta, sampleSize int) (*TableSampleResult, error) {
	columns := make([]string, len(table.Columns))
	for i, col := range table.Columns {
		columns[i] = col.Name
	}
	return s.SampleTableDirectWithColumns(ctx, table, sampleSize, columns)
}

// SampleTableDirectWithColumns extracts random samples with specified columns
// Useful for excluding large columns (TEXT, BLOB, JSON) from comparison
func (s *Sampler) SampleTableDirectWithColumns(ctx context.Context, table types.TableMeta, sampleSize int, columns []string) (*TableSampleResult, error) {
	colList := buildColumnList(columns)

	// Use primary key for ordering if available, otherwise fall back to RAND()
	// PK ordering is much faster than RAND() which requires full table scan
	var query string
	keyColumns := table.KeyColumns()
	if len(keyColumns) > 0 {
		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` ORDER BY `%s` LIMIT ?",
			colList,
			table.Schema,
			table.Name,
			keyColumns[0],
		)
	} else {
		// Fallback to RAND() only when no key column is available
		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` ORDER BY RAND() LIMIT ?",
			colList,
			table.Schema,
			table.Name,
		)
	}

	s.logger.Debug("direct sampling",
		zap.String("table", table.FullName()),
		zap.Int("samples", sampleSize),
	)

	rows, err := s.conn.QueryContext(ctx, query, sampleSize)
	if err != nil {
		return nil, fmt.Errorf("sample query failed: %w", err)
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var samples []types.Row
	for rows.Next() {
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(types.Row, len(colNames))
		for i, col := range colNames {
			row[col] = values[i]
		}

		samples = append(samples, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return &TableSampleResult{
		Table:   table,
		Samples: samples,
		Total:   len(samples),
	}, nil
}

// SampleByKey extracts a specific row by its key values
// Used for re-verification of mismatched rows
func (s *Sampler) SampleByKey(ctx context.Context, table types.TableMeta, keyValues map[string]interface{}) (types.Row, error) {
	columns := make([]string, len(table.Columns))
	for i, col := range table.Columns {
		columns[i] = col.Name
	}

	colList := buildColumnList(columns)

	// Build WHERE clause for key columns
	keyColumns := table.KeyColumns()
	if len(keyColumns) == 0 {
		return nil, fmt.Errorf("table has no key columns")
	}

	whereParts := make([]string, len(keyColumns))
	args := make([]interface{}, len(keyColumns))
	for i, col := range keyColumns {
		whereParts[i] = fmt.Sprintf("`%s` = ?", col)
		val, ok := keyValues[col]
		if !ok {
			return nil, fmt.Errorf("missing key value for column: %s", col)
		}
		args[i] = val
	}

	query := fmt.Sprintf(
		"SELECT %s FROM `%s`.`%s` WHERE %s LIMIT 1",
		colList,
		table.Schema,
		table.Name,
		strings.Join(whereParts, " AND "),
	)

	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("key lookup query failed: %w", err)
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	if !rows.Next() {
		return nil, nil // Row not found
	}

	values := make([]interface{}, len(colNames))
	valuePtrs := make([]interface{}, len(colNames))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	if err := rows.Scan(valuePtrs...); err != nil {
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	row := make(types.Row, len(colNames))
	for i, col := range colNames {
		row[col] = values[i]
	}

	return row, nil
}

// SampleByKeys extracts multiple rows by their key values in a single batch query
// This is much more efficient than calling SampleByKey multiple times
// Uses IN clause for single-column PKs, tuple comparison for composite PKs
func (s *Sampler) SampleByKeys(ctx context.Context, table types.TableMeta, keyValuesList []map[string]interface{}, columns []string) ([]types.Row, error) {
	if len(keyValuesList) == 0 {
		return nil, nil
	}

	colList := buildColumnList(columns)

	keyColumns := table.KeyColumns()
	if len(keyColumns) == 0 {
		return nil, fmt.Errorf("table has no key columns")
	}

	// Build WHERE clause based on number of key columns
	var query string
	var args []interface{}

	if len(keyColumns) == 1 {
		// Single column PK: WHERE pk IN (?, ?, ?)
		keyCol := keyColumns[0]
		placeholders := make([]string, len(keyValuesList))
		args = make([]interface{}, len(keyValuesList))

		for i, kv := range keyValuesList {
			placeholders[i] = "?"
			args[i] = kv[keyCol]
		}

		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` WHERE `%s` IN (%s)",
			colList,
			table.Schema,
			table.Name,
			keyCol,
			strings.Join(placeholders, ", "),
		)
	} else {
		// Composite PK: WHERE (pk1, pk2) IN ((?, ?), (?, ?))
		colParts := make([]string, len(keyColumns))
		for i, col := range keyColumns {
			colParts[i] = fmt.Sprintf("`%s`", col)
		}

		tuplePlaceholders := make([]string, len(keyValuesList))
		args = make([]interface{}, 0, len(keyValuesList)*len(keyColumns))

		singleTuplePlaceholder := "(" + strings.Repeat("?, ", len(keyColumns)-1) + "?)"
		for i, kv := range keyValuesList {
			tuplePlaceholders[i] = singleTuplePlaceholder
			for _, col := range keyColumns {
				args = append(args, kv[col])
			}
		}

		query = fmt.Sprintf(
			"SELECT %s FROM `%s`.`%s` WHERE (%s) IN (%s)",
			colList,
			table.Schema,
			table.Name,
			strings.Join(colParts, ", "),
			strings.Join(tuplePlaceholders, ", "),
		)
	}

	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch key lookup query failed: %w", err)
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	var results []types.Row
	for rows.Next() {
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(types.Row, len(colNames))
		for i, col := range colNames {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// buildColumnList builds a comma-separated list of quoted column names
func buildColumnList(columns []string) string {
	if len(columns) == 0 {
		return "*"
	}

	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = fmt.Sprintf("`%s`", col)
	}
	return strings.Join(quoted, ", ")
}

// ExtractKeyValues extracts key column values from a row
func ExtractKeyValues(row types.Row, keyColumns []string) map[string]interface{} {
	result := make(map[string]interface{}, len(keyColumns))
	for _, col := range keyColumns {
		if val, ok := row[col]; ok {
			result[col] = val
		}
	}
	return result
}
