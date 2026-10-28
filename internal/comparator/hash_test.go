package comparator

import (
	"testing"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
)

func TestNewHasher(t *testing.T) {
	h := NewHasher()
	if h == nil {
		t.Fatal("NewHasher returned nil")
	}
	if !h.SortColumns {
		t.Error("SortColumns should be true by default")
	}
}

func TestHashRow_Empty(t *testing.T) {
	h := NewHasher()
	hash := h.HashRow(types.Row{})
	if hash != 0 {
		t.Errorf("empty row hash = %d, want 0", hash)
	}

	hash = h.HashRow(nil)
	if hash != 0 {
		t.Errorf("nil row hash = %d, want 0", hash)
	}
}

func TestHashRow_Deterministic(t *testing.T) {
	h := NewHasher()

	row := types.Row{
		"id":    int64(1),
		"name":  "test",
		"email": "test@example.com",
	}

	hash1 := h.HashRow(row)
	hash2 := h.HashRow(row)

	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %d != %d", hash1, hash2)
	}
}

func TestHashRow_ColumnOrder(t *testing.T) {
	h := NewHasher()
	h.SortColumns = true

	// Same data, different insert order
	row1 := types.Row{
		"a": "1",
		"b": "2",
		"c": "3",
	}

	row2 := types.Row{
		"c": "3",
		"a": "1",
		"b": "2",
	}

	hash1 := h.HashRow(row1)
	hash2 := h.HashRow(row2)

	if hash1 != hash2 {
		t.Errorf("hash should be same regardless of column order: %d != %d", hash1, hash2)
	}
}

func TestHashRow_DifferentValues(t *testing.T) {
	h := NewHasher()

	row1 := types.Row{"id": int64(1), "name": "alice"}
	row2 := types.Row{"id": int64(1), "name": "bob"}

	hash1 := h.HashRow(row1)
	hash2 := h.HashRow(row2)

	if hash1 == hash2 {
		t.Error("different rows should have different hashes")
	}
}

func TestHashRow_NullHandling(t *testing.T) {
	h := NewHasher()

	// NULL value
	row1 := types.Row{"id": int64(1), "name": nil}

	// Empty string
	row2 := types.Row{"id": int64(1), "name": ""}

	hash1 := h.HashRow(row1)
	hash2 := h.HashRow(row2)

	if hash1 == hash2 {
		t.Error("NULL and empty string should have different hashes")
	}
}

func TestHashRow_NilBytes(t *testing.T) {
	h := NewHasher()

	// nil []byte
	row1 := types.Row{"id": int64(1), "data": []byte(nil)}

	// empty []byte
	row2 := types.Row{"id": int64(1), "data": []byte{}}

	hash1 := h.HashRow(row1)
	hash2 := h.HashRow(row2)

	// nil []byte is treated as NULL marker, empty []byte is empty
	if hash1 == hash2 {
		t.Error("nil []byte and empty []byte should have different hashes")
	}
}

func TestHashRow_TypeVariants(t *testing.T) {
	h := NewHasher()

	tests := []struct {
		name string
		row  types.Row
	}{
		{"int", types.Row{"v": int(42)}},
		{"int8", types.Row{"v": int8(42)}},
		{"int16", types.Row{"v": int16(42)}},
		{"int32", types.Row{"v": int32(42)}},
		{"int64", types.Row{"v": int64(42)}},
		{"uint", types.Row{"v": uint(42)}},
		{"uint8", types.Row{"v": uint8(42)}},
		{"uint16", types.Row{"v": uint16(42)}},
		{"uint32", types.Row{"v": uint32(42)}},
		{"uint64", types.Row{"v": uint64(42)}},
		{"float32", types.Row{"v": float32(3.14)}},
		{"float64", types.Row{"v": float64(3.14)}},
		{"bool_true", types.Row{"v": true}},
		{"bool_false", types.Row{"v": false}},
		{"string", types.Row{"v": "hello"}},
		{"bytes", types.Row{"v": []byte("hello")}},
		{"time", types.Row{"v": time.Now()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := h.HashRow(tt.row)
			if hash == 0 {
				t.Errorf("hash for %s should not be 0", tt.name)
			}

			// Verify determinism
			hash2 := h.HashRow(tt.row)
			if hash != hash2 {
				t.Errorf("hash not deterministic for %s: %d != %d", tt.name, hash, hash2)
			}
		})
	}
}

