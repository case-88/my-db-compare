package comparator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

// ComparatorOptions holds options for comparison
type ComparatorOptions struct {
	IgnoreDatetimePrecision   bool // Compare datetime at second precision (ignore microseconds)
	IgnoreZeroDatetime        bool // Treat '0000-00-00 00:00:00' as NULL
	IgnoreJSONKeyOrder        bool // Compare JSON semantically (ignore key order)
	GenerateCorrectionQueries bool // Generate UPDATE/INSERT/DELETE queries to fix target
}

// DefaultComparatorOptions returns default options
func DefaultComparatorOptions() ComparatorOptions {
	return ComparatorOptions{
		IgnoreDatetimePrecision:   true,  // default: ignore precision
		IgnoreZeroDatetime:        true,  // default: treat zero datetime as NULL
		IgnoreJSONKeyOrder:        true,  // default: ignore key order (semantic comparison)
		GenerateCorrectionQueries: false, // default: don't generate correction queries
	}
}

// Comparator compares rows between source and target
type Comparator struct {
	hasher     *Hasher
	keyColumns []string
	options    ComparatorOptions
}

// NewComparator creates a new Comparator with default options
func NewComparator(keyColumns []string) *Comparator {
	return NewComparatorWithOptions(keyColumns, DefaultComparatorOptions())
}

// NewComparatorWithOptions creates a new Comparator with specified options
func NewComparatorWithOptions(keyColumns []string, options ComparatorOptions) *Comparator {
	return &Comparator{
		hasher:     NewHasher(),
		keyColumns: keyColumns,
		options:    options,
	}
}

// CompareResult holds the result of comparing two sets of rows
type CompareResult struct {
	MissingInTarget []types.Row // Rows in source but not in target
	MissingInSrc    []types.Row // Rows in target but not in source
	Different       []DiffPair  // Rows that exist in both but have different data
	Matched         int         // Count of rows that matched exactly
	TotalSource     int         // Total rows from source
	TotalTarget     int         // Total rows from target
}

// DiffPair holds a pair of rows with differences
type DiffPair struct {
	SourceRow   types.Row
	TargetRow   types.Row
	DiffColumns []string
}

// CompareSamples compares source and target samples
// Returns mismatches that need re-verification
func (c *Comparator) CompareSamples(srcRows, targetRows []types.Row) *CompareResult {
	result := &CompareResult{
		TotalSource: len(srcRows),
		TotalTarget: len(targetRows),
	}

	// Build hash maps for fast lookup
	srcMap := c.hasher.BuildHashMap(srcRows, c.keyColumns)
	targetMap := c.hasher.BuildHashMap(targetRows, c.keyColumns)

	// Find rows missing in target or different
	for keyHash, srcRowHash := range srcMap {
		targetRowHash, exists := targetMap[keyHash]
		if !exists {
			result.MissingInTarget = append(result.MissingInTarget, srcRowHash.Row)
			continue
		}

		// Key exists in both - check full row hash
		if srcRowHash.Hash != targetRowHash.Hash {
			// Hash differs - find which columns are different
			diffCols := c.findDiffColumns(srcRowHash.Row, targetRowHash.Row)
			if len(diffCols) > 0 {
				// Actual differences found
				result.Different = append(result.Different, DiffPair{
					SourceRow:   srcRowHash.Row,
					TargetRow:   targetRowHash.Row,
					DiffColumns: diffCols,
				})
			} else {
				// Hash differed but values are equal (e.g., datetime format difference)
				result.Matched++
			}
		} else {
			result.Matched++
		}
	}

	// Find rows missing in source
	for keyHash, targetRowHash := range targetMap {
		if _, exists := srcMap[keyHash]; !exists {
			result.MissingInSrc = append(result.MissingInSrc, targetRowHash.Row)
		}
	}

	return result
}

// CompareRows compares two individual rows and returns different columns
func (c *Comparator) CompareRows(srcRow, dstRow types.Row) []string {
	return c.findDiffColumns(srcRow, dstRow)
}

// findDiffColumns finds columns that differ between two rows
func (c *Comparator) findDiffColumns(srcRow, dstRow types.Row) []string {
	var diffCols []string

	// Check all columns in source
	for col, srcVal := range srcRow {
		dstVal, exists := dstRow[col]
		if !exists {
			diffCols = append(diffCols, col)
			continue
		}

		if !c.valuesEqual(srcVal, dstVal) {
			diffCols = append(diffCols, col)
		}
	}

	// Check columns in dest but not in source
	for col := range dstRow {
		if _, exists := srcRow[col]; !exists {
			diffCols = append(diffCols, col)
		}
	}

	return diffCols
}

