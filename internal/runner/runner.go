package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/case-88/my-db-compare/internal/comparator"
	"github.com/case-88/my-db-compare/internal/config"
	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/internal/reporter"
	"github.com/case-88/my-db-compare/internal/sampler"
	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// Runner orchestrates the database comparison process
type Runner struct {
	cfg       *config.Config
	outputDir string
	logger    *zap.Logger
	runMode   string
	inputDir  string

	srcConn    *db.Connection
	targetConn *db.Connection

	srcMeta    *db.MetaQuerier
	targetMeta *db.MetaQuerier

	srcSampler    *sampler.Sampler
	targetSampler *sampler.Sampler

	reporter *reporter.Logger
}

// Result holds the comparison result
type Result struct {
	Summary *reporter.Summary
	Error   error
}

// New creates a new Runner instance
func New(cfg *config.Config, outputDir, runMode, inputDir string, logger *zap.Logger) (*Runner, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Runner{
		cfg:       cfg,
		outputDir: outputDir,
		logger:    logger,
		runMode:   runMode,
		inputDir:  inputDir,
	}, nil
}

// DryRun shows what would be compared without actually comparing
func (r *Runner) DryRun(ctx context.Context) error {
	r.logger.Info("performing dry run")

	// Connect to databases
	if err := r.connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer r.disconnect()

	// Get tables to compare
	tables, err := r.getTablesToCompare(ctx)
	if err != nil {
		return fmt.Errorf("failed to get tables: %w", err)
	}

	fmt.Println("\n=== Dry Run Results ===")
	fmt.Printf("Source: %s:%d/%s\n", r.cfg.Source.Host, r.cfg.Source.Port, r.cfg.Source.Database)
	fmt.Printf("Target: %s:%d/%s\n", r.cfg.Target.Host, r.cfg.Target.Port, r.cfg.Target.Database)
	fmt.Printf("Mode: %s\n", r.runMode)
	if r.runMode == "full" {
		fmt.Printf("Chunk Size: %d\n", r.cfg.Comparison.GetFullModeChunkSize())
		fmt.Printf("Delay: %dms between chunks\n", r.cfg.Comparison.GetFullModeDelayMs())
		fmt.Printf("Table Parallelism: %d\n", r.cfg.Comparison.Parallelism)
		fmt.Printf("Partition Parallelism: %d\n", r.cfg.Comparison.GetFullModeParallelism())
	} else {
		fmt.Printf("Sample Size: %d per table\n", r.cfg.Comparison.SampleSize)
		fmt.Printf("Parallelism: %d\n", r.cfg.Comparison.Parallelism)
	}
	fmt.Printf("\nTables to compare (%d):\n", len(tables))

	for _, t := range tables {
		name := t.FullName()
		if target := r.targetTable(t); target.FullName() != name {
			name += " -> " + target.FullName()
		}
		fmt.Printf("  - %s (estimated %d rows)\n", name, t.EstimatedRows)
	}

	return nil
}

// Run executes the database comparison
func (r *Runner) Run(ctx context.Context) (*Result, error) {
	summary := reporter.NewSummary()
	summary.RunMode = r.runMode
	if r.runMode == "retry" {
		summary.InputDir = r.inputDir
	}

	// Set config info for initial summary
	summary.SetConfig(&reporter.RunConfig{
		SourceHost:      r.cfg.Source.Host,
		SourcePort:      r.cfg.Source.Port,
		TargetHost:      r.cfg.Target.Host,
		TargetPort:      r.cfg.Target.Port,
		SampleSize:      r.cfg.Comparison.SampleSize,
		Parallelism:     r.cfg.Comparison.Parallelism,
		IncludePatterns: r.cfg.Tables.Include,
		ExcludePatterns: r.cfg.Tables.Exclude,
	})

	// Create initial summary file immediately
	if err := summary.SaveJSON(r.outputDir); err != nil {
		r.logger.Warn("failed to create initial summary file", zap.Error(err))
	}

	// Connect to databases
	if err := r.connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer r.disconnect()

	r.logger.Info("init")

	// Initialize components
	r.srcSampler = sampler.NewSampler(r.srcConn, r.logger)
	r.targetSampler = sampler.NewSampler(r.targetConn, r.logger)

	var err2 error
	outputFormat := reporter.Format(r.cfg.Output.Format)
	if outputFormat == "" {
		outputFormat = reporter.FormatJSONL
	}
	r.reporter, err2 = reporter.NewLogger(r.outputDir, outputFormat, r.logger)
	if err2 != nil {
		return nil, fmt.Errorf("failed to create reporter: %w", err2)
	}

	if r.runMode == "retry" {
		r.logger.Info("compare start",
			zap.String("mode", r.runMode),
		)
		return r.runRetryMode(ctx, summary)
	}

	r.logger.Info("fetch tables")
	// Get tables to compare
	tables, err := r.getTablesToCompare(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tables: %w", err)
	}

	r.logger.Info("compare start",
		zap.Int("tables", len(tables)),
		zap.String("mode", r.runMode),
		zap.Int("sample_size", r.cfg.Comparison.SampleSize),
		zap.Int("parallelism", r.cfg.Comparison.Parallelism),
	)

	// Branch based on comparison mode
	if r.runMode == "full" {
		return r.runFullMode(ctx, tables, summary)
	}
	return r.runSamplingMode(ctx, tables, summary)
}

