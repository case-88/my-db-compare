package sampler

import (
	"testing"

	"github.com/case-88/my-db-compare/pkg/types"
)

func TestDefaultPartitionConfig(t *testing.T) {
	cfg := DefaultPartitionConfig()

	if cfg.SampleSize != 10000 {
		t.Errorf("SampleSize = %d, want 10000", cfg.SampleSize)
	}
	if cfg.MinPartitions != 10 {
		t.Errorf("MinPartitions = %d, want 10", cfg.MinPartitions)
	}
	if cfg.MaxPartitions != 300 {
		t.Errorf("MaxPartitions = %d, want 300", cfg.MaxPartitions)
	}
	if cfg.LargeTableThresh != 1000000 {
		t.Errorf("LargeTableThresh = %d, want 1000000", cfg.LargeTableThresh)
	}
}

func TestCalculatePartitionCount(t *testing.T) {
	tests := []struct {
		name          string
		cfg           PartitionConfig
		estimatedRows int64
		want          int
	}{
		{
			name: "small table - single partition",
			cfg: PartitionConfig{
				SampleSize:        1000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 500,
			want:          1, // ceil(500/1000) = 1 partition
		},
		{
			name: "small table - multiple partitions based on row count",
			cfg: PartitionConfig{
				SampleSize:        30000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 5000,
			want:          5, // ceil(5000/1000) = 5 partitions (not 30)
		},
		{
			name: "small table - exact multiple",
			cfg: PartitionConfig{
				SampleSize:        30000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 10000,
			want:          10, // ceil(10000/1000) = 10 partitions
		},
		{
			name: "medium table - proportional scaling",
			cfg: PartitionConfig{
				SampleSize:        1000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 3000,
			want:          3, // 3000 / 1000 = 3 partitions
		},
		{
			name: "small table - respects per-partition limit based on rows",
			cfg: PartitionConfig{
				SampleSize:        5000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 3000,
			want:          3, // rows(3000) < sampleSize(5000) → ceil(3000/1000) = 3
		},
		{
			name: "unknown rows - uses minPartitions",
			cfg: PartitionConfig{
				SampleSize:        1000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 0,
			want:          10, // unknown → minPartitions
		},
		{
			name: "unknown rows - respects per-partition limit",
			cfg: PartitionConfig{
				SampleSize:        5000,
				MinPartitions:     3,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 0,
			want:          5, // need 5 partitions for 5000/1000
		},
		{
			name: "large table - base calculation",
			cfg: PartitionConfig{
				SampleSize:        1000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 500000, // 500K rows, above medium threshold
			want:          100,    // sampleSize/10 = 100
		},
		{
			name: "very large table - sqrt adjustment",
			cfg: PartitionConfig{
				SampleSize:        10000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 100000000, // 100M rows
			want:          300,       // sqrt(100M/1M) = 10, capped at 300
		},
		{
			name: "medium large table with sqrt",
			cfg: PartitionConfig{
				SampleSize:        1000,
				MinPartitions:     10,
				MaxPartitions:     300,
				MaxSamplesPerPart: 1000,
				LargeTableThresh:  1000000,
				DisableRandomness: true,
			},
			estimatedRows: 4000000, // 4M rows
			want:          200,     // base=100, sqrt(4)=2, 100*2=200
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculatePartitionCount(tt.cfg, tt.estimatedRows)
			if got != tt.want {
				t.Errorf("CalculatePartitionCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCalculateSamplesPerPartition(t *testing.T) {
	tests := []struct {
		name          string
		totalSamples  int
		numPartitions int
		wantSum       int
		wantLen       int
	}{
		{
			name:          "even distribution",
			totalSamples:  100,
			numPartitions: 10,
			wantSum:       100,
			wantLen:       10,
		},
		{
			name:          "uneven distribution",
			totalSamples:  103,
			numPartitions: 10,
			wantSum:       103,
			wantLen:       10,
		},
		{
			name:          "more partitions than samples",
			totalSamples:  5,
			numPartitions: 10,
			wantSum:       5,
			wantLen:       10,
		},
		{
			name:          "zero partitions",
			totalSamples:  100,
			numPartitions: 0,
			wantSum:       0,
			wantLen:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateSamplesPerPartition(tt.totalSamples, tt.numPartitions)

			if len(got) != tt.wantLen {
				t.Errorf("len(result) = %d, want %d", len(got), tt.wantLen)
				return
			}

			sum := 0
			for _, v := range got {
				sum += v
			}
			if sum != tt.wantSum {
				t.Errorf("sum(result) = %d, want %d", sum, tt.wantSum)
			}
		})
	}
}

func TestCreateIntPartitions(t *testing.T) {
	tests := []struct {
		name          string
		minVal        int64
		maxVal        int64
		numPartitions int
		samples       []int
		wantLen       int
	}{
		{
			name:          "10 partitions over 1-100",
			minVal:        1,
			maxVal:        100,
			numPartitions: 10,
			samples:       []int{10, 10, 10, 10, 10, 10, 10, 10, 10, 10},
			wantLen:       10,
		},
		{
			name:          "single partition",
			minVal:        1,
			maxVal:        100,
			numPartitions: 1,
			samples:       []int{100},
			wantLen:       1,
		},
		{
			name:          "invalid range",
			minVal:        100,
			maxVal:        1,
			numPartitions: 10,
			samples:       nil,
			wantLen:       0,
		},
		{
			name:          "zero partitions",
			minVal:        1,
			maxVal:        100,
			numPartitions: 0,
			samples:       nil,
			wantLen:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateIntPartitions(tt.minVal, tt.maxVal, tt.numPartitions, tt.samples)

			if len(got) != tt.wantLen {
				t.Errorf("len(result) = %d, want %d", len(got), tt.wantLen)
				return
			}

			if tt.wantLen > 0 {
				// Check first partition starts at minVal
				if got[0].MinValue.(int64) != tt.minVal {
					t.Errorf("first partition MinValue = %v, want %v", got[0].MinValue, tt.minVal)
				}

				// Check last partition ends at maxVal
				if got[tt.wantLen-1].MaxValue.(int64) != tt.maxVal {
					t.Errorf("last partition MaxValue = %v, want %v", got[tt.wantLen-1].MaxValue, tt.maxVal)
				}

				// Check partitions are contiguous
				for i := 1; i < len(got); i++ {
					prevMax := got[i-1].MaxValue.(int64)
					currMin := got[i].MinValue.(int64)
					if currMin != prevMax+1 {
						t.Errorf("gap between partition %d and %d: %d -> %d", i-1, i, prevMax, currMin)
					}
				}
			}
		})
	}
}

func TestCreateStringPartitions(t *testing.T) {
	tests := []struct {
		name          string
		minVal        string
		maxVal        string
		numPartitions int
		samples       []int
		wantLen       int
	}{
		{
			name:          "a to z with 5 partitions",
			minVal:        "a",
			maxVal:        "z",
			numPartitions: 5,
			samples:       []int{20, 20, 20, 20, 20},
			wantLen:       5,
		},
		{
			name:          "empty min",
			minVal:        "",
			maxVal:        "z",
			numPartitions: 5,
			samples:       nil,
			wantLen:       0,
		},
		{
			name:          "empty max",
			minVal:        "a",
			maxVal:        "",
			numPartitions: 5,
			samples:       nil,
			wantLen:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateStringPartitions(tt.minVal, tt.maxVal, tt.numPartitions, tt.samples)

			if len(got) != tt.wantLen {
				t.Errorf("len(result) = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestCreatePartitionPlan_Int(t *testing.T) {
	table := types.TableMeta{
		Schema:        "test",
		Name:          "users",
		PKColumns:     []string{"id"},
		EstimatedRows: 100000,
	}

	cfg := PartitionConfig{
		SampleSize:        1000,
		MinPartitions:     10,
		MaxPartitions:     100,
		LargeTableThresh:  1000000,
		DisableRandomness: true,
	}

	plan, err := CreatePartitionPlan(table, "id", int64(1), int64(100000), cfg)
	if err != nil {
		t.Fatalf("CreatePartitionPlan failed: %v", err)
	}

	if plan.Table.Name != "users" {
		t.Errorf("table name = %q, want %q", plan.Table.Name, "users")
	}

	if plan.KeyColumn != "id" {
		t.Errorf("key column = %q, want %q", plan.KeyColumn, "id")
	}

	// Should have 100 partitions (1000/10 = 100)
	if len(plan.Partitions) != 100 {
		t.Errorf("partition count = %d, want 100", len(plan.Partitions))
	}

	// Total samples should equal sample size
	totalSamples := 0
	for _, p := range plan.Partitions {
		totalSamples += p.Samples
	}
	if totalSamples != cfg.SampleSize {
		t.Errorf("total samples = %d, want %d", totalSamples, cfg.SampleSize)
	}
}

func TestCreatePartitionPlan_Bytes(t *testing.T) {
	table := types.TableMeta{
		Schema:        "test",
		Name:          "users",
		PKColumns:     []string{"name"},
		EstimatedRows: 10000,
	}

	cfg := PartitionConfig{
		SampleSize:       100,
		MinPartitions:    5,
		MaxPartitions:    50,
		LargeTableThresh: 1000000,
	}

	// Use []byte for string values (as returned by MySQL driver)
	plan, err := CreatePartitionPlan(table, "name", []byte("aaa"), []byte("zzz"), cfg)
	if err != nil {
		t.Fatalf("CreatePartitionPlan with []byte failed: %v", err)
	}

	if plan == nil {
		t.Fatal("plan is nil")
	}

	if len(plan.Partitions) == 0 {
		t.Error("expected at least one partition")
	}
}

func TestCreatePartitionPlan_String(t *testing.T) {
	table := types.TableMeta{
		Schema:        "test",
		Name:          "users",
		PKColumns:     []string{"name"},
		EstimatedRows: 10000,
	}

	cfg := PartitionConfig{
		SampleSize:       100,
		MinPartitions:    5,
		MaxPartitions:    50,
		LargeTableThresh: 1000000,
	}

	plan, err := CreatePartitionPlan(table, "name", "aaa", "zzz", cfg)
	if err != nil {
		t.Fatalf("CreatePartitionPlan with string failed: %v", err)
	}

	if plan == nil {
		t.Fatal("plan is nil")
	}

	if len(plan.Partitions) == 0 {
		t.Error("expected at least one partition")
	}
}

func TestCreatePartitionPlan_NilValues(t *testing.T) {
	table := types.TableMeta{
		Schema:    "test",
		Name:      "users",
		PKColumns: []string{"id"},
	}

	cfg := DefaultPartitionConfig()

	_, err := CreatePartitionPlan(table, "id", nil, int64(100), cfg)
	if err == nil {
		t.Error("expected error for nil min value")
	}

	_, err = CreatePartitionPlan(table, "id", int64(1), nil, cfg)
	if err == nil {
		t.Error("expected error for nil max value")
	}
}

func TestCreatePartitionPlan_TypeMismatch(t *testing.T) {
	table := types.TableMeta{
		Schema:    "test",
		Name:      "users",
		PKColumns: []string{"id"},
	}

	cfg := DefaultPartitionConfig()

	_, err := CreatePartitionPlan(table, "id", int64(1), "string", cfg)
	if err == nil {
		t.Error("expected error for type mismatch")
	}
}

func TestCreatePartitionPlan_UnsupportedType(t *testing.T) {
	table := types.TableMeta{
		Schema:    "test",
		Name:      "users",
		PKColumns: []string{"id"},
	}

	cfg := DefaultPartitionConfig()

	_, err := CreatePartitionPlan(table, "id", 3.14, 6.28, cfg)
	if err == nil {
		t.Error("expected error for unsupported float type")
	}
}
