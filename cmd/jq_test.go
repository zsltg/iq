package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// runWithStdin runs the root command with the given stdin text and returns the
// combined output.
func runWithStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })
	root, _ := newRootCmd()
	root.SetIn(strings.NewReader(stdin))
	return runCmd(t, root, args...)
}

// TestQueryReadsPipedStdin checks a query with no source reads a piped dump and
// that --explain and --verbose describe that read, while a plain run does not.
func TestQueryReadsPipedStdin(t *testing.T) {
	configEnv(t)
	dump := `{"key":"a","value":{"n":1}}` + "\n"
	tests := []struct {
		name     string
		flags    []string
		wantPlan bool
		wantData bool
	}{
		{name: "plain run", wantPlan: false, wantData: true},
		{name: "explain stops before reading", flags: []string{"--explain"}, wantPlan: true, wantData: false},
		{name: "verbose plans and runs", flags: []string{"--verbose"}, wantPlan: true, wantData: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.flags...), ".[]")
			out, err := runWithStdin(t, dump, args...)
			require.NoError(t, err)
			require.Equal(t, tt.wantPlan, strings.Contains(out, "decode stdin dump"), out)
			require.Equal(t, tt.wantData, strings.Contains(out, `"n"`), out)
		})
	}
}

// TestQueryWithoutASourceOnATerminal checks a query with no source and no piped
// input reports the missing source instead of waiting on stdin.
func TestQueryWithoutASourceOnATerminal(t *testing.T) {
	configEnv(t)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = orig })

	root, _ := newRootCmd()
	_, err := runCmd(t, root, ".[]")
	require.ErrorIs(t, err, errNoSource)
}

// TestQueryLogsTracesAndHints runs a query against a fake backend and checks the
// log record, the wire trace switch and the scan-refusal hint.
func TestQueryLogsTracesAndHints(t *testing.T) {
	t.Run("a run logs its start", func(t *testing.T) {
		useCombDriver(t)
		root, _ := newRootCmd()
		out, err := runCmd(t, root, "--src", "a", "--log.file", "stderr", "--log.level", "debug", "--log.format", "json", ".[]")
		require.NoError(t, err)
		require.Contains(t, out, `"msg":"query start"`)
	})

	t.Run("verbose turns the trace on", func(t *testing.T) {
		opened := useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", "--verbose", ".[]")
		require.NoError(t, err)
		require.NotEmpty(t, *opened)
		require.NotNil(t, (*opened)[0].trace)
	})

	t.Run("no verbose keeps the trace off", func(t *testing.T) {
		opened := useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", ".[]")
		require.NoError(t, err)
		require.NotEmpty(t, *opened)
		require.Nil(t, (*opened)[0].trace)
	})

	t.Run("a refused scan names the way out and keeps the cause", func(t *testing.T) {
		useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", ".")
		require.ErrorIs(t, err, query.ErrScanNotAllowed)
		require.ErrorContains(t, err, "re-run with --unbounded")
	})
}

// TestQueryWithAnUnknownSourceDoesNotReadStdin checks a named source that does not
// exist is an error even when input is piped.
func TestQueryWithAnUnknownSourceDoesNotReadStdin(t *testing.T) {
	configEnv(t)
	_, err := runWithStdin(t, `{"key":"a","value":{"n":1}}`+"\n", "--src", "nosuch", ".[]")
	require.ErrorContains(t, err, "nosuch")
}