// runSamplingMode executes sampling-based comparison
func (r *Runner) runSamplingMode(ctx context.Context, tables []types.TableMeta, summary *reporter.Summary) (*Result, error) {
	// Compare tables with parallelism
	resultCh := make(chan *reporter.TableResult, len(tables))
	tableCh := make(chan types.TableMeta, len(tables))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Comparison.Parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx, tableCh, resultCh)
		}()
	}

	// Send tables to workers
	go func() {
		for _, t := range tables {
			select {
			case <-ctx.Done():
				return
			case tableCh <- t:
			}
		}
		close(tableCh)
	}()

	// Wait for workers and close result channel
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Start progress reporter (every 1 minute)
	totalTables := len(tables)
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				completed, sampled, matched, verified := summary.GetProgress()
				r.logger.Info("📊 progress",
					zap.String("elapsed", time.Since(summary.StartTime).Round(time.Second).String()),
					zap.String("tables", fmt.Sprintf("%d/%d", completed, totalTables)),
					zap.Int("sampled", sampled),
					zap.Int("matched", matched),
					zap.Int("verified_mismatches", verified),
				)
			case <-progressDone:
				return
			}
		}
	}()

	// Collect results
	for result := range resultCh {
		summary.AddResult(result)
	}

	// Stop progress reporter
	close(progressDone)

	summary.Finish()

	// Save summary
	if err := summary.SaveJSON(r.outputDir); err != nil {
		r.logger.Warn("failed to save summary", zap.Error(err))
	}

	return &Result{Summary: summary}, nil
}

