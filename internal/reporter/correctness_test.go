package reporter

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailedTableIsUnverified(t *testing.T) {
	s := NewSummary()
	s.AddResult(&TableResult{Table: "app.users", Error: "query failed"})
	require.True(t, s.HasErrors())
	require.Equal(t, 1, s.TablesFailed)
	require.Zero(t, s.TablesCompared)
	require.Equal(t, "INCOMPLETE", s.Status())
	require.Zero(t, s.MatchRate())
	var buf bytes.Buffer
	s.Fprint(&buf)
	require.Contains(t, buf.String(), "Unverified:     app.users: query failed")
	require.NotContains(t, buf.String(), "ALL MATCHED")
	data, err := s.ToJSON()
	require.NoError(t, err)
	require.Contains(t, string(data), `"tables_failed": 1`)
}

func TestSummaryScope(t *testing.T) {
	s := NewSummary()
	require.Equal(t, "RUNNING", s.Status())
	s.Finish()
	s.RunMode = "full"
	s.Config = &RunConfig{CheckTargetOnlyRows: true}
	s.AddResult(&TableResult{Table: "app.users"})
	require.Equal(t, "INCOMPLETE", s.Status())
	s.Results[0].TargetOnlyChecked = true
	require.Equal(t, "ALL MATCHED", s.Status())
	s.RunMode = "sample"
	require.Equal(t, "SAMPLED ROWS MATCHED", s.Status())
	s.Config.CheckTargetOnlyRows = false
	s.RunMode = "full"
	require.Equal(t, "SOURCE ROWS MATCHED", s.Status())
	s.RunMode = "retry"
	require.Equal(t, "RETRY ROWS MATCHED", s.Status())
}
