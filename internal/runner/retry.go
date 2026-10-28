package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/case-88/my-db-compare/internal/comparator"
	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/internal/reporter"
	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

const (
	textRecordSeparator = "----------------------------------------"
	maxJSONLLineBytes   = 10 * 1024 * 1024 // 10MB per line
)

func (r *Runner) runRetryMode(ctx context.Context, summary *reporter.Summary) (*Result, error) {
	if r.inputDir == "" {
		return nil, fmt.Errorf("input directory is required for retry mode")
	}

	files, err := findMismatchFiles(r.inputDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no mismatch logs found in %s", r.inputDir)
	}

	compOpts := comparator.ComparatorOptions{
		IgnoreDatetimePrecision:   r.cfg.Comparison.ShouldIgnoreDatetimePrecision(),
		IgnoreZeroDatetime:        r.cfg.Comparison.ShouldIgnoreZeroDatetime(),
		IgnoreJSONKeyOrder:        r.cfg.Comparison.ShouldIgnoreJSONKeyOrder(),
		GenerateCorrectionQueries: r.cfg.Comparison.ShouldGenerateCorrectionQueries(),
	}
	generateCorrection := compOpts.GenerateCorrectionQueries

	tableFiles := groupMismatchFilesByTable(r.logger, files)
	if len(tableFiles) == 0 {
		return nil, fmt.Errorf("no mismatch logs found in %s", r.inputDir)
	}

	parallelism := r.cfg.Comparison.Parallelism
	if parallelism < 1 {
		parallelism = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type tableWork struct {
		table string
		files []string
	}

	workCh := make(chan tableWork, len(tableFiles))
	resultCh := make(chan *reporter.TableResult, len(tableFiles))
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for work := range workCh {
				comp := comparator.NewComparatorWithOptions([]string{}, compOpts)
				result, err := r.retryTable(ctx, work.table, work.files, comp, generateCorrection)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
				resultCh <- result
			}
		}()
	}

	for table, tableFileList := range tableFiles {
		workCh <- tableWork{table: table, files: tableFileList}
	}
	close(workCh)

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for {
		select {
		case err := <-errCh:
			if err != nil {
				wg.Wait()
				return nil, err
			}
		case result, ok := <-resultCh:
			if !ok {
				summary.Finish()
				if err := summary.SaveJSON(r.outputDir); err != nil {
					r.logger.Warn("failed to write summary", zap.Error(err))
				}
				return &Result{Summary: summary}, nil
			}
			summary.AddResult(result)
		}
	}
}

func (r *Runner) retryTable(
	ctx context.Context,
	tableName string,
	files []string,
	comp *comparator.Comparator,
	generateCorrection bool,
) (*reporter.TableResult, error) {
	start := time.Now()
	result := &reporter.TableResult{
		Table: tableName,
	}
	r.logger.Info("compare started",
		zap.String("table", tableName),
		zap.Int("files", len(files)),
	)

	outputFormat := reporter.Format(r.cfg.Output.Format)
	if outputFormat == "" {
		outputFormat = reporter.FormatJSONL
	}
	tableLogger, err := reporter.NewLogger(r.outputDir, outputFormat, r.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create retry logger: %w", err)
	}

	opened := false
	openWriter := func() error {
		if opened {
			return nil
		}
		if err := tableLogger.Open(tableName); err != nil {
			return err
		}
		opened = true
		return nil
	}
	defer func() {
		if opened {
			tableLogger.Close()
		}
	}()

	for _, filePath := range files {
		r.logger.Info("retry file",
			zap.String("table", tableName),
			zap.String("file", filepath.Base(filePath)),
		)
		if err := r.retryMismatchFile(ctx, filePath, tableName, comp, generateCorrection, result, openWriter, tableLogger); err != nil {
			return nil, err
		}
	}

	result.Duration = time.Since(start)
	r.logger.Info("compare done",
		zap.String("table", tableName),
		zap.Int("mismatches", result.Verified),
		zap.Int("resolved", result.Resolved),
		zap.Duration("duration", result.Duration),
	)
	return result, nil
}

