package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestCheckTargetOnlyRowsConfig(t *testing.T) {
	require.False(t, DefaultConfig().Comparison.CheckTargetOnlyRows)
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("comparison:\n  check_target_only_rows: true\n"), 0600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.True(t, cfg.Comparison.CheckTargetOnlyRows)
	t.Setenv("DBCOMPARE_COMPARISON_CHECK_TARGET_ONLY_ROWS", "false")
	cfg, err = Load(path)
	require.NoError(t, err)
	require.False(t, cfg.Comparison.CheckTargetOnlyRows)
}
