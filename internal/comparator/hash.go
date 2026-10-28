package comparator

import (
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/cespare/xxhash/v2"
)

// NullMarker is a special byte sequence to represent NULL values
var NullMarker = []byte{0x00, 0xFF, 0x00, 0xFF}

// Hasher computes hash values for database rows
type Hasher struct {
	// SortColumns determines if columns should be sorted before hashing
	// When true, hash is deterministic regardless of column order
	SortColumns bool
}

// NewHasher creates a new Hasher with default settings
func NewHasher() *Hasher {
	return &Hasher{
		SortColumns: true,
	}
}

// HashRow computes xxHash64 for a single row
// NULL values are represented with a special marker to ensure uniqueness
func (h *Hasher) HashRow(row types.Row) uint64 {
	if len(row) == 0 {
		return 0
	}

	hasher := xxhash.New()

	// Get column names
	columns := make([]string, 0, len(row))
	for col := range row {
		columns = append(columns, col)
	}

	// Sort columns for deterministic hashing
	if h.SortColumns {
		sort.Strings(columns)
	}

	// Hash each column value
	for _, col := range columns {
		// Write column name
		hasher.Write([]byte(col))
		hasher.Write([]byte{0x01}) // separator

		// Write value
		val := row[col]
		h.writeValue(hasher, val)
		hasher.Write([]byte{0x02}) // separator
	}

	return hasher.Sum64()
}

// HashRowColumns computes hash for specific columns only
func (h *Hasher) HashRowColumns(row types.Row, columns []string) uint64 {
	if len(row) == 0 || len(columns) == 0 {
		return 0
	}

	hasher := xxhash.New()

	// Use provided column order (don't sort)
	for _, col := range columns {
		hasher.Write([]byte(col))
		hasher.Write([]byte{0x01})

		val, ok := row[col]
		if !ok {
			hasher.Write(NullMarker)
		} else {
			h.writeValue(hasher, val)
		}
		hasher.Write([]byte{0x02})
	}

	return hasher.Sum64()
}

// HashKey computes hash for key columns only
func (h *Hasher) HashKey(row types.Row, keyColumns []string) uint64 {
	return h.HashRowColumns(row, keyColumns)
}

// writeValue writes a value to the hasher, handling various types
func (h *Hasher) writeValue(hasher *xxhash.Digest, val interface{}) {
	if val == nil {
		hasher.Write(NullMarker)
		return
	}

	switch v := val.(type) {
	case []byte:
		if v == nil {
			hasher.Write(NullMarker)
		} else {
			hasher.Write(v)
		}

	case string:
		hasher.Write([]byte(v))

	case int:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		hasher.Write(buf)

	case int8:
		hasher.Write([]byte{byte(v)})

	case int16:
		buf := make([]byte, 2)
		binary.BigEndian.PutUint16(buf, uint16(v))
		hasher.Write(buf)

	case int32:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(v))
		hasher.Write(buf)

	case int64:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		hasher.Write(buf)

	case uint:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		hasher.Write(buf)

	case uint8:
		hasher.Write([]byte{v})

	case uint16:
		buf := make([]byte, 2)
		binary.BigEndian.PutUint16(buf, v)
		hasher.Write(buf)

	case uint32:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, v)
		hasher.Write(buf)

	case uint64:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, v)
		hasher.Write(buf)

	case float32:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(v))
		hasher.Write(buf)

	case float64:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v))
		hasher.Write(buf)

	case bool:
		if v {
			hasher.Write([]byte{1})
		} else {
			hasher.Write([]byte{0})
		}

	case time.Time:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v.UnixNano()))
		hasher.Write(buf)

	default:
		// Fallback: convert to string
		hasher.Write([]byte(fmt.Sprintf("%v", v)))
	}
}

// RowHash represents a row with its computed hash
type RowHash struct {
	Row     types.Row
	Hash    uint64
	KeyHash uint64
}

// HashRows computes hashes for multiple rows
func (h *Hasher) HashRows(rows []types.Row, keyColumns []string) []RowHash {
	result := make([]RowHash, len(rows))

	for i, row := range rows {
		result[i] = RowHash{
			Row:     row,
			Hash:    h.HashRow(row),
			KeyHash: h.HashKey(row, keyColumns),
		}
	}

	return result
}

// BuildHashMap builds a map from key hash to row hash for fast lookup
func (h *Hasher) BuildHashMap(rows []types.Row, keyColumns []string) map[uint64]RowHash {
	result := make(map[uint64]RowHash, len(rows))

	for _, row := range rows {
		keyHash := h.HashKey(row, keyColumns)
		result[keyHash] = RowHash{
			Row:     row,
			Hash:    h.HashRow(row),
			KeyHash: keyHash,
		}
	}

	return result
}
