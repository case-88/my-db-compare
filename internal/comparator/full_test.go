package comparator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultFullCompareConfig(t *testing.T) {
	cfg := DefaultFullCompareConfig()

	assert.Equal(t, 2000, cfg.ChunkSize)
	assert.Equal(t, 10, cfg.DelayMs)
	assert.Equal(t, 4, cfg.Parallelism)
}

func TestFullCompareProgress_GetProgress(t *testing.T) {
	t.Run("empty progress", func(t *testing.T) {
		progress := &FullCompareProgress{
			Table:     "test.users",
			TotalRows: 1000,
			StartTime: time.Now(),
		}

		processed, total, mismatches, elapsed, remaining := progress.GetProgress()

		assert.Equal(t, int64(0), processed)
		assert.Equal(t, int64(1000), total)
		assert.Equal(t, int64(0), mismatches)
		assert.True(t, elapsed >= 0)
		assert.Equal(t, time.Duration(0), remaining)
	})

	t.Run("partial progress", func(t *testing.T) {
		progress := &FullCompareProgress{
			Table:         "test.users",
			TotalRows:     1000,
			ProcessedRows: 500,
			MismatchCount: 5,
			StartTime:     time.Now().Add(-1 * time.Minute),
		}

		processed, total, mismatches, elapsed, remaining := progress.GetProgress()

		assert.Equal(t, int64(500), processed)
		assert.Equal(t, int64(1000), total)
		assert.Equal(t, int64(5), mismatches)
		assert.True(t, elapsed >= 1*time.Minute)
		assert.True(t, remaining > 0) // Should estimate remaining time
	})
}

func TestCreatePartitions_Int64(t *testing.T) {
	fc := &FullComparer{
		config: FullCompareConfig{
			Parallelism: 4,
		},
	}

	partitions := fc.createPartitions(int64(1), int64(100), 4)

	assert.Len(t, partitions, 4)
	assert.Equal(t, int64(1), partitions[0].MinValue)
	assert.Equal(t, int64(100), partitions[3].MaxValue)

	// Check non-overlapping ranges
	for i := 1; i < len(partitions); i++ {
		assert.True(t, partitions[i].MinValue.(int64) > partitions[i-1].MinValue.(int64))
	}
}

func TestCreatePartitions_Uint64(t *testing.T) {
	fc := &FullComparer{
		config: FullCompareConfig{
			Parallelism: 2,
		},
	}

	partitions := fc.createPartitions(uint64(0), uint64(1000), 2)

	assert.Len(t, partitions, 2)
	assert.Equal(t, uint64(0), partitions[0].MinValue)
	assert.Equal(t, uint64(1000), partitions[1].MaxValue)
}

func TestCreatePartitions_NonNumeric(t *testing.T) {
	fc := &FullComparer{
		config: FullCompareConfig{
			Parallelism: 4,
		},
	}

	// String PK - should return single partition
	partitions := fc.createPartitions("aaa", "zzz", 4)

	assert.Len(t, partitions, 1)
	assert.Equal(t, "aaa", partitions[0].MinValue)
	assert.Equal(t, "zzz", partitions[0].MaxValue)
}

func TestCreatePartitions_SinglePartition(t *testing.T) {
	fc := &FullComparer{
		config: FullCompareConfig{
			Parallelism: 1,
		},
	}

	partitions := fc.createPartitions(int64(1), int64(100), 1)

	assert.Len(t, partitions, 1)
	assert.Equal(t, int64(1), partitions[0].MinValue)
	assert.Equal(t, int64(100), partitions[0].MaxValue)
}

func TestCreatePartitions_ZeroCount(t *testing.T) {
	fc := &FullComparer{
		config: FullCompareConfig{
			Parallelism: 1,
		},
	}

	// Should default to 1 partition
	partitions := fc.createPartitions(int64(1), int64(100), 0)

	assert.Len(t, partitions, 1)
}

