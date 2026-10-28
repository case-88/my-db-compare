package sampler

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// PartitionConfig holds partition calculation configuration
type PartitionConfig struct {
	SampleSize        int   // Total number of samples to collect
	MinPartitions     int   // Minimum number of partitions (default: 10)
	MaxPartitions     int   // Maximum number of partitions (default: 300)
	MaxSamplesPerPart int   // Maximum samples per partition (default: 1000)
	LargeTableThresh  int64 // Row count threshold for large table adjustment (default: 1M)
	DisableRandomness bool  // Disable partition count randomness (for testing)
}

// DefaultPartitionConfig returns default partition configuration
func DefaultPartitionConfig() PartitionConfig {
	return PartitionConfig{
		SampleSize:        10000,
		MinPartitions:     10,
		MaxPartitions:     300,
		MaxSamplesPerPart: 1000,    // LIMIT per partition query
		LargeTableThresh:  1000000, // 1M rows
	}
}

// Partition represents a single partition range
type Partition struct {
	Index    int         // Partition index (0-based)
	MinValue interface{} // Inclusive minimum value
	MaxValue interface{} // Inclusive maximum value
	Samples  int         // Number of samples to collect from this partition
}

// PartitionPlan holds the complete partition plan for a table
type PartitionPlan struct {
	Table       types.TableMeta // Table metadata
	KeyColumn   string          // Column used for partitioning
	Partitions  []Partition     // List of partitions
	TotalSample int             // Total samples across all partitions
}

// CalculatePartitionCount calculates the optimal number of partitions
// Algorithm considers:
// - Small tables (rows <= sampleSize): partitions based on actual row count
// - Medium tables: proportional scaling
// - Large tables: sqrt adjustment
// - Per-partition LIMIT constraint (MaxSamplesPerPart)
func CalculatePartitionCount(cfg PartitionConfig, estimatedRows int64) int {
	// Case 1: Small tables - partition based on actual table size
	if estimatedRows > 0 && estimatedRows <= int64(cfg.SampleSize) {
		if cfg.MaxSamplesPerPart > 0 {
			// ceil(estimatedRows / maxPerPart)
			partitions := (int(estimatedRows) + cfg.MaxSamplesPerPart - 1) / cfg.MaxSamplesPerPart
			if partitions < 1 {
				partitions = 1
			}
			return partitions
		}
		return 1
	}

	// For larger tables, calculate minimum partitions based on sample size
	minNeededForLimit := 1
	if cfg.MaxSamplesPerPart > 0 {
		minNeededForLimit = (cfg.SampleSize + cfg.MaxSamplesPerPart - 1) / cfg.MaxSamplesPerPart // ceil division
	}

	// Case 2: Unknown row count - use minimum needed partitions
	if estimatedRows <= 0 {
		partitions := cfg.MinPartitions
		if partitions < minNeededForLimit {
			partitions = minNeededForLimit
		}
		// Apply randomness (only increase, to avoid violating minNeededForLimit)
		if !cfg.DisableRandomness {
			partitions = applyPartitionRandomnessUp(partitions, cfg.MaxPartitions)
		}
		return partitions
	}

	// Case 3: Medium tables - scale proportionally
	// e.g., 3000 rows, sampleSize=1000 → ~3 partitions, not 100
	mediumThreshold := int64(cfg.SampleSize * cfg.MinPartitions)
	if estimatedRows < mediumThreshold {
		partitions := int(estimatedRows / int64(cfg.SampleSize))
		if partitions < 1 {
			partitions = 1
		}
		// Respect per-partition limit
		if partitions < minNeededForLimit {
			partitions = minNeededForLimit
		}
		// Apply randomness (only increase, to avoid violating minNeededForLimit)
		if !cfg.DisableRandomness {
			partitions = applyPartitionRandomnessUp(partitions, cfg.MaxPartitions)
		}
		return partitions
	}

	// Case 4: Large tables - base calculation with bounds
	basePartitions := cfg.SampleSize / 10

	partitions := basePartitions
	if partitions < cfg.MinPartitions {
		partitions = cfg.MinPartitions
	}
	if partitions > cfg.MaxPartitions {
		partitions = cfg.MaxPartitions
	}

	// Respect per-partition limit
	if partitions < minNeededForLimit {
		partitions = minNeededForLimit
	}

	// For very large tables, apply sqrt adjustment to increase partitions
	if estimatedRows > cfg.LargeTableThresh {
		scaleFactor := math.Sqrt(float64(estimatedRows) / float64(cfg.LargeTableThresh))
		adjusted := int(float64(partitions) * scaleFactor)

		if adjusted > cfg.MaxPartitions {
			adjusted = cfg.MaxPartitions
		}
		partitions = adjusted
	}

	// Apply randomness (±10%) for varied sampling across repeated runs
	if !cfg.DisableRandomness {
		partitions = applyPartitionRandomness(partitions, minNeededForLimit, cfg.MaxPartitions)
	}

	return partitions
}

