package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/pkg/types"
)

// MySQL은 명시적으로 지정한 테스트 서버에 독립 스키마를 만든다.
func MySQL(t *testing.T) (*db.Connection, string) {
	t.Helper()
	if testing.Short() || os.Getenv("TEST_COMPARE_PORT") == "" {
		t.Skip("set TEST_COMPARE_PORT for isolated local MySQL tests")
	}
	var port int
	if _, err := fmt.Sscan(os.Getenv("TEST_COMPARE_PORT"), &port); err != nil {
		t.Fatal(err)
	}
	conn, err := db.NewConnection(context.Background(), types.DBConfig{
		Host: "127.0.0.1", Port: port, User: "root",
	}, "test", db.DefaultPoolConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("compare_test_%d", time.Now().UnixNano())
	if _, err := conn.DB.Exec("CREATE DATABASE `" + schema + "`"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.DB.Exec("DROP DATABASE `" + schema + "`"); err != nil {
			t.Error(err)
		}
		conn.Close()
	})
	return conn, schema
}

func Exec(t *testing.T, conn *db.Connection, query string, args ...interface{}) {
	t.Helper()
	if _, err := conn.DB.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