// valuesEqual compares two values for equality
// Handles []byte, time.Time, datetime strings, JSON, and other types
func (c *Comparator) valuesEqual(a, b interface{}) bool {
	// Handle nil
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// When IgnoreZeroDatetime is enabled, treat zero datetime as NULL
		if c.options.IgnoreZeroDatetime {
			if a == nil && isZeroDatetime(b) {
				return true
			}
			if b == nil && isZeroDatetime(a) {
				return true
			}
		}
		return false
	}

	ignorePrecision := c.options.IgnoreDatetimePrecision
	ignoreJSONKeyOrder := c.options.IgnoreJSONKeyOrder

	// Handle []byte specially (may contain datetime strings or JSON)
	aBytes, aIsBytes := a.([]byte)
	bBytes, bIsBytes := b.([]byte)
	if aIsBytes && bIsBytes {
		// Fast path: exact match
		if bytes.Equal(aBytes, bBytes) {
			return true
		}
		// Try to compare as datetime strings first (handles datetime(0) vs datetime(6))
		if ignorePrecision && timesEqualAsStrings(string(aBytes), string(bBytes)) {
			return true
		}
		// Try JSON semantic comparison if enabled
		if ignoreJSONKeyOrder && jsonEqual(aBytes, bBytes) {
			return true
		}
		return false
	}

	// Handle string (may be datetime or JSON)
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		// Fast path: exact match
		if aStr == bStr {
			return true
		}
		// Try to compare as datetime strings first
		if ignorePrecision && timesEqualAsStrings(aStr, bStr) {
			return true
		}
		// Try JSON semantic comparison if enabled
		if ignoreJSONKeyOrder && jsonEqual([]byte(aStr), []byte(bStr)) {
			return true
		}
		return false
	}

	// Handle mixed []byte and string
	if aIsBytes && bIsStr {
		aStr := string(aBytes)
		if aStr == bStr {
			return true
		}
		if ignorePrecision && timesEqualAsStrings(aStr, bStr) {
			return true
		}
		if ignoreJSONKeyOrder && jsonEqual(aBytes, []byte(bStr)) {
			return true
		}
		return false
	}
	if aIsStr && bIsBytes {
		bStr := string(bBytes)
		if aStr == bStr {
			return true
		}
		if ignorePrecision && timesEqualAsStrings(aStr, bStr) {
			return true
		}
		if ignoreJSONKeyOrder && jsonEqual([]byte(aStr), bBytes) {
			return true
		}
		return false
	}

	// Handle time.Time
	aTime, aIsTime := a.(time.Time)
	bTime, bIsTime := b.(time.Time)
	if aIsTime && bIsTime {
		if ignorePrecision {
			return aTime.Truncate(time.Second).Equal(bTime.Truncate(time.Second))
		}
		return aTime.Equal(bTime)
	}

	// Handle numeric type comparisons (int64 vs int, etc.)
	aNum, aIsNum := toInt64(a)
	bNum, bIsNum := toInt64(b)
	if aIsNum && bIsNum {
		return aNum == bNum
	}

	// Use reflect.DeepEqual as fallback
	return reflect.DeepEqual(a, b)
}

// jsonEqual compares two JSON byte slices semantically (ignoring key order)
// Returns true if both are valid JSON and structurally equal
// Optimized with early termination checks to avoid unnecessary parsing
func jsonEqual(a, b []byte) bool {
	// Quick check: if either is empty or doesn't look like JSON, skip
	if len(a) == 0 || len(b) == 0 {
		return false
	}

	// JSON must start with { or [
	aStart, bStart := a[0], b[0]
	if (aStart != '{' && aStart != '[') || (bStart != '{' && bStart != '[') {
		return false
	}

	// Early termination: bracket type mismatch (object vs array)
	// If one starts with { and other with [, they cannot be equal
	if aStart != bStart {
		return false
	}

	// Early termination: significant length difference
	// If lengths differ by more than 50%, unlikely to be semantically equal
	// (accounting for whitespace differences)
	lenA, lenB := len(a), len(b)
	if lenA > lenB*2 || lenB > lenA*2 {
		return false
	}

	var objA, objB interface{}
	if err := json.Unmarshal(a, &objA); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &objB); err != nil {
		return false
	}
	return reflect.DeepEqual(objA, objB)
}

