package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/case-88/my-db-compare/internal/comparator"
	"github.com/case-88/my-db-compare/internal/config"
	"github.com/case-88/my-db-compare/internal/db"
	"github.com/case-88/my-db-compare/internal/reporter"
	"github.com/case-88/my-db-compare/internal/sampler"
	"github.com/case-88/my-db-compare/internal/testutil"
	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestTargetOnlyRunMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	for i := 1; i <= 2; i++ {
		testutil.Exec(t, conn, fmt.Sprintf("CREATE TABLE %s.src%d (a INT, b INT, v DOUBLE, PRIMARY KEY(a,b))", schema, i))
		testutil.Exec(t, conn, fmt.Sprintf("CREATE TABLE %s.dst%d (x INT, y INT, v DOUBLE, PRIMARY KEY(x,y))", schema, i))
		testutil.Exec(t, conn, fmt.Sprintf("INSERT INTO %s.src%d VALUES (1,1,1.25),(1,2,1.25),(1,3,1.25)", schema, i))
		testutil.Exec(t, conn, fmt.Sprintf("INSERT INTO %s.dst%d VALUES (1,1,1.25),(1,2,1.25),(1,3,1.75),(9,9,1.25)", schema, i))
	}
	for _, mode := range []string{"sample", "full"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", mode, enabled), func(t *testing.T) {
				cfg := config.DefaultConfig()
				cfg.Source = conn.Config
				cfg.Target = conn.Config
				cfg.Tables.Include = []string{schema + ".src*"}
				cfg.Comparison.CheckTargetOnlyRows = enabled
				cfg.Comparison.FullModeChunkSize = 2
				cfg.Comparison.FullModeDelayMs = 0
				generate := true
				cfg.Comparison.GenerateCorrectionQueries = &generate
				for i := 1; i <= 2; i++ {
					cfg.Mappings = append(cfg.Mappings, config.TableMapping{Source: fmt.Sprintf("%s.src%d", schema, i), Target: fmt.Sprintf("%s.dst%d", schema, i), Columns: map[string]string{"a": "x", "b": "y"}})
				}
				out := t.TempDir()
				r, err := New(cfg, out, mode, "", nil)
				require.NoError(t, err)
				result, err := r.Run(context.Background())
				require.NoError(t, err)
				require.Len(t, result.Summary.Results, 2)
				require.Equal(t, 2, result.Summary.TablesCompared)
				for _, row := range result.Summary.Results {
					require.Equal(t, 3, row.Sampled)
					require.Equal(t, 2, row.Matched)
					require.Equal(t, 1, row.Different)
					require.Equal(t, enabled, row.TargetOnlyChecked)
					if enabled {
						require.Equal(t, 1, row.MissingInSource)
						require.Equal(t, 4, row.TargetRowsChecked)
					} else {
						require.Zero(t, row.MissingInSource)
						require.Zero(t, row.TargetRowsChecked)
					}
					data, err := os.ReadFile(filepath.Join(out, "mismatch-"+row.Table+".jsonl"))
					require.NoError(t, err)
					for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
						var m types.Mismatch
						require.NoError(t, json.Unmarshal([]byte(line), &m))
						require.Equal(t, row.Table, m.Table)
						if m.Type == types.MismatchMissingSource {
							require.Contains(t, m.CorrectionQuery, "DELETE FROM")
							require.Contains(t, m.CorrectionQuery, "`x` = 9 AND `y` = 9")
						}
					}
				}
			})
		}
	}
}

func TestRetryFailureIsNotReportedAsCancellation(t *testing.T) {
	for _, content := range []string{"invalid json\n", "{\"table\":\"app.users\"}\n"} {
		input := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(input, "mismatch-app.users.jsonl"), []byte(content), 0600))
		r, err := New(config.DefaultConfig(), t.TempDir(), "retry", input, nil)
		require.NoError(t, err)
		s := reporter.NewSummary()
		s.RunMode = "retry"
		result, err := r.runRetryMode(context.Background(), s)
		require.Error(t, err)
		require.False(t, errors.Is(err, context.Canceled))
		require.NotEmpty(t, result.Summary.Error)
		require.Equal(t, "INCOMPLETE", result.Summary.Status())
	}
}

func TestCanceledTablesAreReported(t *testing.T) {
	for _, mode := range []string{"sample", "full"} {
		t.Run(mode, func(t *testing.T) {
			r, err := New(config.DefaultConfig(), t.TempDir(), mode, "", nil)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			tables := []types.TableMeta{{Schema: "test", Name: "a"}, {Schema: "test", Name: "b"}}
			s := reporter.NewSummary()
			var result *Result
			if mode == "full" {
				result, err = r.runFullMode(ctx, tables, s)
			} else {
				result, err = r.runSamplingMode(ctx, tables, s)
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 2, result.Summary.TablesFailed)
			require.Equal(t, "INCOMPLETE", result.Summary.Status())
		})
	}
}

func TestTableErrorsMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".src (id INT PRIMARY KEY)")
	testutil.Exec(t, conn, "INSERT INTO "+schema+".src VALUES (1)")
	for _, mode := range []string{"sample", "full"} {
		cfg := config.DefaultConfig()
		cfg.Source = conn.Config
		cfg.Target = conn.Config
		cfg.Tables.Include = []string{schema + ".src"}
		r, err := New(cfg, t.TempDir(), mode, "", nil)
		require.NoError(t, err)
		// 소스에는 존재하고 타겟 조회에 필요한 칼럼은 없는 상황을 직접 주입한다.
		r.srcConn = conn
		r.targetConn = conn
		r.srcMeta = db.NewMetaQuerier(conn, nil)
		r.srcSampler = sampler.NewSampler(conn, nil)
		r.targetSampler = sampler.NewSampler(conn, nil)
		table := types.TableMeta{Schema: schema, Name: "src", PKColumns: []string{"id"}, Columns: []types.ColumnMeta{{Name: "id"}, {Name: "absent"}}}
		var result *reporter.TableResult
		if mode == "sample" {
			result = r.compareTable(context.Background(), table)
		} else {
			result = r.compareTableFull(context.Background(), table, comparator.DefaultFullCompareConfig(), nil)
		}
		require.NotEmpty(t, result.Error)
		s := reporter.NewSummary()
		s.AddResult(result)
		_, err = r.finishRun(context.Background(), s)
		require.Error(t, err)
		require.Equal(t, "INCOMPLETE", s.Status())
	}
}