// runFullMode executes full data comparison
func (r *Runner) runFullMode(ctx context.Context, tables []types.TableMeta, summary *reporter.Summary) (*Result, error) {
	r.logger.Info("running full comparison mode",
		zap.Int("tables", len(tables)),
		zap.Int("chunk_size", r.cfg.Comparison.GetFullModeChunkSize()),
		zap.Int("delay_ms", r.cfg.Comparison.GetFullModeDelayMs()),
		zap.Int("partition_parallelism", r.cfg.Comparison.GetFullModeParallelism()),
	)

	// Full comparator config
	fullConfig := comparator.FullCompareConfig{
		ChunkSize:   r.cfg.Comparison.GetFullModeChunkSize(),
		DelayMs:     r.cfg.Comparison.GetFullModeDelayMs(),
		Parallelism: r.cfg.Comparison.GetFullModeParallelism(),
		Options: comparator.ComparatorOptions{
			IgnoreDatetimePrecision:   r.cfg.Comparison.ShouldIgnoreDatetimePrecision(),
			IgnoreZeroDatetime:        r.cfg.Comparison.ShouldIgnoreZeroDatetime(),
			IgnoreJSONKeyOrder:        r.cfg.Comparison.ShouldIgnoreJSONKeyOrder(),
			GenerateCorrectionQueries: r.cfg.Comparison.ShouldGenerateCorrectionQueries(),
		},
	}

	// Result channel for table results
	resultCh := make(chan *reporter.TableResult, len(tables))
	tableCh := make(chan types.TableMeta, len(tables))

	// Progress channel for full mode progress reporting
	progressCh := make(chan *comparator.FullCompareProgress, 100)

	// Start workers for table-level parallelism
	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Comparison.Parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.fullModeWorker(ctx, tableCh, resultCh, progressCh, fullConfig)
		}()
	}

	// Send tables to workers
	go func() {
		for _, t := range tables {
			select {
			case <-ctx.Done():
				return
			case tableCh <- t:
			}
		}
		close(tableCh)
	}()

	// Wait for workers and close result channel
	go func() {
		wg.Wait()
		close(resultCh)
		close(progressCh)
	}()

	// Progress reporter goroutine (handles progress updates from full comparer)
	progressDone := make(chan struct{})
	go func() {
		for progress := range progressCh {
			processed, total, mismatches, elapsed, remaining := progress.GetProgress()
			pct := float64(0)
			if total > 0 {
				pct = float64(processed) / float64(total) * 100
			}
			r.logger.Info("📊 full mode progress",
				zap.String("table", progress.Table),
				zap.Int64("processed", processed),
				zap.Int64("total", total),
				zap.String("progress", fmt.Sprintf("%.1f%%", pct)),
				zap.Int64("mismatches", mismatches),
				zap.String("elapsed", elapsed.Round(time.Second).String()),
				zap.String("remaining", remaining.Round(time.Second).String()),
			)
		}
		close(progressDone)
	}()

	// Collect results
	for result := range resultCh {
		summary.AddResult(result)
	}

	// Wait for progress reporter to finish
	<-progressDone

	summary.Finish()

	// Save summary
	if err := summary.SaveJSON(r.outputDir); err != nil {
		r.logger.Warn("failed to save summary", zap.Error(err))
	}

	return &Result{Summary: summary}, nil
}

// fullModeWorker processes tables in full comparison mode
func (r *Runner) fullModeWorker(
	ctx context.Context,
	tables <-chan types.TableMeta,
	results chan<- *reporter.TableResult,
	progressCh chan<- *comparator.FullCompareProgress,
	config comparator.FullCompareConfig,
) {
	for table := range tables {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result := r.compareTableFull(ctx, table, config, progressCh)
		results <- result
	}
}

// compareTableFull performs full comparison for a single table
func (r *Runner) compareTableFull(
	ctx context.Context,
	table types.TableMeta,
	config comparator.FullCompareConfig,
	progressCh chan<- *comparator.FullCompareProgress,
) *reporter.TableResult {
	start := time.Now()
	result := &reporter.TableResult{
		Table:         table.FullName(),
		EstimatedRows: table.EstimatedRows,
		KeyColumns:    table.KeyColumns(),
	}

	// Check if table has a key
	if !table.HasKey() {
		result.Skipped = true
		result.SkipReason = "no primary key or unique key"
		r.logger.Info("skipping table without key",
			zap.String("table", table.FullName()),
		)
		return result
	}

	// Get columns to compare
	compareColumns := r.getCompareColumns(table)

	r.logger.Info("starting full comparison",
		zap.String("table", table.FullName()),
		zap.Int64("estimated_rows", table.EstimatedRows),
		zap.Int("columns", len(compareColumns)),
		zap.Int("chunk_size", config.ChunkSize),
		zap.Int("parallelism", config.Parallelism),
	)

	// Create full comparer
	fullComparer := comparator.NewFullComparer(r.srcConn, r.targetConn, config, r.logger)

	// Mismatch handler - writes mismatches immediately to file
	var mismatchMu sync.Mutex
	var reporterOpened bool

	handler := func(mismatches []types.Mismatch) error {
		if len(mismatches) == 0 {
			return nil
		}

		mismatchMu.Lock()
		defer mismatchMu.Unlock()

		// Open reporter for this table if not already opened
		if !reporterOpened {
			if err := r.reporter.Open(table.FullName()); err != nil {
				return err
			}
			reporterOpened = true
		}

		// Write mismatches immediately
		r.reporter.WriteBatch(mismatches)
		return nil
	}

	// Run full comparison
	fullResult, err := fullComparer.CompareTable(ctx, table, compareColumns, handler, progressCh)

	// Close reporter if it was opened
	mismatchMu.Lock()
	if reporterOpened {
		r.reporter.Close()
	}
	mismatchMu.Unlock()

	if err != nil {
		result.Error = fmt.Sprintf("full comparison failed: %v", err)
		return result
	}

	// Map full result to table result
	result.Sampled = int(fullResult.ProcessedRows)
	result.Matched = int(fullResult.MatchCount)
	result.MissingInTarget = int(fullResult.MissingInTarget)
	result.MissingInSource = int(fullResult.MissingInSource)
	result.Different = int(fullResult.Different)
	result.Verified = int(fullResult.MismatchCount) // In full mode, all mismatches are counted as verified
	result.Duration = time.Since(start)

	r.logger.Info("full comparison completed",
		zap.String("table", table.FullName()),
		zap.Int64("processed_rows", fullResult.ProcessedRows),
		zap.Int64("matched", fullResult.MatchCount),
		zap.Int64("mismatches", fullResult.MismatchCount),
		zap.Duration("duration", result.Duration),
	)

	return result
}

