package sampler

import (
	"context"
	"testing"

	"github.com/case-88/my-db-compare/internal/testutil"
	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestPartitionFailureAndMappingMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".mapped (physical_id INT PRIMARY KEY)")
	testutil.Exec(t, conn, "INSERT INTO "+schema+".mapped VALUES (1),(2),(3)")
	meta := types.TableMeta{Schema: schema, Name: "mapped", PKColumns: []string{"id"}, ColumnMap: map[string]string{"id": "physical_id"}}
	plan := &PartitionPlan{Table: meta, KeyColumn: "id", Partitions: []Partition{{MinValue: int64(1), MaxValue: int64(3), Samples: 3}}}
	s := NewSampler(conn, nil)
	result, err := s.SampleTableWithColumns(context.Background(), plan, []string{"id"})
	require.NoError(t, err)
	require.Equal(t, 3, result.Total)
	for _, row := range result.Samples {
		require.Contains(t, row, "id")
		require.NotContains(t, row, "physical_id")
	}
	_, err = s.SampleTableWithColumns(context.Background(), plan, []string{"absent"})
	require.Error(t, err)
	_, err = s.SampleTableWithColumnsParallel(context.Background(), plan, []string{"absent"}, 2)
	require.Error(t, err)
}
