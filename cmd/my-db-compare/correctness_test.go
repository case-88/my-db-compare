package main

import (
	"context"
	"errors"
	"testing"

	"github.com/case-88/my-db-compare/internal/reporter"
	"github.com/case-88/my-db-compare/internal/runner"
	"github.com/stretchr/testify/require"
)

func TestComparisonExitCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		ctxErr, runErr error
		row            reporter.TableResult
		want           int
	}{
		{name: "match", want: 0},
		{name: "mismatch", row: reporter.TableResult{Verified: 1}, want: 1},
		{name: "table failure", row: reporter.TableResult{Error: "failed"}, want: 2},
		{name: "skipped", row: reporter.TableResult{Skipped: true}, want: 2},
		{name: "timeout", ctxErr: context.DeadlineExceeded, want: 124},
		{name: "cancel", ctxErr: context.Canceled, want: 130},
		{name: "run failure", runErr: errors.New("failed"), want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reporter.NewSummary()
			s.AddResult(&tc.row)
			require.Equal(t, tc.want, comparisonExitCode(tc.ctxErr, tc.runErr, &runner.Result{Summary: s}))
		})
	}
}

func TestTargetOnlyFlag(t *testing.T) {
	for _, mode := range []string{"sample", "full"} {
		opts, err := parseArgs([]string{"compare", mode, "--check-target-only-rows=true"})
		require.NoError(t, err)
		require.Equal(t, "true", opts.checkTargetOnly)
	}
	_, err := parseArgs([]string{"compare", "full", "--check-target-only-rows=typo"})
	require.Error(t, err)
	_, err = parseArgs([]string{"compare", "retry", "--input=x", "--check-target-only-rows=true"})
	require.Error(t, err)
}
