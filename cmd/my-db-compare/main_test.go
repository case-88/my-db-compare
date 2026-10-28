package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitTables(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"db.table1,db.table2", []string{"db.table1", "db.table2"}},
		{"db.*", []string{"db.*"}},
		{"", nil},
		{"single", []string{"single"}},
		{"a,b,c,d", []string{"a", "b", "c", "d"}},
		{"a,,b", []string{"a", "b"}}, // Empty between commas
	}

	for _, tc := range tests {
		result := splitTables(tc.input)
		assert.Equal(t, tc.expected, result, "input: %s", tc.input)
	}
}

func TestSetupLogger(t *testing.T) {
	tests := []struct {
		level   string
		wantErr bool
	}{
		{"debug", false},
		{"info", false},
		{"warn", false},
		{"error", false},
		{"invalid", false}, // Falls back to info
	}

	for _, tc := range tests {
		logger, err := setupLogger(tc.level)
		if tc.wantErr {
			assert.Error(t, err, "level: %s", tc.level)
		} else {
			assert.NoError(t, err, "level: %s", tc.level)
			assert.NotNil(t, logger, "level: %s", tc.level)
		}
	}
}