// applyPartitionRandomness adds ±10% variance to partition count
// while respecting min/max bounds
func applyPartitionRandomness(partitions, minPartitions, maxPartitions int) int {
	if partitions <= 1 {
		return partitions
	}

	// ±10% variance
	variance := float64(partitions) * 0.1
	delta := (rand.Float64()*2 - 1) * variance // -variance to +variance
	adjusted := int(float64(partitions) + delta)

	// Respect bounds
	if adjusted < minPartitions {
		adjusted = minPartitions
	}
	if adjusted > maxPartitions {
		adjusted = maxPartitions
	}
	if adjusted < 1 {
		adjusted = 1
	}

	return adjusted
}

// applyPartitionRandomnessUp adds 0~+10% variance (only increase)
// Used when minNeededForLimit constraint is tight
func applyPartitionRandomnessUp(partitions, maxPartitions int) int {
	if partitions <= 1 {
		return partitions
	}

	// 0~+10% variance (only increase to avoid violating sample limits)
	variance := float64(partitions) * 0.1
	delta := rand.Float64() * variance // 0 to +variance
	adjusted := int(float64(partitions) + delta)

	// Respect max bound
	if adjusted > maxPartitions {
		adjusted = maxPartitions
	}

	return adjusted
}

// CalculateSamplesPerPartition distributes samples across partitions
// Returns slice of sample counts for each partition
func CalculateSamplesPerPartition(totalSamples, numPartitions int) []int {
	if numPartitions <= 0 {
		return nil
	}

	samples := make([]int, numPartitions)
	baseCount := totalSamples / numPartitions
	remainder := totalSamples % numPartitions

	for i := 0; i < numPartitions; i++ {
		samples[i] = baseCount
		// Distribute remainder across first partitions
		if i < remainder {
			samples[i]++
		}
	}

	return samples
}

// CreateIntPartitions creates partitions for integer key columns
func CreateIntPartitions(minVal, maxVal int64, numPartitions int, samplesPerPartition []int) []Partition {
	if numPartitions <= 0 || minVal > maxVal {
		return nil
	}

	partitions := make([]Partition, numPartitions)
	rangeSize := maxVal - minVal + 1
	partitionSize := rangeSize / int64(numPartitions)

	for i := 0; i < numPartitions; i++ {
		partitions[i].Index = i
		partitions[i].MinValue = minVal + int64(i)*partitionSize

		if i == numPartitions-1 {
			// Last partition includes all remaining values
			partitions[i].MaxValue = maxVal
		} else {
			partitions[i].MaxValue = minVal + int64(i+1)*partitionSize - 1
		}

		if i < len(samplesPerPartition) {
			partitions[i].Samples = samplesPerPartition[i]
		}
	}

	return partitions
}

// CreateUintPartitions creates partitions for unsigned integer key columns (BIGINT UNSIGNED)
func CreateUintPartitions(minVal, maxVal uint64, numPartitions int, samplesPerPartition []int) []Partition {
	if numPartitions <= 0 || minVal > maxVal {
		return nil
	}

	partitions := make([]Partition, numPartitions)
	rangeSize := maxVal - minVal + 1
	partitionSize := rangeSize / uint64(numPartitions)

	for i := 0; i < numPartitions; i++ {
		partitions[i].Index = i
		partitions[i].MinValue = minVal + uint64(i)*partitionSize

		if i == numPartitions-1 {
			// Last partition includes all remaining values
			partitions[i].MaxValue = maxVal
		} else {
			partitions[i].MaxValue = minVal + uint64(i+1)*partitionSize - 1
		}

		if i < len(samplesPerPartition) {
			partitions[i].Samples = samplesPerPartition[i]
		}
	}

	return partitions
}

