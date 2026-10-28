package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDBConfig_DSN(t *testing.T) {
	tests := []struct {
		name     string
		config   DBConfig
		expected string
	}{
		{
			name: "basic config",
			config: DBConfig{
				Host:     "localhost",
				Port:     3306,
				User:     "root",
				Password: "password",
				Database: "testdb",
			},
			expected: "root:password@tcp(localhost:3306)/testdb?parseTime=true&charset=utf8mb4",
		},
		{
			name: "custom port",
			config: DBConfig{
				Host:     "db.example.com",
				Port:     4000,
				User:     "admin",
				Password: "secret",
				Database: "mydb",
			},
			expected: "admin:secret@tcp(db.example.com:4000)/mydb?parseTime=true&charset=utf8mb4",
		},
		{
			name: "empty password",
			config: DBConfig{
				Host:     "localhost",
				Port:     3306,
				User:     "root",
				Password: "",
				Database: "test",
			},
			expected: "root:@tcp(localhost:3306)/test?parseTime=true&charset=utf8mb4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.DSN()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTableMeta_FullName(t *testing.T) {
	meta := TableMeta{
		Schema: "mydb",
		Name:   "users",
	}

	assert.Equal(t, "mydb.users", meta.FullName())
}

func TestTableMeta_KeyColumns(t *testing.T) {
	t.Run("returns PK columns when available", func(t *testing.T) {
		meta := TableMeta{
			PKColumns: []string{"id"},
			UKColumns: []string{"email"},
		}

		assert.Equal(t, []string{"id"}, meta.KeyColumns())
	})

	t.Run("returns UK columns when PK is empty", func(t *testing.T) {
		meta := TableMeta{
			PKColumns: []string{},
			UKColumns: []string{"email"},
		}

		assert.Equal(t, []string{"email"}, meta.KeyColumns())
	})

	t.Run("returns empty when no keys", func(t *testing.T) {
		meta := TableMeta{
			PKColumns: []string{},
			UKColumns: []string{},
		}

		assert.Empty(t, meta.KeyColumns())
	})
}

func TestTableMeta_HasKey(t *testing.T) {
	tests := []struct {
		name     string
		meta     TableMeta
		expected bool
	}{
		{
			name:     "has PK",
			meta:     TableMeta{PKColumns: []string{"id"}},
			expected: true,
		},
		{
			name:     "has UK only",
			meta:     TableMeta{UKColumns: []string{"email"}},
			expected: true,
		},
		{
			name:     "no keys",
			meta:     TableMeta{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.meta.HasKey())
		})
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{123, "123"},
		{3306, "3306"},
		{65535, "65535"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := itoa(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildUpdateQuery(t *testing.T) {
	tests := []struct {
		name        string
		table       string
		pk          map[string]interface{}
		sourceData  Row
		diffColumns []string
		expected    string
	}{
		{
			name:        "single column update",
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 123},
			sourceData:  Row{"id": 123, "status": "active", "name": "John"},
			diffColumns: []string{"status"},
			expected:    "UPDATE `mydb`.`users` SET `status` = 'active' WHERE `id` = 123;",
		},
		{
			name:        "multiple columns update",
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 456},
			sourceData:  Row{"id": 456, "status": "inactive", "email": "test@example.com"},
			diffColumns: []string{"status", "email"},
			expected:    "UPDATE `mydb`.`users` SET `status` = 'inactive', `email` = 'test@example.com' WHERE `id` = 456;",
		},
		{
			name:        "null value update",
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 789},
			sourceData:  Row{"id": 789, "status": nil},
			diffColumns: []string{"status"},
			expected:    "UPDATE `mydb`.`users` SET `status` = NULL WHERE `id` = 789;",
		},
		{
			name:        "composite pk",
			table:       "mydb.order_items",
			pk:          map[string]interface{}{"order_id": 100, "item_id": 5},
			sourceData:  Row{"order_id": 100, "item_id": 5, "quantity": 10},
			diffColumns: []string{"quantity"},
			expected:    "UPDATE `mydb`.`order_items` SET `quantity` = 10 WHERE `item_id` = 5 AND `order_id` = 100;",
		},
		{
			name:        "empty diff columns",
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 1},
			sourceData:  Row{"id": 1},
			diffColumns: []string{},
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildUpdateQuery(tt.table, tt.pk, tt.sourceData, tt.diffColumns)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildInsertQuery(t *testing.T) {
	tests := []struct {
		name       string
		table      string
		sourceData Row
		expected   string
	}{
		{
			name:       "simple insert",
			table:      "mydb.users",
			sourceData: Row{"id": 123, "name": "John"},
			expected:   "INSERT INTO `mydb`.`users` (`id`, `name`) VALUES (123, 'John');",
		},
		{
			name:       "insert with null",
			table:      "mydb.users",
			sourceData: Row{"id": 456, "email": nil, "name": "Jane"},
			expected:   "INSERT INTO `mydb`.`users` (`email`, `id`, `name`) VALUES (NULL, 456, 'Jane');",
		},
		{
			name:       "empty data",
			table:      "mydb.users",
			sourceData: Row{},
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildInsertQuery(tt.table, tt.sourceData)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildDeleteQuery(t *testing.T) {
	tests := []struct {
		name     string
		table    string
		pk       map[string]interface{}
		expected string
	}{
		{
			name:     "simple delete",
			table:    "mydb.users",
			pk:       map[string]interface{}{"id": 123},
			expected: "DELETE FROM `mydb`.`users` WHERE `id` = 123;",
		},
		{
			name:     "composite pk delete",
			table:    "mydb.order_items",
			pk:       map[string]interface{}{"order_id": 100, "item_id": 5},
			expected: "DELETE FROM `mydb`.`order_items` WHERE `item_id` = 5 AND `order_id` = 100;",
		},
		{
			name:     "empty pk",
			table:    "mydb.users",
			pk:       map[string]interface{}{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildDeleteQuery(tt.table, tt.pk)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildCorrectionQuery(t *testing.T) {
	tests := []struct {
		name        string
		mismatchType MismatchType
		table       string
		pk          map[string]interface{}
		sourceData  Row
		diffColumns []string
		contains    string
	}{
		{
			name:        "data diff generates UPDATE",
			mismatchType: MismatchDataDiff,
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 1},
			sourceData:  Row{"id": 1, "status": "active"},
			diffColumns: []string{"status"},
			contains:    "UPDATE",
		},
		{
			name:        "missing in target generates INSERT",
			mismatchType: MismatchMissingTarget,
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 1},
			sourceData:  Row{"id": 1, "name": "Test"},
			diffColumns: nil,
			contains:    "INSERT",
		},
		{
			name:        "missing in source generates DELETE",
			mismatchType: MismatchMissingSource,
			table:       "mydb.users",
			pk:          map[string]interface{}{"id": 1},
			sourceData:  nil,
			diffColumns: nil,
			contains:    "DELETE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildCorrectionQuery(tt.mismatchType, tt.table, tt.pk, tt.sourceData, tt.diffColumns)
			assert.Contains(t, result, tt.contains)
		})
	}
}
