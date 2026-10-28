package comparator

import (
	"testing"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

func TestNewComparator(t *testing.T) {
	c := NewComparator([]string{"id"})
	if c == nil {
		t.Fatal("NewComparator returned nil")
	}
	if c.hasher == nil {
		t.Error("hasher should not be nil")
	}
	if len(c.keyColumns) != 1 || c.keyColumns[0] != "id" {
		t.Errorf("keyColumns = %v, want [id]", c.keyColumns)
	}
}

func TestCompareSamples_AllMatch(t *testing.T) {
	c := NewComparator([]string{"id"})

	srcRows := []types.Row{
		{"id": int64(1), "name": "alice", "email": "alice@example.com"},
		{"id": int64(2), "name": "bob", "email": "bob@example.com"},
	}

	// Same data
	targetRows := []types.Row{
		{"id": int64(1), "name": "alice", "email": "alice@example.com"},
		{"id": int64(2), "name": "bob", "email": "bob@example.com"},
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 2 {
		t.Errorf("Matched = %d, want 2", result.Matched)
	}
	if len(result.MissingInTarget) != 0 {
		t.Errorf("MissingInTarget = %d, want 0", len(result.MissingInTarget))
	}
	if len(result.MissingInSrc) != 0 {
		t.Errorf("MissingInSrc = %d, want 0", len(result.MissingInSrc))
	}
	if len(result.Different) != 0 {
		t.Errorf("Different = %d, want 0", len(result.Different))
	}
	if result.TotalSource != 2 {
		t.Errorf("TotalSource = %d, want 2", result.TotalSource)
	}
	if result.TotalTarget != 2 {
		t.Errorf("TotalTarget = %d, want 2", result.TotalTarget)
	}
}

func TestCompareSamples_MissingInTarget(t *testing.T) {
	c := NewComparator([]string{"id"})

	srcRows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
		{"id": int64(3), "name": "carol"},
	}

	targetRows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
		// id=3 missing
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 2 {
		t.Errorf("Matched = %d, want 2", result.Matched)
	}
	if len(result.MissingInTarget) != 1 {
		t.Errorf("MissingInTarget = %d, want 1", len(result.MissingInTarget))
	}
	if len(result.MissingInTarget) > 0 {
		if result.MissingInTarget[0]["id"] != int64(3) {
			t.Errorf("MissingInTarget[0][id] = %v, want 3", result.MissingInTarget[0]["id"])
		}
	}
}

func TestCompareSamples_MissingInSrc(t *testing.T) {
	c := NewComparator([]string{"id"})

	srcRows := []types.Row{
		{"id": int64(1), "name": "alice"},
	}

	targetRows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
		{"id": int64(3), "name": "carol"},
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 1 {
		t.Errorf("Matched = %d, want 1", result.Matched)
	}
	if len(result.MissingInSrc) != 2 {
		t.Errorf("MissingInSrc = %d, want 2", len(result.MissingInSrc))
	}
}

func TestCompareSamples_Different(t *testing.T) {
	c := NewComparator([]string{"id"})

	srcRows := []types.Row{
		{"id": int64(1), "name": "alice", "status": "active"},
		{"id": int64(2), "name": "bob", "status": "active"},
	}

	targetRows := []types.Row{
		{"id": int64(1), "name": "alice", "status": "inactive"}, // status changed
		{"id": int64(2), "name": "bobby", "status": "active"},   // name changed
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 0 {
		t.Errorf("Matched = %d, want 0", result.Matched)
	}
	if len(result.Different) != 2 {
		t.Errorf("Different = %d, want 2", len(result.Different))
	}

	// Check diff columns
	for _, diff := range result.Different {
		if diff.SourceRow["id"] == int64(1) {
			found := false
			for _, col := range diff.DiffColumns {
				if col == "status" {
					found = true
					break
				}
			}
			if !found {
				t.Error("id=1: DiffColumns should contain 'status'")
			}
		}
		if diff.SourceRow["id"] == int64(2) {
			found := false
			for _, col := range diff.DiffColumns {
				if col == "name" {
					found = true
					break
				}
			}
			if !found {
				t.Error("id=2: DiffColumns should contain 'name'")
			}
		}
	}
}

func TestCompareSamples_Mixed(t *testing.T) {
	c := NewComparator([]string{"id"})

	srcRows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
		{"id": int64(3), "name": "carol"},
	}

	targetRows := []types.Row{
		{"id": int64(1), "name": "alice"}, // match
		{"id": int64(2), "name": "bobby"}, // different
		{"id": int64(4), "name": "david"}, // missing in src
		// id=3 missing in dest
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 1 {
		t.Errorf("Matched = %d, want 1", result.Matched)
	}
	if len(result.MissingInTarget) != 1 {
		t.Errorf("MissingInTarget = %d, want 1", len(result.MissingInTarget))
	}
	if len(result.MissingInSrc) != 1 {
		t.Errorf("MissingInSrc = %d, want 1", len(result.MissingInSrc))
	}
	if len(result.Different) != 1 {
		t.Errorf("Different = %d, want 1", len(result.Different))
	}
}

func TestCompareSamples_CompositeKey(t *testing.T) {
	c := NewComparator([]string{"tenant_id", "user_id"})

	srcRows := []types.Row{
		{"tenant_id": int64(1), "user_id": int64(100), "name": "alice"},
		{"tenant_id": int64(1), "user_id": int64(101), "name": "bob"},
		{"tenant_id": int64(2), "user_id": int64(100), "name": "carol"},
	}

	targetRows := []types.Row{
		{"tenant_id": int64(1), "user_id": int64(100), "name": "alice"},
		{"tenant_id": int64(1), "user_id": int64(101), "name": "bobby"}, // changed
		{"tenant_id": int64(2), "user_id": int64(100), "name": "carol"},
	}

	result := c.CompareSamples(srcRows, targetRows)

	if result.Matched != 2 {
		t.Errorf("Matched = %d, want 2", result.Matched)
	}
	if len(result.Different) != 1 {
		t.Errorf("Different = %d, want 1", len(result.Different))
	}
}

func TestCompareSamples_Empty(t *testing.T) {
	c := NewComparator([]string{"id"})

	// Both empty
	result := c.CompareSamples(nil, nil)
	if result.Matched != 0 || len(result.MissingInTarget) != 0 || len(result.MissingInSrc) != 0 {
		t.Error("comparing empty sets should return empty result")
	}

	// Source empty
	result = c.CompareSamples(nil, []types.Row{{"id": int64(1)}})
	if len(result.MissingInSrc) != 1 {
		t.Errorf("MissingInSrc = %d, want 1", len(result.MissingInSrc))
	}

	// Dest empty
	result = c.CompareSamples([]types.Row{{"id": int64(1)}}, nil)
	if len(result.MissingInTarget) != 1 {
		t.Errorf("MissingInTarget = %d, want 1", len(result.MissingInTarget))
	}
}

func TestCompareRows(t *testing.T) {
	c := NewComparator([]string{"id"})

	tests := []struct {
		name      string
		srcRow    types.Row
		targetRow types.Row
		wantDiff  []string
	}{
		{
			name:      "identical rows",
			srcRow:    types.Row{"id": int64(1), "name": "alice"},
			targetRow: types.Row{"id": int64(1), "name": "alice"},
			wantDiff:  nil,
		},
		{
			name:      "different value",
			srcRow:    types.Row{"id": int64(1), "name": "alice"},
			targetRow: types.Row{"id": int64(1), "name": "bob"},
			wantDiff:  []string{"name"},
		},
		{
			name:      "missing column in dest",
			srcRow:    types.Row{"id": int64(1), "name": "alice", "email": "a@b.com"},
			targetRow: types.Row{"id": int64(1), "name": "alice"},
			wantDiff:  []string{"email"},
		},
		{
			name:      "extra column in dest",
			srcRow:    types.Row{"id": int64(1), "name": "alice"},
			targetRow: types.Row{"id": int64(1), "name": "alice", "email": "a@b.com"},
			wantDiff:  []string{"email"},
		},
		{
			name:      "null vs value",
			srcRow:    types.Row{"id": int64(1), "name": nil},
			targetRow: types.Row{"id": int64(1), "name": "alice"},
			wantDiff:  []string{"name"},
		},
		{
			name:      "both null",
			srcRow:    types.Row{"id": int64(1), "name": nil},
			targetRow: types.Row{"id": int64(1), "name": nil},
			wantDiff:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := c.CompareRows(tt.srcRow, tt.targetRow)

			if len(tt.wantDiff) == 0 && len(diff) == 0 {
				return // OK
			}

			if len(diff) != len(tt.wantDiff) {
				t.Errorf("diff columns = %v, want %v", diff, tt.wantDiff)
				return
			}

			// Check expected columns are in diff
			for _, want := range tt.wantDiff {
				found := false
				for _, got := range diff {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected column %q in diff, got %v", want, diff)
				}
			}
		})
	}
}

func TestValuesEqual(t *testing.T) {
	now := time.Now()
	c := NewComparator([]string{"id"}) // Create comparator with default options

	tests := []struct {
		name string
		a, b interface{}
		want bool
	}{
		{"nil == nil", nil, nil, true},
		{"nil != value", nil, "test", false},
		{"value != nil", "test", nil, false},
		{"same string", "hello", "hello", true},
		{"diff string", "hello", "world", false},
		{"same int64", int64(42), int64(42), true},
		{"diff int64", int64(42), int64(43), false},
		{"int vs int64", int(42), int64(42), true},
		{"int32 vs int64", int32(42), int64(42), true},
		{"same bytes", []byte("hello"), []byte("hello"), true},
		{"diff bytes", []byte("hello"), []byte("world"), false},
		{"same time", now, now, true},
		{"same bool true", true, true, true},
		{"same bool false", false, false, true},
		{"diff bool", true, false, false},
		{"same float64", float64(3.14), float64(3.14), true},
		{"diff float64", float64(3.14), float64(2.71), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.valuesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("valuesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestValuesEqual_JSON(t *testing.T) {
	// Test with IgnoreJSONKeyOrder enabled (default)
	c := NewComparatorWithOptions([]string{"id"}, ComparatorOptions{
		IgnoreDatetimePrecision: true,
		IgnoreJSONKeyOrder:      true,
	})

	tests := []struct {
		name string
		a    interface{}
		b    interface{}
		want bool
	}{
		// JSON key order difference
		{
			"json_key_order_diff_bytes",
			[]byte(`{"shape":"circle","object":"book","color":"red"}`),
			[]byte(`{"shape":"circle","color":"red","object":"book"}`),
			true,
		},
		{
			"json_key_order_diff_string",
			`{"a":1,"b":2,"c":3}`,
			`{"c":3,"a":1,"b":2}`,
			true,
		},
		// Nested JSON
		{
			"json_nested",
			[]byte(`{"outer":{"inner":"value"},"arr":[1,2,3]}`),
			[]byte(`{"arr":[1,2,3],"outer":{"inner":"value"}}`),
			true,
		},
		// Actual value difference should still be detected
		{
			"json_value_diff",
			[]byte(`{"a":1,"b":2}`),
			[]byte(`{"a":1,"b":3}`),
			false,
		},
		// Array order matters
		{
			"json_array_order_diff",
			[]byte(`{"arr":[1,2,3]}`),
			[]byte(`{"arr":[3,2,1]}`),
			false,
		},
		// Non-JSON strings should use string comparison
		{
			"non_json_string",
			"hello world",
			"hello world",
			true,
		},
		{
			"non_json_string_diff",
			"hello",
			"world",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.valuesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("valuesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}

	// Test with IgnoreJSONKeyOrder disabled
	cNoJSON := NewComparatorWithOptions([]string{"id"}, ComparatorOptions{
		IgnoreDatetimePrecision: true,
		IgnoreJSONKeyOrder:      false,
	})

	t.Run("json_key_order_diff_when_disabled", func(t *testing.T) {
		a := []byte(`{"a":1,"b":2}`)
		b := []byte(`{"b":2,"a":1}`)
		got := cNoJSON.valuesEqual(a, b)
		if got != false {
			t.Errorf("with IgnoreJSONKeyOrder=false, valuesEqual should return false for different key orders")
		}
	})
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		wantVal int64
		wantOK  bool
	}{
		{"int", int(42), 42, true},
		{"int8", int8(42), 42, true},
		{"int16", int16(42), 42, true},
		{"int32", int32(42), 42, true},
		{"int64", int64(42), 42, true},
		{"uint", uint(42), 42, true},
		{"uint8", uint8(42), 42, true},
		{"uint16", uint16(42), 42, true},
		{"uint32", uint32(42), 42, true},
		{"uint64", uint64(42), 42, true},
		{"string", "42", 0, false},
		{"float", float64(42.0), 0, false},
		{"nil", nil, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVal, gotOK := toInt64(tt.input)
			if gotVal != tt.wantVal || gotOK != tt.wantOK {
				t.Errorf("toInt64(%v) = (%d, %v), want (%d, %v)",
					tt.input, gotVal, gotOK, tt.wantVal, tt.wantOK)
			}
		})
	}
}

func TestToMismatches(t *testing.T) {
	c := NewComparator([]string{"id"})

	result := &CompareResult{
		MissingInTarget: []types.Row{
			{"id": int64(1), "name": "alice"},
		},
		MissingInSrc: []types.Row{
			{"id": int64(2), "name": "bob"},
		},
		Different: []DiffPair{
			{
				SourceRow:   types.Row{"id": int64(3), "name": "carol"},
				TargetRow:   types.Row{"id": int64(3), "name": "caroline"},
				DiffColumns: []string{"name"},
			},
		},
		Matched:     5,
		TotalSource: 7,
		TotalTarget: 7,
	}

	table := types.TableMeta{Schema: "testdb", Name: "users"}
	mismatches := c.ToMismatches(result, table, table)

	if len(mismatches) != 3 {
		t.Errorf("len(mismatches) = %d, want 3", len(mismatches))
	}

	// Check types
	typeCount := make(map[types.MismatchType]int)
	for _, m := range mismatches {
		typeCount[m.Type]++
		if m.Table != "testdb.users" {
			t.Errorf("Table = %s, want testdb.users", m.Table)
		}
		if m.PK == nil {
			t.Error("PK should not be nil")
		}
		if m.Timestamp.IsZero() {
			t.Error("Timestamp should be set")
		}
		// Check Query field is generated
		if m.Query == "" {
			t.Error("Query should not be empty")
		}
		if !contains(m.Query, "SELECT") {
			t.Errorf("Query should contain SELECT statement, got: %s", m.Query)
		}
		if !contains(m.Query, "`testdb`.`users`") {
			t.Errorf("Query should contain table name, got: %s", m.Query)
		}
	}

	if typeCount[types.MismatchMissingTarget] != 1 {
		t.Errorf("MissingDest count = %d, want 1", typeCount[types.MismatchMissingTarget])
	}
	if typeCount[types.MismatchMissingSource] != 1 {
		t.Errorf("MissingSource count = %d, want 1", typeCount[types.MismatchMissingSource])
	}
	if typeCount[types.MismatchDataDiff] != 1 {
		t.Errorf("DataDiff count = %d, want 1", typeCount[types.MismatchDataDiff])
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestExtractKeyMap(t *testing.T) {
	row := types.Row{
		"id":    int64(1),
		"name":  "alice",
		"email": "alice@example.com",
	}

	// Single key
	pk := extractKeyMap(row, []string{"id"})
	if len(pk) != 1 {
		t.Errorf("len(pk) = %d, want 1", len(pk))
	}
	if pk["id"] != int64(1) {
		t.Errorf("pk[id] = %v, want 1", pk["id"])
	}

	// Composite key
	pk = extractKeyMap(row, []string{"id", "name"})
	if len(pk) != 2 {
		t.Errorf("len(pk) = %d, want 2", len(pk))
	}

	// Missing key column
	pk = extractKeyMap(row, []string{"id", "nonexistent"})
	if len(pk) != 1 {
		t.Errorf("len(pk) = %d, want 1 (missing column ignored)", len(pk))
	}
}

func TestQuickCompare(t *testing.T) {
	if !QuickCompare(123, 123) {
		t.Error("same hashes should return true")
	}
	if QuickCompare(123, 456) {
		t.Error("different hashes should return false")
	}
}

func TestGetStats(t *testing.T) {
	result := &CompareResult{
		MissingInTarget: []types.Row{
			{"id": int64(1)},
			{"id": int64(2)},
		},
		MissingInSrc: []types.Row{
			{"id": int64(3)},
		},
		Different: []DiffPair{
			{SourceRow: types.Row{"id": int64(4)}},
		},
		Matched:     6,
		TotalSource: 9,
		TotalTarget: 8,
	}

	stats := GetStats(result)

	if stats.TotalCompared != 10 {
		t.Errorf("TotalCompared = %d, want 10", stats.TotalCompared)
	}
	if stats.Matched != 6 {
		t.Errorf("Matched = %d, want 6", stats.Matched)
	}
	if stats.MissingInTarget != 2 {
		t.Errorf("MissingInTarget = %d, want 2", stats.MissingInTarget)
	}
	if stats.MissingInSource != 1 {
		t.Errorf("MissingInSource = %d, want 1", stats.MissingInSource)
	}
	if stats.Different != 1 {
		t.Errorf("Different = %d, want 1", stats.Different)
	}
	if stats.MatchRate != 60.0 {
		t.Errorf("MatchRate = %f, want 60.0", stats.MatchRate)
	}
}

func TestGetStats_Empty(t *testing.T) {
	result := &CompareResult{}
	stats := GetStats(result)

	if stats.TotalCompared != 0 {
		t.Errorf("TotalCompared = %d, want 0", stats.TotalCompared)
	}
	if stats.MatchRate != 0 {
		t.Errorf("MatchRate = %f, want 0", stats.MatchRate)
	}
}

func BenchmarkCompareSamples(b *testing.B) {
	c := NewComparator([]string{"id"})

	// Create 1000 rows
	srcRows := make([]types.Row, 1000)
	targetRows := make([]types.Row, 1000)

	for i := 0; i < 1000; i++ {
		srcRows[i] = types.Row{
			"id":    int64(i),
			"name":  "user",
			"email": "user@example.com",
		}
		// 90% match, 10% different
		if i%10 == 0 {
			targetRows[i] = types.Row{
				"id":    int64(i),
				"name":  "different",
				"email": "different@example.com",
			}
		} else {
			targetRows[i] = types.Row{
				"id":    int64(i),
				"name":  "user",
				"email": "user@example.com",
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.CompareSamples(srcRows, targetRows)
	}
}

// Tests for datetime comparison optimizations

func TestLooksLikeDatetime(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"2024-01-15", true},
		{"2024-01-15 10:30:00", true},
		{"2024-01-15T10:30:00", true},
		{"2024-01-15T10:30:00Z", true},
		{"2024-01-15 10:30:00.123456", true},
		{"hello world", false},
		{"12345", false},
		{"2024/01/15", false}, // Wrong separator
		{"", false},
		{"short", false},
		{"20240115", false},  // No hyphens
		{"2024-1-15", false}, // Single digit month (doesn't match pattern)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeDatetime(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeDatetime(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDetectDatetimeFormat(t *testing.T) {
	tests := []struct {
		input    string
		wantFmt  string
		wantDesc string
	}{
		{"2024-01-15", "2006-01-02", "date only"},
		{"2024-01-15 10:30:00", "2006-01-02 15:04:05", "datetime without fraction"},
		{"2024-01-15T10:30:00", "2006-01-02T15:04:05", "datetime T separator"},
		{"2024-01-15T10:30:00Z", "2006-01-02T15:04:05Z", "datetime with Z"},
		{"2024-01-15 10:30:00.123", "2006-01-02 15:04:05.999", "datetime with milliseconds"},
		{"2024-01-15 10:30:00.123456", "2006-01-02 15:04:05.999999", "datetime with microseconds"},
		{"hello", "", "non-datetime string"},
	}

	for _, tt := range tests {
		t.Run(tt.wantDesc, func(t *testing.T) {
			got := detectDatetimeFormat(tt.input)
			if got != tt.wantFmt {
				t.Errorf("detectDatetimeFormat(%q) = %q, want %q", tt.input, got, tt.wantFmt)
			}
		})
	}
}

func TestTimesEqualAsStrings_EarlyTermination(t *testing.T) {
	// Test that non-datetime strings are rejected early
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"both too short", "abc", "xyz", false},
		{"a too short", "abc", "2024-01-15 10:30:00", false},
		{"b too short", "2024-01-15 10:30:00", "abc", false},
		{"non-datetime strings", "hello world!", "goodbye world", false},
		{"same datetime", "2024-01-15 10:30:00", "2024-01-15 10:30:00", true},
		{"datetime(0) vs datetime(6)", "2024-01-15 10:30:00", "2024-01-15 10:30:00.000000", true},
		{"datetime(6) vs datetime(0)", "2024-01-15 10:30:00.123456", "2024-01-15 10:30:00", true},
		{"different times", "2024-01-15 10:30:00", "2024-01-15 10:30:01", false},
		{"different dates", "2024-01-15 10:30:00", "2024-01-16 10:30:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timesEqualAsStrings(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("timesEqualAsStrings(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestJsonEqual_EarlyTermination(t *testing.T) {
	tests := []struct {
		name string
		a    []byte
		b    []byte
		want bool
	}{
		// Early termination cases
		{"empty a", []byte{}, []byte(`{"a":1}`), false},
		{"empty b", []byte(`{"a":1}`), []byte{}, false},
		{"a not json", []byte("hello"), []byte(`{"a":1}`), false},
		{"b not json", []byte(`{"a":1}`), []byte("hello"), false},
		{"bracket mismatch object vs array", []byte(`{"a":1}`), []byte(`[1,2,3]`), false},
		{"bracket mismatch array vs object", []byte(`[1,2,3]`), []byte(`{"a":1}`), false},

		// Significant length difference (early termination)
		{"length diff too large", []byte(`{"a":1}`), []byte(`{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"i":9,"j":10,"k":11,"l":12}`), false},

		// Normal comparison cases
		{"same object", []byte(`{"a":1,"b":2}`), []byte(`{"a":1,"b":2}`), true},
		{"same object different order", []byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`), true},
		{"same array", []byte(`[1,2,3]`), []byte(`[1,2,3]`), true},
		{"different values", []byte(`{"a":1}`), []byte(`{"a":2}`), false},
		{"different array order", []byte(`[1,2,3]`), []byte(`[3,2,1]`), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("jsonEqual(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsDigit(t *testing.T) {
	for b := byte(0); b < 255; b++ {
		want := b >= '0' && b <= '9'
		got := isDigit(b)
		if got != want {
			t.Errorf("isDigit(%d) = %v, want %v", b, got, want)
		}
	}
}

func BenchmarkTimesEqualAsStrings_NonDatetime(b *testing.B) {
	// Test early termination performance for non-datetime strings
	a := "hello world this is not a datetime"
	c := "goodbye world this is also not"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		timesEqualAsStrings(a, c)
	}
}

func BenchmarkTimesEqualAsStrings_Datetime(b *testing.B) {
	// Test datetime comparison with format detection
	a := "2024-01-15 10:30:00"
	c := "2024-01-15 10:30:00.000000"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		timesEqualAsStrings(a, c)
	}
}

func BenchmarkJsonEqual_NonJson(b *testing.B) {
	// Test early termination for non-JSON strings
	a := []byte("hello world")
	c := []byte("goodbye world")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jsonEqual(a, c)
	}
}

func BenchmarkJsonEqual_SmallJson(b *testing.B) {
	// Test JSON comparison with key order difference
	a := []byte(`{"a":1,"b":2,"c":3}`)
	c := []byte(`{"c":3,"b":2,"a":1}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jsonEqual(a, c)
	}
}

func TestToMismatches_Mapped(t *testing.T) {
	c := NewComparatorWithOptions([]string{"id"}, ComparatorOptions{GenerateCorrectionQueries: true})
	result := &CompareResult{
		Different: []DiffPair{{
			SourceRow:   types.Row{"id": int64(3), "name": "carol"},
			TargetRow:   types.Row{"id": int64(3), "name": "caroline"},
			DiffColumns: []string{"name"},
		}},
	}
	source := types.TableMeta{Schema: "app", Name: "users"}
	target := types.TableMeta{
		Schema:    "app_v2",
		Name:      "members",
		ColumnMap: map[string]string{"id": "member_id", "name": "full_name"},
	}

	mismatches := c.ToMismatches(result, source, target)
	if len(mismatches) != 1 {
		t.Fatalf("len(mismatches) = %d, want 1", len(mismatches))
	}
	m := mismatches[0]

	if m.Table != "app.users" {
		t.Errorf("Table = %s, want app.users", m.Table)
	}
	if want := "SELECT `id`, `name` FROM `app`.`users` WHERE `id` = 3;"; m.Query != want {
		t.Errorf("Query = %s, want %s", m.Query, want)
	}
	if want := "SELECT `member_id` AS `id`, `full_name` AS `name` FROM `app_v2`.`members` WHERE `member_id` = 3;"; m.TargetQuery != want {
		t.Errorf("TargetQuery = %s, want %s", m.TargetQuery, want)
	}
	if want := "UPDATE `app_v2`.`members` SET `full_name` = 'carol' WHERE `member_id` = 3;"; m.CorrectionQuery != want {
		t.Errorf("CorrectionQuery = %s, want %s", m.CorrectionQuery, want)
	}

	// Unmapped tables leave TargetQuery empty
	if got := c.ToMismatches(result, source, source)[0].TargetQuery; got != "" {
		t.Errorf("unmapped TargetQuery = %q, want empty", got)
	}
}