// connect establishes database connections
func (r *Runner) connect(ctx context.Context) error {
	poolCfg := db.DefaultPoolConfig()

	// Connect to source
	srcConn, err := db.NewConnection(ctx, r.cfg.Source, "source", poolCfg, r.logger)
	if err != nil {
		return fmt.Errorf("source connection failed: %w", err)
	}
	r.srcConn = srcConn
	r.srcMeta = db.NewMetaQuerier(srcConn, r.logger)

	// Connect to target
	targetConn, err := db.NewConnection(ctx, r.cfg.Target, "target", poolCfg, r.logger)
	if err != nil {
		r.srcConn.Close()
		return fmt.Errorf("target connection failed: %w", err)
	}
	r.targetConn = targetConn
	r.targetMeta = db.NewMetaQuerier(targetConn, r.logger)

	return nil
}

// disconnect closes database connections
func (r *Runner) disconnect() {
	if r.srcConn != nil {
		r.srcConn.Close()
	}
	if r.targetConn != nil {
		r.targetConn.Close()
	}
}

// getTablesToCompare retrieves tables to compare based on configuration
func (r *Runner) getTablesToCompare(ctx context.Context) ([]types.TableMeta, error) {
	r.logger.Info("step 1/4: resolve schemas")
	schemas := r.cfg.GetSchemas()

	if len(schemas) == 0 {
		// Query all schemas from source
		r.logger.Info("step 2/4: query all schemas")
		allTables, err := r.srcMeta.GetTables(ctx, "")
		if err != nil {
			return nil, err
		}

		// Get unique schemas
		schemaMap := make(map[string]struct{})
		for _, t := range allTables {
			schemaMap[t.Schema] = struct{}{}
		}
		for s := range schemaMap {
			schemas = append(schemas, s)
		}
		r.logger.Info("found schemas", zap.Int("count", len(schemas)))
	} else {
		r.logger.Info("step 2/4: use schemas", zap.Strings("schemas", schemas))
	}

	r.logger.Info("step 3/4: fetch tables", zap.Int("schema_count", len(schemas)))
	var allTables []types.TableMeta
	for _, schema := range schemas {
		tables, err := r.srcMeta.GetTables(ctx, schema)
		if err != nil {
			r.logger.Warn("failed to get tables from schema",
				zap.String("schema", schema),
				zap.Error(err),
			)
			continue
		}
		r.logger.Info("schema tables", zap.String("schema", schema), zap.Int("count", len(tables)))
		allTables = append(allTables, tables...)
	}

	// Apply filters (mapped tables are always compared)
	filtered := db.FilterTables(allTables, r.cfg.Tables.Include, r.cfg.Tables.Exclude)
	filtered = r.addMappedTables(allTables, filtered)
	r.logger.Info("step 4/4: apply filters",
		zap.Int("before", len(allTables)),
		zap.Int("after", len(filtered)),
	)

	// Get full metadata for all tables in batch (3 queries instead of N*4)
	r.logger.Info("fetch metadata", zap.Int("count", len(filtered)))
	result, err := r.srcMeta.GetTableMetaBatch(ctx, filtered)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get table metadata: %w", err)
	}

	// Filter out tables without metadata
	var validTables []types.TableMeta
	for _, meta := range result {
		if len(meta.Columns) == 0 {
			r.logger.Warn("skipping table with no columns",
				zap.String("table", meta.FullName()),
			)
			continue
		}
		validTables = append(validTables, meta)
	}

	if err := r.checkMappings(ctx, validTables); err != nil {
		return nil, err
	}

	r.logger.Info("metadata done", zap.Int("tables", len(validTables)))
	return validTables, nil
}