// timesEqualAsStrings compares two strings as datetime values
// Handles datetime(0) vs datetime(6) comparison: "2015-10-05 12:05:38" vs "2015-10-05 12:05:38.000000"
// Optimized with pattern-based format detection to minimize parsing attempts
func timesEqualAsStrings(a, b string) bool {
	// Quick equality check
	if a == b {
		return true
	}

	// Early termination: check minimum datetime length
	// Shortest valid format is "2006-01-02" (10 chars)
	if len(a) < 10 || len(b) < 10 {
		return false
	}

	// Early termination: check if both look like datetime strings
	// Valid datetime starts with YYYY-MM-DD pattern (digit-digit-digit-digit-hyphen)
	if !looksLikeDatetime(a) || !looksLikeDatetime(b) {
		return false
	}

	// Get format based on string length and pattern for optimized parsing
	aFormat := detectDatetimeFormat(a)
	bFormat := detectDatetimeFormat(b)

	var aTime, bTime time.Time
	var aErr, bErr error

	// Try detected format first, then fall back to all formats
	if aFormat != "" {
		aTime, aErr = time.Parse(aFormat, a)
	}
	if aErr != nil {
		aTime, aErr = parseWithAllFormats(a)
	}

	if bFormat != "" {
		bTime, bErr = time.Parse(bFormat, b)
	}
	if bErr != nil {
		bTime, bErr = parseWithAllFormats(b)
	}

	// If both parsed successfully, compare truncated to seconds
	if aErr == nil && bErr == nil {
		// Truncate to seconds for comparison (ignore microseconds difference)
		return aTime.Truncate(time.Second).Equal(bTime.Truncate(time.Second))
	}

	return false
}

// looksLikeDatetime performs a quick check if string looks like a datetime
// Checks for YYYY-MM-DD pattern at the beginning
func looksLikeDatetime(s string) bool {
	if len(s) < 10 {
		return false
	}
	// Check YYYY-MM-DD pattern: 4 digits, hyphen, 2 digits, hyphen, 2 digits
	return isDigit(s[0]) && isDigit(s[1]) && isDigit(s[2]) && isDigit(s[3]) &&
		s[4] == '-' &&
		isDigit(s[5]) && isDigit(s[6]) &&
		s[7] == '-' &&
		isDigit(s[8]) && isDigit(s[9])
}

// isDigit checks if a byte is a digit
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// detectDatetimeFormat detects the datetime format based on string length and pattern
// Returns empty string if format cannot be determined
func detectDatetimeFormat(s string) string {
	n := len(s)
	switch {
	case n == 10:
		// "2006-01-02"
		return "2006-01-02"
	case n == 19:
		// "2006-01-02 15:04:05" or "2006-01-02T15:04:05"
		if s[10] == 'T' {
			return "2006-01-02T15:04:05"
		}
		return "2006-01-02 15:04:05"
	case n == 20 && s[n-1] == 'Z':
		// "2006-01-02T15:04:05Z"
		return "2006-01-02T15:04:05Z"
	case n == 23:
		// "2006-01-02 15:04:05.999" (milliseconds)
		if s[10] == 'T' {
			return "2006-01-02T15:04:05.999"
		}
		return "2006-01-02 15:04:05.999"
	case n == 26:
		// "2006-01-02 15:04:05.999999" (microseconds)
		if s[10] == 'T' {
			return "2006-01-02T15:04:05.999999"
		}
		return "2006-01-02 15:04:05.999999"
	case n >= 25 && (s[n-1] == 'Z' || s[n-3] == ':' || s[n-5] == '+' || s[n-5] == '-'):
		// ISO format with timezone
		if s[10] == 'T' {
			if n > 26 {
				return "2006-01-02T15:04:05.999999999Z07:00"
			}
			return "2006-01-02T15:04:05Z07:00"
		}
	}
	return ""
}

// parseWithAllFormats tries all known datetime formats
func parseWithAllFormats(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05.999999999",       // with nanoseconds
		"2006-01-02 15:04:05.999999",          // with microseconds
		"2006-01-02 15:04:05.999",             // with milliseconds
		"2006-01-02 15:04:05",                 // without fractional seconds
		"2006-01-02T15:04:05.999999999Z07:00", // ISO with timezone
		"2006-01-02T15:04:05Z07:00",           // ISO without fractional
		"2006-01-02",                          // date only
	}

	var lastErr error
	for _, format := range formats {
		t, err := time.Parse(format, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// toInt64 tries to convert a value to int64
func toInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	default:
		return 0, false
	}
}

