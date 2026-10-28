package comparator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/case-88/my-db-compare/internal/testutil"
	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestFractionalValuesAreDifferent(t *testing.T) {
	for _, pair := range [][2]interface{}{
		{float32(1.25), float32(1.75)}, {float64(1.25), float64(1.75)},
		{float64(-1.25), float64(-1.75)}, {[]byte("1.25"), []byte("1.75")},
	} {
		c := NewComparator([]string{"id"})
		r := c.CompareSamples([]types.Row{{"id": 1, "v": pair[0]}}, []types.Row{{"id": 1, "v": pair[1]}})
		require.Zero(t, r.Matched)
		require.Len(t, r.Different, 1)
	}
}

func TestKeysetAfter(t *testing.T) {
	table := types.TableMeta{PKColumns: []string{"a", "b", "c", "d"}, ColumnMap: map[string]string{"a": "mapped_a"}}
	condition, args, err := keysetAfter(table, types.Row{"a": 1, "b": 2, "c": 3, "d": "last"})
	require.NoError(t, err)
	require.Equal(t, "(`mapped_a` > ?) OR (`mapped_a` = ? AND `b` > ?) OR (`mapped_a` = ? AND `b` = ? AND `c` > ?) OR (`mapped_a` = ? AND `b` = ? AND `c` = ? AND `d` > ?)", condition)
	require.Equal(t, []interface{}{1, 1, 2, 1, 2, 3, 1, 2, 3, "last"}, args)
	_, _, err = keysetAfter(table, types.Row{"a": 1})
	require.Error(t, err)
}

func TestFullCompositeAndTargetOnlyMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	for _, keys := range [][]string{{"a"}, {"a", "b"}, {"a", "b", "c"}, {"a", "b", "c", "d"}} {
		t.Run(fmt.Sprint(len(keys)), func(t *testing.T) {
			var defs, targetDefs, mappedKeys []string
			mapping := map[string]string{}
			for _, key := range keys {
				defs = append(defs, key+" BIGINT NOT NULL")
				targetDefs = append(targetDefs, "t_"+key+" BIGINT NOT NULL")
				mappedKeys = append(mappedKeys, "t_"+key)
				mapping[key] = "t_" + key
			}
			src := types.TableMeta{Schema: schema, Name: fmt.Sprintf("src%d", len(keys)), PKColumns: keys}
			dst := types.TableMeta{Schema: schema, Name: fmt.Sprintf("dst%d", len(keys)), PKColumns: keys, ColumnMap: mapping}
			testutil.Exec(t, conn, "CREATE TABLE "+src.FullName()+" ("+strings.Join(defs, ",")+", v DOUBLE, PRIMARY KEY ("+strings.Join(keys, ",")+"))")
			testutil.Exec(t, conn, "CREATE TABLE "+dst.FullName()+" ("+strings.Join(targetDefs, ",")+", v DOUBLE, PRIMARY KEY ("+strings.Join(mappedKeys, ",")+"))")
			for i := 1; i <= 5; i++ {
				values := make([]string, len(keys))
				for j := range keys {
					values[j] = "1"
				}
				values[len(keys)-1] = fmt.Sprint(i)
				testutil.Exec(t, conn, "INSERT INTO "+src.FullName()+" VALUES ("+strings.Join(values, ",")+",1.25)")
				testutil.Exec(t, conn, "INSERT INTO "+dst.FullName()+" VALUES ("+strings.Join(values, ",")+",1.25)")
			}
			testutil.Exec(t, conn, "UPDATE "+dst.FullName()+" SET v=1.75 WHERE "+mappedKeys[len(keys)-1]+"=3")
			extra := make([]string, len(keys))
			for i := range extra {
				extra[i] = "9"
			}
			testutil.Exec(t, conn, "INSERT INTO "+dst.FullName()+" VALUES ("+strings.Join(extra, ",")+",1.25)")
			for _, enabled := range []bool{false, true} {
				cfg := DefaultFullCompareConfig()
				cfg.ChunkSize = 2
				cfg.DelayMs = 0
				cfg.Parallelism = 2
				cfg.CheckTargetOnlyRows = enabled
				cfg.Options.GenerateCorrectionQueries = true
				var records []types.Mismatch
				var mu sync.Mutex
				handler := func(ms []types.Mismatch) error {
					mu.Lock()
					defer mu.Unlock()
					records = append(records, ms...)
					return nil
				}
				result, err := NewFullComparer(conn, conn, cfg, nil).CompareTable(context.Background(), src, dst, append(append([]string{}, keys...), "v"), handler, nil)
				require.NoError(t, err)
				require.EqualValues(t, 5, result.ProcessedRows)
				require.EqualValues(t, 4, result.MatchCount)
				require.EqualValues(t, 1, result.Different)
				require.Equal(t, enabled, result.TargetOnlyChecked)
				if enabled {
					require.EqualValues(t, 6, result.TargetRowsChecked)
					require.EqualValues(t, 1, result.MissingInSource)
					require.Len(t, records, 2)
					for _, m := range records {
						if m.Type == types.MismatchMissingSource {
							require.Contains(t, m.CorrectionQuery, "DELETE FROM `"+schema+"`.`"+dst.Name+"`")
							for _, key := range mappedKeys {
								require.Contains(t, m.CorrectionQuery, "`"+key+"` = 9")
							}
						}
					}
				} else {
					require.Zero(t, result.TargetRowsChecked)
					require.Zero(t, result.MissingInSource)
					require.Len(t, records, 1)
				}
			}
		})
	}
}

func TestFullBoundaryAndErrorsMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	for _, tc := range []struct{ name, typ, values string }{
		{"text", "VARCHAR(32)", "('a'),('b'),('c')"},
		{"unsigned", "BIGINT UNSIGNED", "(18446744073709551613),(18446744073709551614),(18446744073709551615)"},
		{"signed", "BIGINT", fmt.Sprintf("(%d),(%d),(%d)", int64(math.MaxInt64-2), int64(math.MaxInt64-1), int64(math.MaxInt64))},
	} {
		testutil.Exec(t, conn, "CREATE TABLE "+schema+"."+tc.name+" (id "+tc.typ+" PRIMARY KEY)")
		testutil.Exec(t, conn, "INSERT INTO "+schema+"."+tc.name+" VALUES "+tc.values)
		meta := types.TableMeta{Schema: schema, Name: tc.name, PKColumns: []string{"id"}}
		cfg := DefaultFullCompareConfig()
		cfg.ChunkSize = 1
		cfg.DelayMs = 0
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := NewFullComparer(conn, conn, cfg, nil).CompareTable(ctx, meta, meta, []string{"id"}, nil, nil)
		cancel()
		require.NoError(t, err)
		require.EqualValues(t, 3, result.ProcessedRows)
	}
	meta := types.TableMeta{Schema: schema, Name: "text", PKColumns: []string{"id"}}
	cfg := DefaultFullCompareConfig()
	cfg.ChunkSize = 1
	cfg.DelayMs = 0
	fc := NewFullComparer(conn, conn, cfg, nil)
	result, err := fc.CompareTable(context.Background(), meta, meta, []string{"id", "absent"}, nil, nil)
	require.Error(t, err)
	require.Error(t, result.Error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fc.CompareTable(ctx, meta, meta, []string{"id"}, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".empty (id VARCHAR(32) PRIMARY KEY)")
	empty := meta
	empty.Name = "empty"
	cfg.CheckTargetOnlyRows = true
	fc = NewFullComparer(conn, conn, cfg, nil)
	result, err = fc.CompareTable(context.Background(), empty, meta, []string{"id"}, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, result.MissingInSource)
	require.True(t, result.TargetOnlyChecked)
	writeErr := errors.New("injected output failure")
	_, err = fc.CompareTable(context.Background(), meta, empty, []string{"id"}, func([]types.Mismatch) error { return writeErr }, nil)
	require.ErrorIs(t, err, writeErr)
}