// addMappedTables appends mapping source tables that the filters left out
func (r *Runner) addMappedTables(allTables, filtered []types.TableMeta) []types.TableMeta {
	if len(r.cfg.Mappings) == 0 {
		return filtered
	}

	selected := make(map[string]bool, len(filtered))
	for _, t := range filtered {
		selected[strings.ToLower(t.FullName())] = true
	}
	for _, t := range allTables {
		key := strings.ToLower(t.FullName())
		if !selected[key] && r.cfg.MappingFor(t.FullName()) != nil {
			filtered = append(filtered, t)
			selected[key] = true
		}
	}
	return filtered
}

// targetTable returns the table to read on the target side. For a mapped
// table it points at the mapped target table and its column names.
func (r *Runner) targetTable(table types.TableMeta) types.TableMeta {
	m := r.cfg.MappingFor(table.FullName())
	if m == nil {
		return table
	}
	schema, name, _ := config.SplitTableName(m.Target)
	target := table
	target.Schema = schema
	target.Name = name
	target.ColumnMap = m.ColumnMap()
	return target
}

// checkMappings verifies that every mapping points at existing tables and
// columns, so a typo fails the run instead of showing up as missing rows.
func (r *Runner) checkMappings(ctx context.Context, tables []types.TableMeta) error {
	if len(r.cfg.Mappings) == 0 {
		return nil
	}

	sourceByName := make(map[string]types.TableMeta, len(tables))
	for _, t := range tables {
		sourceByName[strings.ToLower(t.FullName())] = t
	}

	var targets []types.TableMeta
	for _, m := range r.cfg.Mappings {
		if _, ok := sourceByName[strings.ToLower(m.Source)]; !ok {
			return fmt.Errorf("mapping source table %s not found", m.Source)
		}
		schema, name, _ := config.SplitTableName(m.Target)
		targets = append(targets, types.TableMeta{Schema: schema, Name: name})
	}

	targetMetas, err := r.targetMeta.GetTableMetaBatch(ctx, targets)
	if err != nil {
		return fmt.Errorf("failed to get mapping target metadata: %w", err)
	}

	for i, m := range r.cfg.Mappings {
		source := sourceByName[strings.ToLower(m.Source)]
		target := targetMetas[i]
		if len(target.Columns) == 0 {
			return fmt.Errorf("mapping target table %s not found", m.Target)
		}

		sourceCols := columnNameSet(source.Columns)
		for src := range m.Columns {
			if !sourceCols[strings.ToLower(src)] {
				return fmt.Errorf("mapping %s: source column %q not found", m.Source, src)
			}
		}

		targetCols := columnNameSet(target.Columns)
		mapped := r.targetTable(source)
		for _, col := range source.Columns {
			physical := mapped.PhysicalColumn(col.Name)
			if !targetCols[strings.ToLower(physical)] {
				return fmt.Errorf("mapping %s: target column %s.%s not found (for source column %s)",
					m.Source, m.Target, physical, col.Name)
			}
		}
	}
	return nil
}

func columnNameSet(columns []types.ColumnMeta) map[string]bool {
	set := make(map[string]bool, len(columns))
	for _, col := range columns {
		set[strings.ToLower(col.Name)] = true
	}
	return set
}

// worker processes tables from the channel
func (r *Runner) worker(ctx context.Context, tables <-chan types.TableMeta, results chan<- *reporter.TableResult) {
	for table := range tables {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result := r.compareTable(ctx, table)
		results <- result
	}
}

