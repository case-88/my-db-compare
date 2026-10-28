package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCLIProcess(t *testing.T) {
	if os.Getenv("COMPARE_CLI_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"compare"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestCLIExitAndScopeMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".src (a INT,b INT,PRIMARY KEY(a,b))")
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".dst (x INT,y INT,PRIMARY KEY(x,y))")
	testutil.Exec(t, conn, "INSERT INTO "+schema+".src VALUES (1,1),(1,2),(1,3)")
	testutil.Exec(t, conn, "INSERT INTO "+schema+".dst VALUES (1,1),(1,2),(1,3),(9,9)")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf(`source:
  host: 127.0.0.1
  port: %d
  user: root
target:
  host: 127.0.0.1
  port: %d
  user: root
comparison:
  full_mode_chunk_size: 2
  full_mode_delay_ms: 0
  generate_correction_queries: true
tables:
  include: [%s.src]
mappings:
  - source: %s.src
    target: %s.dst
    columns: {a: x, b: y}
`, conn.Config.Port, conn.Config.Port, schema, schema, schema)
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0600))
	run := func(mode string, want int, flags ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		args := []string{"-test.run=^TestCLIProcess$", "--", mode, "--config", configPath, "--output", t.TempDir()}
		cmd := exec.CommandContext(ctx, os.Args[0], append(args, flags...)...)
		cmd.Env = append(os.Environ(), "COMPARE_CLI_TEST=1")
		output, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, string(output))
			code = exitErr.ExitCode()
		}
		require.Equal(t, want, code, string(output))
		return string(output)
	}
	output := run("full", 0)
	require.Contains(t, output, "SOURCE ROWS MATCHED")
	require.Contains(t, output, "NOT CHECKED")
	for _, mode := range []string{"sample", "full"} {
		output = run(mode, 1, "--check-target-only-rows=true")
		require.Contains(t, output, "MISMATCHES FOUND")
	}
	testutil.Exec(t, conn, "DELETE FROM "+schema+".dst WHERE x=9")
	output = run("full", 0, "--check-target-only-rows=true")
	require.Contains(t, output, "ALL MATCHED")
	output = run("sample", 0, "--check-target-only-rows=true")
	require.Contains(t, output, "SAMPLED ROWS MATCHED")
	lock, err := conn.DB.Conn(context.Background())
	require.NoError(t, err)
	defer func() {
		lock.ExecContext(context.Background(), "UNLOCK TABLES")
		lock.Close()
	}()
	_, err = lock.ExecContext(context.Background(), "LOCK TABLES "+schema+".src WRITE")
	require.NoError(t, err)
	for _, mode := range []string{"sample", "full"} {
		run(mode, 124, "--timeout=200ms")
	}
	_, err = lock.ExecContext(context.Background(), "UNLOCK TABLES")
	require.NoError(t, err)
	testutil.Exec(t, conn, "DROP TABLE "+schema+".dst")
	run("full", 2)
}
