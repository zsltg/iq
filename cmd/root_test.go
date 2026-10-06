package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
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

// TestCombineWriteFlagsAreExplicit pins that a combine never writes by accident.
// Its results come out of one program over a null input and carry no keys, so a
// destination needs a key expression; when combine was a mode of the root command
// the write flags parsed happily and were then silently dropped. Now --insert
// demands a key, a key flag without --insert is refused, --typed does not exist
// here at all, and a plain move — the root action that owns these flags — still
// dispatches.
func TestCombineWriteFlagsAreExplicit(t *testing.T) {
	t.Setenv("IQ_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"insert without a key", []string{"combine", "users=.", "--with", ".", "--insert", "dest"}, "needs --key or --key-field"},
		{"typed", []string{"combine", "users=.", "--with", ".", "--typed"}, "unknown flag: --typed"},
		{"key without insert", []string{"combine", "users=.", "--with", ".", "--key", ".id"}, "applies to --insert"},
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

// TestRootTakesNoCombineFlags pins that the retired --from/--combine spellings
// are gone outright rather than quietly accepted: the cross-source action is
// `iq combine` now, and a stale script must fail loudly instead of running a
// single-source query that ignores the flags.
func TestRootTakesNoCombineFlags(t *testing.T) {
	t.Setenv("IQ_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	for _, args := range [][]string{
		{"--from", "users=.", "--combine", "."},
		{"--combine", "."},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root, _ := newRootCmd()

			_, err := runCmd(t, root, args...)

			require.ErrorContains(t, err, "unknown flag")
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

// TestRootVersionFlagIsBare pins the scriptable contract: --version prints the
// version alone, with no name or "version" prefix; `iq version` is the rich form.
func TestRootVersionFlagIsBare(t *testing.T) {
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--version")
	require.NoError(t, err)
	require.Equal(t, buildVersion()+"\n", out)
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
	root, cfg := newRootCmd()
	closeResources(t, cfg)
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
	root, cfg := newRootCmd()
	closeResources(t, cfg)
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

// TestRootTakesOneFilter pins the MaximumNArgs(1) bound: the filter is the only
// positional, so a second word is a usage error rather than a silent drop.
func TestRootTakesOneFilter(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, ".a", "extra")
	require.ErrorContains(t, err, "accepts at most 1 arg(s), received 2")
}

// TestRootConfigFlagRejectsABadPath drives the one failure os.Setenv has: a
// value the C environment cannot hold. The run must stop with the wrapped
// message, not fall back to the environment's config file.
func TestRootConfigFlagRejectsABadPath(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--config", "bad\x00path", "config", "location")
	require.ErrorContains(t, err, "apply --config")
}

// TestRootRejectsABadStoredOption proves PreRun stops the invocation when a
// stored default does not apply, rather than running with the flag default.
func TestRootRejectsABadStoredOption(t *testing.T) {
	configEnv(t)
	cf := &iqconfig.Config{
		Sources: map[string]iqconfig.Source{},
		Options: map[string]string{"timeout": "notaduration"},
	}
	require.NoError(t, cf.Save())

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "version")
	require.ErrorContains(t, err, `stored option "timeout"`)
}

// TestRootMonochromeAloneIsAccepted is the other half of the -M/-C conflict:
// only both together are refused.
func TestRootMonochromeAloneIsAccepted(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "-M", "version")
	require.NoError(t, err)
	require.False(t, colorOn(), "-M turns color off")

	root2, _ := newRootCmd()
	_, err = runCmd(t, root2, "-M", "-C", "version")
	require.ErrorContains(t, err, "cannot use --monochrome with --color")
}

// TestRootResolvesColorForTheInvocation pins the PreRun color decision on a run
// with no --output: -C turns color on for a captured buffer that is no terminal.
func TestRootResolvesColorForTheInvocation(t *testing.T) {
	configEnv(t)
	color.NoColor = true // runCmd restores the prior value.
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "-C", "version")
	require.NoError(t, err)
	require.True(t, colorOn(), "-C forces color on for the whole invocation")
}

// TestRootOutputColorResolvedAgainstTheFile pins the second color decision: with
// --output the destination is the file, so color follows the file and not the
// terminal it replaced. A pty master stands in for the terminal.
func TestRootOutputColorResolvedAgainstTheFile(t *testing.T) {
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no pty available to act as a terminal")
	}
	t.Cleanup(func() { _ = pty.Close() })
	configEnv(t)
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })

	path := filepath.Join(t.TempDir(), "out.txt")
	root, cfg := newRootCmd()
	closeResources(t, cfg)
	root.SetOut(pty)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--output", path, "version"})
	require.NoError(t, root.Execute())

	require.NotNil(t, cfg.outClose, "the --output handle is kept for Execute to close")
	require.False(t, colorOn(), "color follows the --output file, not the terminal")
}