// ToMismatches converts CompareResult to a list of Mismatch records
// Deduplicates mismatches by PK to handle cases where duplicate sampling may occur
// target is the table the rows were read from on the target side; it differs
// from source only when the table is mapped. Mismatches always use source
// names; target_query and correction queries use target names.
func (c *Comparator) ToMismatches(result *CompareResult, source, target types.TableMeta) []types.Mismatch {
	tableName := source.FullName()
	mapped := target.FullName() != tableName || target.ColumnMap != nil
	targetQuery := func(pk map[string]interface{}, columns []string) string {
		if !mapped {
			return ""
		}
		return target.LookupQuery(pk, columns)
	}

	var mismatches []types.Mismatch
	now := time.Now()
	genCorrection := c.options.GenerateCorrectionQueries

	// Track seen PKs to avoid duplicates (can happen when sample size > table size)
	seenPKs := make(map[string]bool)

	// Missing in target (source only)
	for _, row := range result.MissingInTarget {
		pk := extractKeyMap(row, c.keyColumns)
		pkKey := pkToString(pk)
		if seenPKs[pkKey] {
			continue
		}
		seenPKs[pkKey] = true

		m := types.Mismatch{
			Table:       tableName,
			PK:          pk,
			Type:        types.MismatchMissingTarget,
			SourceData:  row,
			Query:       types.BuildLookupQuery(tableName, pk),
			TargetQuery: targetQuery(pk, nil),
			Timestamp:   now,
		}
		if genCorrection {
			m.CorrectionQuery = target.CorrectionQuery(m.Type, pk, row, nil)
		}
		mismatches = append(mismatches, m)
	}

	// Missing in source (target only)
	for _, row := range result.MissingInSrc {
		pk := extractKeyMap(row, c.keyColumns)
		pkKey := pkToString(pk)
		if seenPKs[pkKey] {
			continue
		}
		seenPKs[pkKey] = true

		m := types.Mismatch{
			Table:       tableName,
			PK:          pk,
			Type:        types.MismatchMissingSource,
			TargetData:  row,
			Query:       types.BuildLookupQuery(tableName, pk),
			TargetQuery: targetQuery(pk, nil),
			Timestamp:   now,
		}
		if genCorrection {
			m.CorrectionQuery = target.CorrectionQuery(m.Type, pk, nil, nil)
		}
		mismatches = append(mismatches, m)
	}

	// Different data
	for _, diff := range result.Different {
		pk := extractKeyMap(diff.SourceRow, c.keyColumns)
		pkKey := pkToString(pk)
		if seenPKs[pkKey] {
			continue
		}
		seenPKs[pkKey] = true

		m := types.Mismatch{
			Table:       tableName,
			PK:          pk,
			Type:        types.MismatchDataDiff,
			DiffColumns: diff.DiffColumns,
			SourceData:  diff.SourceRow,
			TargetData:  diff.TargetRow,
			Query:       types.BuildLookupQueryWithColumns(tableName, pk, diff.DiffColumns),
			TargetQuery: targetQuery(pk, diff.DiffColumns),
			Timestamp:   now,
		}
		if genCorrection {
			m.CorrectionQuery = target.CorrectionQuery(m.Type, pk, diff.SourceRow, diff.DiffColumns)
		}
		mismatches = append(mismatches, m)
	}

	return mismatches
}

// pkToString converts a PK map to a deterministic string for deduplication
func pkToString(pk map[string]interface{}) string {
	if len(pk) == 0 {
		return ""
	}
	// For single-column PK (most common), use simple format
	if len(pk) == 1 {
		for _, v := range pk {
			return fmt.Sprintf("%v", v)
		}
	}
	// For composite PKs, sort keys and concatenate
	keys := make([]string, 0, len(pk))
	for k := range pk {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, pk[k]))
	}
	return strings.Join(parts, ",")
}

// extractKeyMap extracts key column values from a row
func extractKeyMap(row types.Row, keyColumns []string) map[string]interface{} {
	result := make(map[string]interface{}, len(keyColumns))
	for _, col := range keyColumns {
		if val, ok := row[col]; ok {
			result[col] = val
		}
	}
	return result
}

// QuickCompare does a fast comparison using only hashes
// Returns true if rows are likely equal (hashes match)
func QuickCompare(srcHash, dstHash uint64) bool {
	return srcHash == dstHash
}

// Stats returns statistics about the comparison
type Stats struct {
	TotalCompared   int
	Matched         int
	MissingInTarget int
	MissingInSource int
	Different       int
	MatchRate       float64
}

// GetStats calculates statistics from CompareResult
func GetStats(result *CompareResult) Stats {
	total := result.Matched + len(result.MissingInTarget) + len(result.MissingInSrc) + len(result.Different)

	var matchRate float64
	if total > 0 {
		matchRate = float64(result.Matched) / float64(total) * 100
	}

	return Stats{
		TotalCompared:   total,
		Matched:         result.Matched,
		MissingInTarget: len(result.MissingInTarget),
		MissingInSource: len(result.MissingInSrc),
		Different:       len(result.Different),
		MatchRate:       matchRate,
	}
}

// isZeroDatetime checks if a value represents a zero datetime
// Zero datetime values: "0000-00-00 00:00:00", "0000-00-00", time.Time zero value
func isZeroDatetime(v interface{}) bool {
	switch val := v.(type) {
	case []byte:
		s := string(val)
		return s == "0000-00-00 00:00:00" || s == "0000-00-00"
	case string:
		return val == "0000-00-00 00:00:00" || val == "0000-00-00"
	case time.Time:
		return val.IsZero()
	}
	return false
}