func (r *Runner) retryMismatchFile(
	ctx context.Context,
	filePath string,
	defaultTable string,
	comp *comparator.Comparator,
	generateCorrection bool,
	result *reporter.TableResult,
	openWriter func() error,
	tableLogger *reporter.Logger,
) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open mismatch file: %w", err)
	}
	defer file.Close()

	handleMismatch := func(m types.Mismatch) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if m.Table != "" && m.Table != defaultTable {
			r.logger.Warn("mismatch table differs from file name, using file name",
				zap.String("file_table", defaultTable),
				zap.String("record_table", m.Table),
				zap.String("path", filePath),
			)
		}
		m.Table = defaultTable

		if strings.TrimSpace(m.Query) == "" {
			r.logger.Warn("mismatch record missing query, skipping", zap.String("table", defaultTable))
			return nil
		}

		result.Sampled++

		srcRow, srcErr := querySingleRow(ctx, r.srcConn, m.Query)
		if srcErr != nil {
			r.logger.Warn("source query failed, keeping mismatch",
				zap.String("table", defaultTable),
				zap.Error(srcErr),
			)
			result.Error = appendError(result.Error, srcErr)
			return r.writeRetryMismatch(m, result, openWriter, tableLogger)
		}

		targetRow, targetErr := querySingleRow(ctx, r.targetConn, m.Query)
		if targetErr != nil {
			r.logger.Warn("target query failed, keeping mismatch",
				zap.String("table", defaultTable),
				zap.Error(targetErr),
			)
			result.Error = appendError(result.Error, targetErr)
			return r.writeRetryMismatch(m, result, openWriter, tableLogger)
		}

		now := time.Now()

		// Both missing: resolved
		if srcRow == nil && targetRow == nil {
			result.Resolved++
			result.Matched++
			return nil
		}

		// Missing in target
		if srcRow != nil && targetRow == nil {
			newMismatch := types.Mismatch{
				Table:      defaultTable,
				PK:         m.PK,
				Type:       types.MismatchMissingTarget,
				SourceData: srcRow,
				Query:      m.Query,
				Timestamp:  now,
			}
			r.populateCorrectionQuery(&newMismatch, generateCorrection)
			return r.writeRetryMismatch(newMismatch, result, openWriter, tableLogger)
		}

		// Missing in source
		if srcRow == nil && targetRow != nil {
			newMismatch := types.Mismatch{
				Table:      defaultTable,
				PK:         m.PK,
				Type:       types.MismatchMissingSource,
				TargetData: targetRow,
				Query:      m.Query,
				Timestamp:  now,
			}
			r.populateCorrectionQuery(&newMismatch, generateCorrection)
			return r.writeRetryMismatch(newMismatch, result, openWriter, tableLogger)
		}

		diffCols := comp.CompareRows(srcRow, targetRow)
		if len(diffCols) == 0 {
			result.Resolved++
			result.Matched++
			return nil
		}

		newMismatch := types.Mismatch{
			Table:       defaultTable,
			PK:          m.PK,
			Type:        types.MismatchDataDiff,
			DiffColumns: diffCols,
			SourceData:  srcRow,
			TargetData:  targetRow,
			Query:       m.Query,
			Timestamp:   now,
		}
		r.populateCorrectionQuery(&newMismatch, generateCorrection)
		return r.writeRetryMismatch(newMismatch, result, openWriter, tableLogger)
	}

	switch ext {
	case ".jsonl":
		return parseJSONL(file, handleMismatch)
	case ".json":
		return parseJSON(file, handleMismatch)
	case ".txt":
		return parseText(file, defaultTable, handleMismatch)
	default:
		r.logger.Warn("unsupported mismatch file extension", zap.String("path", filePath))
		return nil
	}
}

func (r *Runner) writeRetryMismatch(m types.Mismatch, result *reporter.TableResult, openWriter func() error, tableLogger *reporter.Logger) error {
	if err := openWriter(); err != nil {
		return err
	}
	return r.writeRetryMismatchWithLogger(m, result, tableLogger)
}