// compareTable compares a single table
func (r *Runner) compareTable(ctx context.Context, table types.TableMeta) *reporter.TableResult {
	start := time.Now()
	result := &reporter.TableResult{
		Table:         table.FullName(),
		EstimatedRows: table.EstimatedRows,
		KeyColumns:    table.KeyColumns(),
	}

	// Check if table has a key
	if !table.HasKey() {
		result.Skipped = true
		result.SkipReason = "no primary key or unique key"
		r.logger.Info("skipping table without key",
			zap.String("table", table.FullName()),
		)
		return result
	}

	// Get columns to compare (may exclude large columns if configured)
	compareColumns := r.getCompareColumns(table)

	r.logger.Info("compare started",
		zap.String("table", table.FullName()),
		zap.Int64("estimated_rows", table.EstimatedRows),
		zap.Int("columns", len(compareColumns)),
	)

	// Sample from source
	var srcResult *sampler.TableSampleResult
	var err error

	// For small tables (rows <= sample_size), use direct sampling without partitioning
	if table.EstimatedRows <= int64(r.cfg.Comparison.SampleSize) {
		// Use SampleSize as limit since EstimatedRows can be inaccurate (could be 0 but have actual data)
		limit := r.cfg.Comparison.SampleSize
		r.logger.Debug("using direct sampling for small table",
			zap.String("table", table.FullName()),
			zap.Int64("estimated_rows", table.EstimatedRows),
			zap.Int("limit", limit),
		)
		srcResult, err = r.srcSampler.SampleTableDirectWithColumns(ctx, table, limit, compareColumns)
	} else {
		// Create partition plan for larger tables
		plan, planErr := r.createPartitionPlan(ctx, table)
		if planErr != nil {
			// Check if error is due to empty table (nil min/max values)
			if strings.Contains(planErr.Error(), "min/max values cannot be nil") {
				result.Skipped = true
				result.SkipReason = "empty table (no data)"
				r.logger.Info("compare skipped",
					zap.String("table", table.FullName()),
				)
				return result
			}
			result.Error = fmt.Sprintf("failed to create partition plan: %v", planErr)
			r.logger.Warn("failed to create partition plan",
				zap.String("table", table.FullName()),
				zap.Error(planErr),
			)
			return result
		}
		srcResult, err = r.srcSampler.SampleTableWithColumns(ctx, plan, compareColumns)
	}

	if err != nil {
		result.Error = fmt.Sprintf("failed to sample source: %v", err)
		return result
	}

	// Skip if no rows sampled (empty table)
	if len(srcResult.Samples) == 0 {
		result.Skipped = true
		result.SkipReason = "empty table (no data)"
		r.logger.Info("compare skipped",
			zap.String("table", table.FullName()),
		)
		return result
	}

	// Sample same keys from target (using same filtered columns)
	targetRows, err := r.sampleTargetByKeys(ctx, table, srcResult.Samples, compareColumns)
	if err != nil {
		result.Error = fmt.Sprintf("failed to sample target: %v", err)
		return result
	}

	// Compare samples
	compOpts := comparator.ComparatorOptions{
		IgnoreDatetimePrecision:   r.cfg.Comparison.ShouldIgnoreDatetimePrecision(),
		IgnoreZeroDatetime:        r.cfg.Comparison.ShouldIgnoreZeroDatetime(),
		IgnoreJSONKeyOrder:        r.cfg.Comparison.ShouldIgnoreJSONKeyOrder(),
		GenerateCorrectionQueries: r.cfg.Comparison.ShouldGenerateCorrectionQueries(),
	}
	comp := comparator.NewComparatorWithOptions(table.KeyColumns(), compOpts)
	compareResult := comp.CompareSamples(srcResult.Samples, targetRows)

	// Convert to mismatches (with deduplication)
	mismatches := comp.ToMismatches(compareResult, table.FullName())

	// Count mismatch types from deduplicated results
	var missingInTarget, missingInSource, different int
	for _, m := range mismatches {
		switch m.Type {
		case types.MismatchMissingTarget:
			missingInTarget++
		case types.MismatchMissingSource:
			missingInSource++
		case types.MismatchDataDiff:
			different++
		}
	}

	result.Sampled = compareResult.TotalSource
	result.Matched = compareResult.Matched
	result.MissingInTarget = missingInTarget
	result.MissingInSource = missingInSource
	result.Different = different

	// Handle mismatches (no automatic retry in sample/full)
	if len(mismatches) > 0 {
		result.Verified = len(mismatches)
		result.Resolved = 0

		if err := r.reporter.Open(table.FullName()); err == nil {
			r.reporter.WriteBatch(mismatches)
			r.reporter.Close()
		}
	}

	result.Duration = time.Since(start)

	r.logger.Info("compare done",
		zap.String("table", table.FullName()),
		zap.Int("sampled", result.Sampled),
		zap.Int("matched", result.Matched),
		zap.Int("verified_mismatches", result.Verified),
		zap.Duration("duration", result.Duration),
	)

	return result
}

