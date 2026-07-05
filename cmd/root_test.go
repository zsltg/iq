package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRootTimeoutDefault pins the default --timeout. The default bounds every
// query, so a drift in the 5*time.Second literal must not pass unnoticed.
func TestRootTimeoutDefault(t *testing.T) {
	root, _ := newRootCmd()

	got, err := root.PersistentFlags().GetDuration("timeout")

	require.NoError(t, err)
	require.Equal(t, 5*time.Second, got)
}

// TestRootRejectsBadDiagnosticsFlags drives a bad value for each validated
// diagnostics flag through the root PreRun, pinning that each failure aborts the
// invocation (the invalid modes error before any file is created).
func TestRootRejectsBadDiagnosticsFlags(t *testing.T) {
	cases := [][]string{
		{"--error.format=xml", "ls"},
		{"--log.level=loud", "ls"},
		{"--log.format=xml", "ls"},
		{"--debug.pprof=bogus", "ls"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			root, _ := newRootCmd()
			_, err := runCmd(t, root, args...)
			require.Error(t, err)
		})
	}
}

func TestValidateErrorFormat(t *testing.T) {
	require.NoError(t, validateErrorFormat("text"))
	require.NoError(t, validateErrorFormat("JSON"))
	require.NoError(t, validateErrorFormat(" json "))
	require.Error(t, validateErrorFormat("xml"))
	require.Error(t, validateErrorFormat(""))
}

// TestRootVerboseShorthand pins the collision fix: the global -v is --verbose,
// so cobra leaves --version long-only rather than claiming -v.
func TestRootVerboseShorthand(t *testing.T) {
	root, _ := newRootCmd()
	sh := root.PersistentFlags().ShorthandLookup("v")
	require.NotNil(t, sh)
	require.Equal(t, "verbose", sh.Name)
	require.NotEmpty(t, root.Version)
}

func TestRootRegistersSourceCommands(t *testing.T) {
	root, _ := newRootCmd()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, name := range []string{"add", "ls", "rm", "mv", "src", "group", "ping", "inspect", "exec"} {
		require.True(t, have[name], "root should register %q", name)
	}
}