func (r *Runner) writeRetryMismatchWithLogger(m types.Mismatch, result *reporter.TableResult, tableLogger *reporter.Logger) error {
	result.Verified++
	switch m.Type {
	case types.MismatchMissingTarget:
		result.MissingInTarget++
	case types.MismatchMissingSource:
		result.MissingInSource++
	case types.MismatchDataDiff:
		result.Different++
	}
	if tableLogger == nil {
		return nil
	}
	if err := tableLogger.Write(m); err != nil {
		return err
	}
	return nil
}

func (r *Runner) populateCorrectionQuery(m *types.Mismatch, enabled bool) {
	if !enabled {
		return
	}
	m.CorrectionQuery = types.BuildCorrectionQuery(m.Type, m.Table, m.PK, m.SourceData, m.DiffColumns)
}

func findMismatchFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read input directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "mismatch-") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".jsonl" && ext != ".json" && ext != ".txt" {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files, nil
}

func tableNameFromMismatchFile(path string) string {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "mismatch-") {
		return ""
	}
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return strings.TrimPrefix(name, "mismatch-")
}

func groupMismatchFilesByTable(logger *zap.Logger, files []string) map[string][]string {
	if logger == nil {
		logger = zap.NewNop()
	}
	result := make(map[string][]string)
	for _, filePath := range files {
		tableName := tableNameFromMismatchFile(filePath)
		if tableName == "" {
			logger.Warn("skipping mismatch file with unknown table name", zap.String("path", filePath))
			continue
		}
		result[tableName] = append(result[tableName], filePath)
	}
	return result
}

func parseJSONL(file *os.File, handle func(types.Mismatch) error) error {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m types.Mismatch
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return fmt.Errorf("failed to parse jsonl mismatch: %w", err)
		}
		if err := handle(m); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func parseJSON(file *os.File, handle func(types.Mismatch) error) error {
	dec := json.NewDecoder(file)
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("failed to read json mismatch: %w", err)
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("expected json array in mismatch file")
	}
	for dec.More() {
		var m types.Mismatch
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("failed to decode json mismatch: %w", err)
		}
		if err := handle(m); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

func parseText(file *os.File, defaultTable string, handle func(types.Mismatch) error) error {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONLLineBytes)

	current := types.Mismatch{
		Table: defaultTable,
		PK:    map[string]interface{}{},
	}

	flush := func() error {
		if strings.TrimSpace(current.Query) == "" {
			return nil
		}
		err := handle(current)
		current = types.Mismatch{
			Table: defaultTable,
			PK:    map[string]interface{}{},
		}
		return err
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == textRecordSeparator {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "Table:") {
			current.Table = strings.TrimSpace(strings.TrimPrefix(line, "Table:"))
			continue
		}
		if strings.HasPrefix(line, "Type:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Type:"))
			current.Type = types.MismatchType(val)
			continue
		}
		if strings.HasPrefix(line, "PK:") {
			pkStr := strings.TrimSpace(strings.TrimPrefix(line, "PK:"))
			if pkStr != "" {
				current.PK = parsePKLine(pkStr)
			}
			continue
		}
		if strings.HasPrefix(line, "Query:") {
			current.Query = strings.TrimSpace(strings.TrimPrefix(line, "Query:"))
			continue
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func parsePKLine(pkStr string) map[string]interface{} {
	result := make(map[string]interface{})
	parts := strings.Split(pkStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		result[key] = parsePKValue(val)
	}
	return result
}

func parsePKValue(val string) interface{} {
	if val == "<NULL>" {
		return nil
	}
	if i, err := strconv.ParseInt(val, 10, 64); err == nil {
		return i
	}
	if u, err := strconv.ParseUint(val, 10, 64); err == nil {
		return u
	}
	return val
}

func appendError(existing string, err error) string {
	if err == nil {
		return existing
	}
	if existing == "" {
		return err.Error()
	}
	return existing + "; " + err.Error()
}

func querySingleRow(ctx context.Context, conn *db.Connection, query string) (types.Row, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var row types.Row
	count := 0
	for rows.Next() {
		values := make([]interface{}, len(colNames))
		valuePtrs := make([]interface{}, len(colNames))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		if count == 0 {
			row = make(types.Row, len(colNames))
			for i, col := range colNames {
				row[col] = values[i]
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if count == 0 {
		return nil, nil
	}
	if count > 1 {
		// Query should return a single row; keep first row
	}
	return row, nil
}
