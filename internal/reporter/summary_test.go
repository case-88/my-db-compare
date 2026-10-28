package reporter

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSummary(t *testing.T) {
	s := NewSummary()
	assert.NotNil(t, s)
	assert.False(t, s.StartTime.IsZero())
	assert.True(t, s.EndTime.IsZero())
}

func TestSummary_AddResult(t *testing.T) {
	s := NewSummary()

	// Add compared table
	s.AddResult(&TableResult{
		Table:           "testdb.users",
		Sampled:         100,
		Matched:         95,
		MissingInTarget: 3,
		Different:       2,
		Verified:        5,
		Resolved:        0,
	})

	assert.Equal(t, 1, s.TotalTables)
	assert.Equal(t, 1, s.TablesCompared)
	assert.Equal(t, 0, s.TablesSkipped)
	assert.Equal(t, 100, s.TotalSampled)
	assert.Equal(t, 95, s.TotalMatched)
	assert.Equal(t, 5, s.TotalMismatched)
	assert.Equal(t, 5, s.TotalVerified)

	// Add skipped table
	s.AddResult(&TableResult{
		Table:      "testdb.logs",
		Skipped:    true,
		SkipReason: "no primary key",
	})

	assert.Equal(t, 2, s.TotalTables)
	assert.Equal(t, 1, s.TablesCompared)
	assert.Equal(t, 1, s.TablesSkipped)
}

func TestSummary_MatchRate(t *testing.T) {
	s := NewSummary()

	// No samples yet
	assert.Equal(t, 0.0, s.MatchRate())

	s.AddResult(&TableResult{
		Sampled: 100,
		Matched: 90,
	})

	assert.Equal(t, 90.0, s.MatchRate())
}

func TestSummary_HasMismatches(t *testing.T) {
	s := NewSummary()
	assert.False(t, s.HasMismatches())

	s.AddResult(&TableResult{
		Sampled:  100,
		Matched:  100,
		Verified: 0,
	})
	assert.False(t, s.HasMismatches())

	s.AddResult(&TableResult{
		Sampled:  100,
		Matched:  95,
		Verified: 5,
	})
	assert.True(t, s.HasMismatches())
}

func TestSummary_Duration(t *testing.T) {
	s := NewSummary()

	// Before finish - duration should be >= 0 (can be 0 on fast machines)
	assert.GreaterOrEqual(t, s.Duration(), time.Duration(0))

	time.Sleep(20 * time.Millisecond)
	s.Finish()

	// After finish
	d := s.Duration()
	assert.Greater(t, d, 10*time.Millisecond)

	// Duration should be fixed after Finish
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, d, s.Duration())
}

func TestSummary_Print(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{
		Table:           "testdb.users",
		Sampled:         1000,
		Matched:         990,
		MissingInTarget: 5,
		Different:       5,
		Verified:        10,
		Duration:        time.Second,
	})
	s.AddResult(&TableResult{
		Table:      "testdb.logs",
		Skipped:    true,
		SkipReason: "no primary key",
	})
	s.Finish()

	var buf bytes.Buffer
	s.Fprint(&buf)

	output := buf.String()
	assert.Contains(t, output, "COMPARISON SUMMARY")
	assert.Contains(t, output, "testdb.users")
	assert.Contains(t, output, "1000")
	assert.Contains(t, output, "INCOMPLETE")
}

func TestSummary_PrintAllMatched(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{
		Table:    "testdb.users",
		Sampled:  100,
		Matched:  100,
		Verified: 0,
	})
	s.Finish()

	var buf bytes.Buffer
	s.Fprint(&buf)

	output := buf.String()
	assert.Contains(t, output, "SOURCE ROWS MATCHED")
	assert.Contains(t, output, "Target-only rows: NOT CHECKED")
}

func TestSummary_PrintSkipped(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{
		Table:      "testdb.logs",
		Skipped:    true,
		SkipReason: "no primary key",
	})
	s.AddResult(&TableResult{
		Table:      "testdb.temp",
		Skipped:    true,
		SkipReason: "excluded by filter",
	})

	var buf bytes.Buffer
	s.PrintSkipped(&buf)

	output := buf.String()
	assert.Contains(t, output, "Skipped Tables")
	assert.Contains(t, output, "testdb.logs")
	assert.Contains(t, output, "no primary key")
}

func TestSummary_ToJSON(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{
		Table:    "testdb.users",
		Sampled:  100,
		Matched:  95,
		Verified: 5,
	})
	s.Finish()

	data, err := s.ToJSON()
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, float64(1), result["tables_compared"])
	assert.Equal(t, float64(100), result["total_sampled"])
	assert.Equal(t, float64(95), result["total_matched"])
	assert.Equal(t, true, result["has_mismatches"])
}

func TestSummary_SaveJSON(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{Table: "test", Sampled: 10, Matched: 10})
	s.Finish()

	tmpDir := t.TempDir()
	err := s.SaveJSON(tmpDir)
	require.NoError(t, err)
}

func TestSummary_GetMismatchedTables(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{Table: "t1", Verified: 10})
	s.AddResult(&TableResult{Table: "t2", Verified: 0})
	s.AddResult(&TableResult{Table: "t3", Verified: 5})
	s.AddResult(&TableResult{Table: "t4", Skipped: true})

	mismatched := s.GetMismatchedTables()

	assert.Len(t, mismatched, 2)
	assert.Equal(t, "t1", mismatched[0].Table) // Sorted by verified desc
	assert.Equal(t, "t3", mismatched[1].Table)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{500 * time.Millisecond, "500ms"},
		{5 * time.Second, "5s"},
		{90 * time.Second, "1.5m"},
		{2 * time.Hour, "2.0h"},
	}

	for _, tt := range tests {
		result := FormatDuration(tt.d)
		assert.Equal(t, tt.expected, result)
	}
}

func TestProgressBar(t *testing.T) {
	tests := []struct {
		current  int
		total    int
		width    int
		expected string
	}{
		{0, 100, 10, "░░░░░░░░░░"},
		{50, 100, 10, "█████░░░░░"},
		{100, 100, 10, "██████████"},
		{0, 0, 10, "░░░░░░░░░░"},
	}

	for _, tt := range tests {
		result := ProgressBar(tt.current, tt.total, tt.width)
		assert.Equal(t, tt.expected, result)
	}
}
