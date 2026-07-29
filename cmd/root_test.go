package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
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
		{"--format=csv"}, // no filter: PreRun must reject it before RunE reaches Help
		{"--format.decimal=bogus", "ls"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			root, _ := newRootCmd()
			_, err := runCmd(t, root, args...)
			require.Error(t, err)
		})
	}
}

// TestRootRejectsWriteWithCombine pins the dispatch order: the cross-source
// branch is reached before the move branch, so a write flag paired with it used
// to be read, ignored, and silently dropped. Each case must now fail loudly, and
// the last one pins that a plain move still reaches runMove untouched.
func TestRootRejectsWriteWithCombine(t *testing.T) {
	t.Setenv("IQ_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"insert with from", []string{"--from", "users=.", "--combine", ".", "--insert", "dest"}, "--insert/--typed with --from/--combine"},
		{"typed with from", []string{"--from", "users=.", "--combine", ".", "--typed"}, "--insert/--typed with --from/--combine"},
		{"insert with combine alone", []string{"--combine", ".", "--insert", "dest"}, "--insert/--typed with --from/--combine"},
		{"plain move still dispatches", []string{"--insert", "dest"}, "no source selected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := newRootCmd()

			_, err := runCmd(t, root, tt.args...)

			require.ErrorContains(t, err, tt.want)
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

// TestRootFormatShorthand pins -f to the unified --format selector.
func TestRootFormatShorthand(t *testing.T) {
	root, _ := newRootCmd()
	sh := root.Flags().ShorthandLookup("f")
	require.NotNil(t, sh)
	require.Equal(t, "format", sh.Name)
}

// TestRootDecimalResolvesInPreRun pins the resolved decimal mode: PersistentPreRunE
// parses --format.decimal into cfg.decimalMode before any store opens. version runs
// PreRun without touching config or a backend, so it isolates the resolution.
func TestRootDecimalResolvesInPreRun(t *testing.T) {
	t.Run("defaults to auto", func(t *testing.T) {
		root, cfg := newRootCmd()
		_, err := runCmd(t, root, "version")
		require.NoError(t, err)
		require.Equal(t, numfmt.DecimalAuto, cfg.decimalMode)
	})
	t.Run("number flag resolves to DecimalNumber", func(t *testing.T) {
		root, cfg := newRootCmd()
		_, err := runCmd(t, root, "--format.decimal=number", "version")
		require.NoError(t, err)
		require.Equal(t, numfmt.DecimalNumber, cfg.decimalMode)
	})
}

// TestOpenOutputFile pins the --output file helper: it creates and truncates the
// target and wraps a failure so the message anchors at the CLI.
func TestOpenOutputFile(t *testing.T) {
	t.Run("creates and writes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.txt")
		f, err := openOutputFile(path)
		require.NoError(t, err)
		_, err = f.WriteString("hello")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "hello", string(got))
	})
	t.Run("truncates an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.txt")
		require.NoError(t, os.WriteFile(path, []byte("AAAAAAAAAA"), 0o644))
		f, err := openOutputFile(path)
		require.NoError(t, err)
		_, err = f.WriteString("B")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "B", string(got))
	})
	t.Run("missing directory errors with context", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope", "out.txt")
		_, err := openOutputFile(path)
		require.Error(t, err)
		require.Contains(t, err.Error(), "open output file")
	})
}

// TestRootOutputRedirectsToFile pins that --output redirects a command's stdout
// to the file (leaving the captured out/err buffer empty of the payload) and,
// because a regular file is not a terminal, turns color off. version runs the
// root PreRun without a backend, so it isolates the redirect.
func TestRootOutputRedirectsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	root, _ := newRootCmd()
	buf, err := runCmd(t, root, "--output", path, "version")
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(got), "iq "+buildVersion())
	require.NotContains(t, buf, "iq "+buildVersion()) // payload went to the file, not stdout
	require.False(t, colorOn())                       // color off for a file destination
}

// TestRootOutputColorForcedToFile pins that -C forces color even when --output
// redirects to a (non-terminal) file, mirroring the pager behavior.
func TestRootOutputColorForcedToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--color", "--output", path, "version")
	require.NoError(t, err)
	require.True(t, colorOn())
}

// TestRootOutputBadPathFailsFast pins that an unopenable --output target aborts
// the invocation in PreRun with a wrapped, CLI-anchored error.
func TestRootOutputBadPathFailsFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "out.txt")
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--output", path, "version")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open output file")
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
