package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyConfigPathKeepsTheSetenvCauseInTheChain makes sure that the error
// wraps its cause, so errors.Is still finds it.
func TestApplyConfigPathKeepsTheSetenvCauseInTheChain(t *testing.T) {
	configEnv(t)

	err := applyConfigPath("bad\x00path")

	require.ErrorIs(t, err, syscall.EINVAL)
	require.ErrorContains(t, err, "apply --config")
}

// TestRootStopsWhenTheLogFileCannotOpen makes sure that a log file that cannot
// open stops the run instead of running without a logger.
func TestRootStopsWhenTheLogFileCannotOpen(t *testing.T) {
	configEnv(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	root, _ := newRootCmd()

	_, err := runCmd(t, root, "--log.file", filepath.Join(blocker, "iq.log"), "version")

	require.ErrorContains(t, err, "create log dir")
}