// TestRootKeepsTheDiagnosticsHandles pins what PreRun hands to Execute's
// finalize: the log file closer and the profile stopper. A handle left nil leaks
// the resource, because nothing else closes it.
func TestRootKeepsTheDiagnosticsHandles(t *testing.T) {
	t.Run("log file", func(t *testing.T) {
		configEnv(t)
		path := filepath.Join(t.TempDir(), "iq.log")
		root, cfg := newRootCmd()
		_, err := runCmd(t, root, "--log", "--log.file", path, "version")
		require.NoError(t, err)

		require.NotNil(t, cfg.logClose, "the log file is closed by Execute, so PreRun keeps the closer")
		require.NoError(t, cfg.logClose())
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		// The start record names the version and the command, so a run is
		// identifiable in the log from its first line.
		require.Contains(t, string(data), "iq start")
		require.Contains(t, string(data), buildVersion())
		require.Contains(t, string(data), "iq version")
	})

	t.Run("profile", func(t *testing.T) {
		configEnv(t)
		t.Chdir(t.TempDir())
		root, cfg := newRootCmd()
		closeResources(t, cfg)
		_, err := runCmd(t, root, "--debug.pprof", "cpu", "version")
		require.NoError(t, err)

		require.NotNil(t, cfg.pprofStop, "the profile is written by Execute, so PreRun keeps the stopper")
		cfg.pprofStop()
		fi, err := os.Stat("cpu.pprof")
		require.NoError(t, err)
		require.Positive(t, fi.Size())
	})
}

// TestRootBareRunPrintsHelp pins the discoverable entry point: `iq` with no
// filter prints help instead of erroring or reading a filter that is not there.
func TestRootBareRunPrintsHelp(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{})
	require.NoError(t, root.Execute())
	require.Contains(t, buf.String(), "jq for NoSQL databases")
	require.Contains(t, buf.String(), "Usage:")
}

// TestRootInsertExcludesTyped pins the mutually exclusive pair: --insert writes
// into a destination and --typed renders a dump, so one run cannot do both.
func TestRootInsertExcludesTyped(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--insert", "dest", "--typed")
	require.ErrorContains(t, err, "[insert typed] are set none of the others can be")
}

// TestRootWarnsOnAnOpenConfigFile asserts that the pre-run prints the config mode
// warning to stderr only, and that the command still runs.
func TestRootWarnsOnAnOpenConfigFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits, so there is no warning to print")
	}
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })
	p := filepath.Join(t.TempDir(), "iq.toml")
	t.Setenv(iqconfig.EnvConfig, p)
	require.NoError(t, os.WriteFile(p, []byte("[sources.a]\nurl = 'redis://u:p@h:6379/0'\n"), 0o600))
	require.NoError(t, os.Chmod(p, 0o644))
	root, _ := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"version"})

	require.NoError(t, root.Execute())

	require.NotEmpty(t, iqconfig.ModeWarning())
	require.Equal(t, iqconfig.ModeWarning()+"\n", errOut.String())
	require.NotContains(t, out.String(), "warning")
}

// TestLogFailure checks the terminal-error record. A nil error, errQuietExit and
// a nil logger write nothing. An error that wraps a parse error writes one
// "iq failed" ERROR record, and the record does not show the password.
func TestLogFailure(t *testing.T) {
	const secret = "dummy-password"
	parseErr := fmt.Errorf("open source %q: %w", "repro", parseURLErr(t, "redis://review:dummy-password@localhost:bad/0"))
	sinks := []struct {
		name string
		opts logOptions
	}{
		{"json", logOptions{enable: true, file: "stderr", level: slog.LevelDebug, format: "json"}},
		{"text", logOptions{enable: true, file: "stderr", level: slog.LevelDebug, format: "text"}},
		{"tint", logOptions{verbose: true}},
	}
	tests := []struct {
		name      string
		err       error
		noLogger  bool
		wantLines int
	}{
		{name: "nil error", err: nil, wantLines: 0},
		{name: "quiet exit", err: errQuietExit, wantLines: 0},
		{name: "wrapped quiet exit", err: fmt.Errorf("diff: %w", errQuietExit), wantLines: 0},
		{name: "nil logger", err: errors.New("boom"), noLogger: true, wantLines: 0},
		{name: "error with a parse error", err: parseErr, wantLines: 1},
	}
	for _, tt := range tests {
		for _, s := range sinks {
			t.Run(tt.name+"/"+s.name, func(t *testing.T) {
				var stderr bytes.Buffer
				logger, _, err := s.opts.build(&stderr, io.Discard)
				require.NoError(t, err)
				cfg := &config{logger: logger}
				if tt.noLogger {
					cfg.logger = nil
				}

				logFailure(cfg, tt.err)

				out := stderr.String()
				require.NotContains(t, out, secret)
				require.Equal(t, tt.wantLines, strings.Count(out, "\n"))
				if tt.wantLines == 0 {
					return
				}
				require.Contains(t, out, "iq failed")
				require.Contains(t, out, "ERR")
				if s.name == "json" {
					var rec map[string]any
					require.NoError(t, json.Unmarshal([]byte(out), &rec))
					require.Equal(t, "ERROR", rec["level"])
					require.Equal(t, "iq failed", rec["msg"])
				}
			})
		}
	}
}