func TestIncrementKey(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{"int64", int64(10), int64(11)},
		{"uint64", uint64(10), uint64(11)},
		{"int", int(10), int(11)},
		{"int32", int32(10), int32(11)},
		{"uint32", uint32(10), uint32(11)},
		{"string", "abc", "abc"}, // Non-numeric returns same value
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := incrementKey(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildColumnListForQuery(t *testing.T) {
	t.Run("empty columns returns *", func(t *testing.T) {
		result := buildColumnListForQuery(nil)
		assert.Equal(t, "*", result)

		result = buildColumnListForQuery([]string{})
		assert.Equal(t, "*", result)
	})

	t.Run("single column", func(t *testing.T) {
		result := buildColumnListForQuery([]string{"id"})
		assert.Equal(t, "`id`", result)
	})

	t.Run("multiple columns", func(t *testing.T) {
		result := buildColumnListForQuery([]string{"id", "name", "email"})
		assert.Equal(t, "`id`, `name`, `email`", result)
	})
}

func TestJoinStrings(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		result := joinStrings([]string{}, ", ")
		assert.Equal(t, "", result)
	})

	t.Run("single element", func(t *testing.T) {
		result := joinStrings([]string{"a"}, ", ")
		assert.Equal(t, "a", result)
	})

	t.Run("multiple elements", func(t *testing.T) {
		result := joinStrings([]string{"a", "b", "c"}, ", ")
		assert.Equal(t, "a, b, c", result)
	})
}

func TestRepeatString(t *testing.T) {
	t.Run("zero repeats", func(t *testing.T) {
		result := repeatString("?", 0, ", ")
		assert.Equal(t, "", result)
	})

	t.Run("single repeat", func(t *testing.T) {
		result := repeatString("?", 1, ", ")
		assert.Equal(t, "?", result)
	})

	t.Run("multiple repeats", func(t *testing.T) {
		result := repeatString("?", 3, ", ")
		assert.Equal(t, "?, ?, ?", result)
	})
}

func TestFullCompareResult(t *testing.T) {
	result := &FullCompareResult{
		Table:           "test.users",
		TotalRows:       1000,
		ProcessedRows:   1000,
		MatchCount:      990,
		MismatchCount:   10,
		MissingInTarget: 3,
		MissingInSource: 2,
		Different:       5,
		Duration:        5 * time.Second,
	}

	assert.Equal(t, "test.users", result.Table)
	assert.Equal(t, int64(1000), result.TotalRows)
	assert.Equal(t, int64(1000), result.ProcessedRows)
	assert.Equal(t, int64(990), result.MatchCount)
	assert.Equal(t, int64(10), result.MismatchCount)
	assert.Equal(t, int64(3), result.MissingInTarget)
	assert.Equal(t, int64(2), result.MissingInSource)
	assert.Equal(t, int64(5), result.Different)
}

// Partition ranges are inclusive on both ends, so they must not share a key.
// Otherwise the boundary row is compared and reported twice.
func TestCreatePartitions_NoSharedBoundary(t *testing.T) {
	fc := &FullComparer{}

	t.Run("int64", func(t *testing.T) {
		parts := fc.createPartitions(int64(1), int64(100), 4)
		for i := 1; i < len(parts); i++ {
			assert.Equal(t, parts[i-1].MaxValue.(int64)+1, parts[i].MinValue.(int64), "partition %d", i)
		}
	})

	t.Run("uint64", func(t *testing.T) {
		parts := fc.createPartitions(uint64(0), uint64(1000), 2)
		assert.Equal(t, parts[0].MaxValue.(uint64)+1, parts[1].MinValue.(uint64))
	})

	t.Run("fewer keys than partitions uses one partition", func(t *testing.T) {
		parts := fc.createPartitions(uint64(0), uint64(2), 4)
		assert.Equal(t, []PartitionRange{{Index: 0, MinValue: uint64(0), MaxValue: uint64(2)}}, parts)

		parts = fc.createPartitions(int64(5), int64(5), 4)
		assert.Equal(t, []PartitionRange{{Index: 0, MinValue: int64(5), MaxValue: int64(5)}}, parts)
	})
}