// CreateTimePartitions creates partitions for time-based key columns (DATETIME, TIMESTAMP)
func CreateTimePartitions(minVal, maxVal time.Time, numPartitions int, samplesPerPartition []int) []Partition {
	if numPartitions <= 0 || minVal.After(maxVal) {
		return nil
	}

	partitions := make([]Partition, numPartitions)
	totalDuration := maxVal.Sub(minVal)
	partitionDuration := totalDuration / time.Duration(numPartitions)

	for i := 0; i < numPartitions; i++ {
		partitions[i].Index = i
		partitions[i].MinValue = minVal.Add(time.Duration(i) * partitionDuration)

		if i == numPartitions-1 {
			// Last partition includes all remaining values
			partitions[i].MaxValue = maxVal
		} else {
			partitions[i].MaxValue = minVal.Add(time.Duration(i+1) * partitionDuration).Add(-time.Nanosecond)
		}

		if i < len(samplesPerPartition) {
			partitions[i].Samples = samplesPerPartition[i]
		}
	}

	return partitions
}

// CreateStringPartitions creates partitions for string key columns
// Uses first character ranges for partitioning
func CreateStringPartitions(minVal, maxVal string, numPartitions int, samplesPerPartition []int) []Partition {
	if numPartitions <= 0 || len(minVal) == 0 || len(maxVal) == 0 {
		return nil
	}

	// For strings, use byte value of first character
	minByte := int(minVal[0])
	maxByte := int(maxVal[0])

	if minByte > maxByte {
		minByte, maxByte = maxByte, minByte
	}

	partitions := make([]Partition, numPartitions)
	rangeSize := maxByte - minByte + 1

	// If range is smaller than partitions, reduce partition count
	if rangeSize < numPartitions {
		numPartitions = rangeSize
		if numPartitions < 1 {
			numPartitions = 1
		}
		partitions = make([]Partition, numPartitions)
	}

	partitionSize := rangeSize / numPartitions

	for i := 0; i < numPartitions; i++ {
		partitions[i].Index = i
		startByte := minByte + i*partitionSize
		partitions[i].MinValue = string(rune(startByte))

		if i == numPartitions-1 {
			partitions[i].MaxValue = string(rune(maxByte + 1)) // Exclusive upper bound
		} else {
			endByte := minByte + (i+1)*partitionSize
			partitions[i].MaxValue = string(rune(endByte))
		}

		if i < len(samplesPerPartition) {
			partitions[i].Samples = samplesPerPartition[i]
		}
	}

	return partitions
}

// CreatePartitionPlan creates a complete partition plan for a table
func CreatePartitionPlan(table types.TableMeta, keyColumn string, minVal, maxVal interface{}, cfg PartitionConfig) (*PartitionPlan, error) {
	if minVal == nil || maxVal == nil {
		return nil, fmt.Errorf("min/max values cannot be nil")
	}

	// Calculate partition count
	numPartitions := CalculatePartitionCount(cfg, table.EstimatedRows)

	// Calculate samples per partition
	samplesPerPartition := CalculateSamplesPerPartition(cfg.SampleSize, numPartitions)

	// Create partitions based on value type
	var partitions []Partition

	switch min := minVal.(type) {
	case int64:
		max, ok := maxVal.(int64)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected int64")
		}
		partitions = CreateIntPartitions(min, max, numPartitions, samplesPerPartition)

	case int:
		max, ok := maxVal.(int)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected int")
		}
		partitions = CreateIntPartitions(int64(min), int64(max), numPartitions, samplesPerPartition)

	case uint64:
		max, ok := maxVal.(uint64)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected uint64")
		}
		partitions = CreateUintPartitions(min, max, numPartitions, samplesPerPartition)

	case []byte:
		maxBytes, ok := maxVal.([]byte)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected []byte")
		}
		partitions = CreateStringPartitions(string(min), string(maxBytes), numPartitions, samplesPerPartition)

	case string:
		max, ok := maxVal.(string)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected string")
		}
		partitions = CreateStringPartitions(min, max, numPartitions, samplesPerPartition)

	case time.Time:
		max, ok := maxVal.(time.Time)
		if !ok {
			return nil, fmt.Errorf("max value type mismatch: expected time.Time")
		}
		partitions = CreateTimePartitions(min, max, numPartitions, samplesPerPartition)

	default:
		return nil, fmt.Errorf("unsupported key type: %T", minVal)
	}

	if partitions == nil {
		return nil, fmt.Errorf("failed to create partitions")
	}

	return &PartitionPlan{
		Table:       table,
		KeyColumn:   keyColumn,
		Partitions:  partitions,
		TotalSample: cfg.SampleSize,
	}, nil
}