// createPartitionPlan creates a partition plan for sampling
func (r *Runner) createPartitionPlan(ctx context.Context, table types.TableMeta) (*sampler.PartitionPlan, error) {
	keyColumns := table.KeyColumns()
	if len(keyColumns) == 0 {
		return nil, fmt.Errorf("table has no key columns")
	}

	keyColumn := keyColumns[0]

	// Get min/max values for the key column
	minVal, maxVal, err := r.srcMeta.GetMinMax(ctx, table.Schema, table.Name, keyColumns)
	if err != nil {
		return nil, fmt.Errorf("failed to get min/max values: %w", err)
	}

	cfg := sampler.PartitionConfig{
		SampleSize:        r.cfg.Comparison.SampleSize,
		MinPartitions:     10,
		MaxPartitions:     500,
		MaxSamplesPerPart: 1000, // LIMIT per partition query to avoid ORDER BY RAND() performance issues
		LargeTableThresh:  1000000,
	}

	return sampler.CreatePartitionPlan(table, keyColumn, minVal, maxVal, cfg)
}

// sampleTargetByKeys samples target using keys from source samples
// Uses batch queries for efficiency (1000 rows per query instead of 1 row per query)
func (r *Runner) sampleTargetByKeys(ctx context.Context, table types.TableMeta, srcRows []types.Row, columns []string) ([]types.Row, error) {
	if len(srcRows) == 0 {
		return nil, nil
	}

	keyColumns := table.KeyColumns()
	targetTable := r.targetTable(table)

	const batchSize = 1000 // MySQL IN clause safe limit
	var allTargetRows []types.Row

	for i := 0; i < len(srcRows); i += batchSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		end := i + batchSize
		if end > len(srcRows) {
			end = len(srcRows)
		}
		batch := srcRows[i:end]

		// Extract key values for batch
		keyValuesList := make([]map[string]interface{}, len(batch))
		for j, srcRow := range batch {
			keyValues := make(map[string]interface{})
			for _, col := range keyColumns {
				if val, ok := srcRow[col]; ok {
					keyValues[col] = val
				}
			}
			keyValuesList[j] = keyValues
		}

		// Batch lookup in target
		targetRows, err := r.targetSampler.SampleByKeys(ctx, targetTable, keyValuesList, columns)
		if err != nil {
			r.logger.Warn("batch target lookup failed, falling back to individual queries",
				zap.String("table", table.FullName()),
				zap.Int("batch_size", len(batch)),
				zap.Error(err),
			)
			// Fallback to individual queries for this batch
			for _, kv := range keyValuesList {
				targetRow, err := r.targetSampler.SampleByKey(ctx, targetTable, kv)
				if err != nil {
					r.logger.Debug("failed to lookup target row",
						zap.String("table", table.FullName()),
						zap.Any("key", kv),
						zap.Error(err),
					)
					continue
				}
				if targetRow != nil {
					allTargetRows = append(allTargetRows, targetRow)
				}
			}
			continue
		}

		allTargetRows = append(allTargetRows, targetRows...)
	}

	return allTargetRows, nil
}

// getCompareColumns returns columns to use for comparison
// If exclude_large_columns is enabled, filters out TEXT, BLOB, JSON types
// Always includes key columns regardless of type
func (r *Runner) getCompareColumns(table types.TableMeta) []string {
	keyColumns := make(map[string]bool)
	for _, col := range table.KeyColumns() {
		keyColumns[col] = true
	}

	var columns []string
	var excludedCount int

	for _, col := range table.Columns {
		// Always include key columns
		if keyColumns[col.Name] {
			columns = append(columns, col.Name)
			continue
		}

		// Check if should ignore large columns
		if r.cfg.Comparison.ShouldIgnoreLargeColumns() && config.IsLargeColumnType(col.ColumnType) {
			excludedCount++
			continue
		}

		columns = append(columns, col.Name)
	}

	if excludedCount > 0 {
		r.logger.Debug("excluded large columns from comparison",
			zap.String("table", table.FullName()),
			zap.Int("excluded", excludedCount),
			zap.Int("comparing", len(columns)),
		)
	}

	return columns
}
