package reporter

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLogger(t *testing.T) {
	t.Run("valid formats", func(t *testing.T) {
		formats := []Format{FormatJSONL, FormatJSON, FormatText, ""}

		for _, f := range formats {
			logger, err := NewLogger("/tmp", f, nil)
			require.NoError(t, err)
			assert.NotNil(t, logger)
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		_, err := NewLogger("/tmp", "invalid", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported format")
	})

	t.Run("default format", func(t *testing.T) {
		logger, err := NewLogger("/tmp", "", nil)
		require.NoError(t, err)
		assert.Equal(t, FormatJSONL, logger.Format())
	})
}

func TestLogger_OpenClose(t *testing.T) {
	tmpDir := t.TempDir()

	logger, err := NewLogger(tmpDir, FormatJSONL, nil)
	require.NoError(t, err)

	err = logger.Open("test_table")
	require.NoError(t, err)

	// Should fail to open again
	err = logger.Open("another_table")
	assert.Error(t, err)

	err = logger.Close()
	require.NoError(t, err)

	// Check file was created
	expectedFile := filepath.Join(tmpDir, "mismatch-test_table.jsonl")
	_, err = os.Stat(expectedFile)
	require.NoError(t, err)
}

func TestLogger_WriteJSONL(t *testing.T) {
	tmpDir := t.TempDir()

	logger, err := NewLogger(tmpDir, FormatJSONL, nil)
	require.NoError(t, err)

	err = logger.Open("test")
	require.NoError(t, err)

	mismatch := types.Mismatch{
		Table:     "testdb.users",
		PK:        map[string]interface{}{"id": 1},
		Type:      types.MismatchMissingTarget,
		Timestamp: time.Now(),
	}

	err = logger.Write(mismatch)
	require.NoError(t, err)
	assert.Equal(t, 1, logger.Count())

	err = logger.Close()
	require.NoError(t, err)

	// Read and verify
	expectedFile := filepath.Join(tmpDir, "mismatch-test.jsonl")
	data, err := os.ReadFile(expectedFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "testdb.users")
	assert.Contains(t, string(data), "MISSING_IN_TARGET")
}

func TestLogger_WriteJSON(t *testing.T) {
	tmpDir := t.TempDir()

	logger, err := NewLogger(tmpDir, FormatJSON, nil)
	require.NoError(t, err)

	err = logger.Open("test")
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		err = logger.Write(types.Mismatch{
			Table:     "testdb.users",
			PK:        map[string]interface{}{"id": i},
			Type:      types.MismatchDataDiff,
			Timestamp: time.Now(),
		})
		require.NoError(t, err)
	}

	err = logger.Close()
	require.NoError(t, err)

	// Read and verify JSON array
	expectedFile := filepath.Join(tmpDir, "mismatch-test.json")
	data, err := os.ReadFile(expectedFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "[")
	assert.Contains(t, string(data), "]")
}

func TestLogger_WriteText(t *testing.T) {
	tmpDir := t.TempDir()

	logger, err := NewLogger(tmpDir, FormatText, nil)
	require.NoError(t, err)

	err = logger.Open("test")
	require.NoError(t, err)

	err = logger.Write(types.Mismatch{
		Table:       "testdb.users",
		PK:          map[string]interface{}{"id": 1},
		Type:        types.MismatchDataDiff,
		DiffColumns: []string{"name", "email"},
		SourceData:  types.Row{"id": 1, "name": "Alice", "email": "alice@old.com"},
		TargetData:  types.Row{"id": 1, "name": "Bob", "email": "bob@new.com"},
		Query:       "SELECT * FROM `testdb`.`users` WHERE `id` = 1;",
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)

	err = logger.Close()
	require.NoError(t, err)

	// Read and verify - single line format
	// Format: timestamp | type | table | pk | diff_values | query
	expectedFile := filepath.Join(tmpDir, "mismatch-test.txt")
	data, err := os.ReadFile(expectedFile)
	require.NoError(t, err)
	output := string(data)
	assert.Contains(t, output, "testdb.users")
	assert.Contains(t, output, "DATA_MISMATCH")
	assert.Contains(t, output, "id=1")
	assert.Contains(t, output, "name:")
	assert.Contains(t, output, "Alice")
	assert.Contains(t, output, "Bob")
	assert.Contains(t, output, "SELECT * FROM")
	// Should be single line (one newline at end)
	assert.Equal(t, 1, len(data)-len([]byte(output[:len(output)-1])))
}

func TestLogger_WriteBatch(t *testing.T) {
	tmpDir := t.TempDir()

	logger, err := NewLogger(tmpDir, FormatJSONL, nil)
	require.NoError(t, err)

	err = logger.Open("test")
	require.NoError(t, err)

	mismatches := []types.Mismatch{
		{Table: "t1", PK: map[string]interface{}{"id": 1}, Type: types.MismatchMissingTarget, Timestamp: time.Now()},
		{Table: "t1", PK: map[string]interface{}{"id": 2}, Type: types.MismatchMissingSource, Timestamp: time.Now()},
		{Table: "t1", PK: map[string]interface{}{"id": 3}, Type: types.MismatchDataDiff, Timestamp: time.Now()},
	}

	err = logger.WriteBatch(mismatches)
	require.NoError(t, err)
	assert.Equal(t, 3, logger.Count())

	err = logger.Close()
	require.NoError(t, err)
}

func TestLogger_WriteWithoutOpen(t *testing.T) {
	logger, err := NewLogger(t.TempDir(), FormatJSONL, nil)
	require.NoError(t, err)

	err = logger.Write(types.Mismatch{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not open")
}

func TestLogger_OutputPath(t *testing.T) {
	logger, err := NewLogger("/custom/path", FormatJSONL, nil)
	require.NoError(t, err)
	assert.Equal(t, "/custom/path", logger.OutputPath())
}