func TestHashRowColumns(t *testing.T) {
	h := NewHasher()

	row := types.Row{
		"id":    int64(1),
		"name":  "test",
		"email": "test@example.com",
	}

	// Hash only specific columns
	hash1 := h.HashRowColumns(row, []string{"id", "name"})
	hash2 := h.HashRowColumns(row, []string{"id", "email"})

	if hash1 == hash2 {
		t.Error("different column sets should produce different hashes")
	}
}

func TestHashRowColumns_MissingColumn(t *testing.T) {
	h := NewHasher()

	row := types.Row{
		"id":   int64(1),
		"name": "test",
	}

	// Request a column that doesn't exist
	hash := h.HashRowColumns(row, []string{"id", "nonexistent"})

	// Should not panic and should produce a hash
	if hash == 0 {
		t.Error("hash should be non-zero even with missing column")
	}
}

func TestHashKey(t *testing.T) {
	h := NewHasher()

	row := types.Row{
		"id":    int64(1),
		"name":  "test",
		"email": "test@example.com",
	}

	keyHash := h.HashKey(row, []string{"id"})
	if keyHash == 0 {
		t.Error("key hash should not be 0")
	}

	// Same key, different non-key values
	row2 := types.Row{
		"id":    int64(1),
		"name":  "different",
		"email": "different@example.com",
	}

	keyHash2 := h.HashKey(row2, []string{"id"})

	if keyHash != keyHash2 {
		t.Error("same key should produce same key hash")
	}
}

func TestHashRows(t *testing.T) {
	h := NewHasher()

	rows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
		{"id": int64(3), "name": "carol"},
	}

	result := h.HashRows(rows, []string{"id"})

	if len(result) != 3 {
		t.Errorf("expected 3 results, got %d", len(result))
	}

	// Check each result has both hashes
	for i, rh := range result {
		if rh.Hash == 0 {
			t.Errorf("row %d: hash should not be 0", i)
		}
		if rh.KeyHash == 0 {
			t.Errorf("row %d: key hash should not be 0", i)
		}
		if rh.Row == nil {
			t.Errorf("row %d: row should not be nil", i)
		}
	}

	// Verify different rows have different hashes
	if result[0].Hash == result[1].Hash {
		t.Error("different rows should have different hashes")
	}
}

func TestBuildHashMap(t *testing.T) {
	h := NewHasher()

	rows := []types.Row{
		{"id": int64(1), "name": "alice"},
		{"id": int64(2), "name": "bob"},
	}

	hashMap := h.BuildHashMap(rows, []string{"id"})

	if len(hashMap) != 2 {
		t.Errorf("expected 2 entries in hash map, got %d", len(hashMap))
	}

	// Look up by key hash
	for keyHash, rowHash := range hashMap {
		if keyHash == 0 {
			t.Error("key hash should not be 0")
		}
		if rowHash.Row == nil {
			t.Error("row should not be nil")
		}
	}
}

func BenchmarkHashRow(b *testing.B) {
	h := NewHasher()
	row := types.Row{
		"id":         int64(12345),
		"name":       "benchmark user",
		"email":      "bench@example.com",
		"created_at": time.Now(),
		"updated_at": time.Now(),
		"status":     "active",
		"count":      int64(100),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.HashRow(row)
	}
}

func BenchmarkBuildHashMap(b *testing.B) {
	h := NewHasher()

	// Create 1000 rows
	rows := make([]types.Row, 1000)
	for i := 0; i < 1000; i++ {
		rows[i] = types.Row{
			"id":    int64(i),
			"name":  "user",
			"email": "user@example.com",
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.BuildHashMap(rows, []string{"id"})
	}
}
