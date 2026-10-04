package cmd

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// configEnv points the config path at a fresh temp file and returns it.
func configEnv(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "iq.toml")
	t.Setenv(iqconfig.EnvConfig, p)
	return p
}

// seedSource saves a config holding one MongoDB source at the env path.
func seedSource(t *testing.T, handle string) {
	t.Helper()
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add(handle, "mongodb://h/db?collection=books"))
	require.NoError(t, cf.Save())
}

func TestConfigLocation(t *testing.T) {
	p := configEnv(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "location")
	require.NoError(t, err)
	require.Equal(t, p, strings.TrimSpace(out))
}

// TestConfigLocationRejectsArgs pins the NoArgs guard: `location` prints one
// path and takes none, so a stray argument is a usage error, not ignored input.
func TestConfigLocationRejectsArgs(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "location", "extra")
	require.ErrorContains(t, err, `unknown command "extra"`)
}

// TestConfigLocationReportsAPathFailure drives the one failure Path has: no
// override and no home-like variable. The command must report it, not print an
// empty line and exit clean.
func TestConfigLocationReportsAPathFailure(t *testing.T) {
	t.Setenv(iqconfig.EnvConfig, "")
	t.Setenv("XDG_CONFIG_HOME", "") // unix
	t.Setenv("HOME", "")            // unix and darwin
	t.Setenv("AppData", "")         // windows

	out, err := runCmd(t, newConfigLocationCmd())
	require.ErrorContains(t, err, "locate user config dir")
	require.NotContains(t, out, "iq.toml", "no path is printed when none resolves")
}

// TestConfigLocationPropagatesAWriteError asserts the print result is returned:
// a closed or full stdout must fail the command, not pass silently.
func TestConfigLocationPropagatesAWriteError(t *testing.T) {
	configEnv(t)
	c := newConfigLocationCmd()
	c.SetOut(&errAfter{0})
	c.SetErr(io.Discard)
	c.SetArgs(nil)
	require.ErrorContains(t, c.Execute(), "write failed")
}

func TestConfigSetGetBase(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "set", "format", "yaml")
	require.NoError(t, err)
	require.Contains(t, out, "set format = yaml")

	root2, _ := newRootCmd()
	out, err = runCmd(t, root2, "config", "get", "format")
	require.NoError(t, err)
	require.Equal(t, "yaml", strings.TrimSpace(out))
}

func TestConfigSetGetPerSource(t *testing.T) {
	configEnv(t)
	seedSource(t, "prod")

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "format", "yaml")
	require.NoError(t, err)
	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "config", "set", "--src", "prod", "format", "jsonl")
	require.NoError(t, err)
	require.Contains(t, out, "set prod format = jsonl")

	// The source value wins for that source; the base value stands elsewhere.
	root3, _ := newRootCmd()
	out, err = runCmd(t, root3, "config", "get", "--src", "prod", "format")
	require.NoError(t, err)
	require.Equal(t, "jsonl", strings.TrimSpace(out))

	root4, _ := newRootCmd()
	out, err = runCmd(t, root4, "config", "get", "format")
	require.NoError(t, err)
	require.Equal(t, "yaml", strings.TrimSpace(out))
}

func TestConfigSetValidLogLevel(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "set", "log.level", "WARN")
	require.NoError(t, err)
	require.Contains(t, out, "set log.level = WARN")
}

func TestConfigGetSourceFallsBackToBase(t *testing.T) {
	configEnv(t)
	seedSource(t, "prod")

	// Only the base value is set; a source `get` inherits it.
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "format", "yaml")
	require.NoError(t, err)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "config", "get", "--src", "prod", "format")
	require.NoError(t, err)
	require.Equal(t, "yaml", strings.TrimSpace(out))
}

func TestConfigSetRejects(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"invalid value", []string{"config", "set", "format", "bogus"}, "invalid"},
		{"invalid duration", []string{"config", "set", "timeout", "notaduration"}, "invalid"},
		{"invalid log level", []string{"config", "set", "log.level", "LOUD"}, "invalid --log.level"},
		{"invalid log format", []string{"config", "set", "log.format", "xml"}, "invalid log.format"},
		{"invalid decimal", []string{"config", "set", "format.decimal", "bogus"}, "decimal"},
		{"unknown option", []string{"config", "set", "nope", "x"}, "unknown option"},
		// A real root flag that is not on the persistable allowlist. `nope` above is
		// no flag at all, so it is caught later, by the nil-flag branch; only a
		// non-persistable flag exercises the allowlist guard itself. Stored, it
		// would be applied at query time, which the guard exists to prevent.
		{"non-persistable flag", []string{"config", "set", "force", "true"}, "unknown option"},
		{"unknown source", []string{"config", "set", "--src", "ghost", "format", "yaml"}, "unknown source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configEnv(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestConfigSetDelete(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "compact", "true")
	require.NoError(t, err)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "config", "set", "-D", "compact")
	require.NoError(t, err)
	require.Contains(t, out, "unset compact")

	root3, _ := newRootCmd()
	out, err = runCmd(t, root3, "config", "set", "--delete", "compact")
	require.NoError(t, err)
	require.Contains(t, out, "was not set")
}

func TestConfigSetArgErrors(t *testing.T) {
	configEnv(t)
	// A plain set needs a value; -D takes an option only. (The retired `unset`
	// subcommand is gone — deletion is `set -D`.)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "compact")
	require.ErrorContains(t, err, "set takes <option> <value>")

	root2, _ := newRootCmd()
	_, err = runCmd(t, root2, "config", "set", "-D", "compact", "true")
	require.ErrorContains(t, err, "no value")
}

// TestConfigSetArgCount pins the RangeArgs(1, 2) bound. Cobra must reject the
// count before RunE reads args[0], so the message is cobra's, not the body's.
func TestConfigSetArgCount(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no args", []string{"config", "set"}},
		{"three args", []string{"config", "set", "format", "yaml", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configEnv(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, "accepts between 1 and 2 arg(s)")
		})
	}
}

// TestConfigSetReportsASaveFailure makes the write fail after the value
// validates: an unwritable config dir must fail the command, so `set` never
// reports a default it did not store.
func TestConfigSetReportsASaveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "iq.toml")
	t.Setenv(iqconfig.EnvConfig, p)
	require.NoError(t, os.WriteFile(p, []byte("\n"), 0o600))
	require.NoError(t, os.Chmod(dir, 0o500)) // readable and listable, but not writable.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "set", "format", "yaml")
	require.ErrorContains(t, err, "create temp config")
	require.NotContains(t, out, "set format = yaml", "no success line for a store that failed")
}

// TestRunConfigSetPropagatesAWriteError asserts the confirmation line's write
// result is returned, not dropped after a successful store.
func TestRunConfigSetPropagatesAWriteError(t *testing.T) {
	configEnv(t)
	c := newConfigSetCmd(&config{})
	c.SetOut(&errAfter{0})
	require.ErrorContains(t, runConfigSet(c, &config{}, []string{"format", "yaml"}), "write failed")
}

// TestListAllOptionsPropagatesAWriteError asserts the per-option write result is
// returned, so a broken stdout stops the listing instead of ending clean.
func TestListAllOptionsPropagatesAWriteError(t *testing.T) {
	configEnv(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.ErrorContains(t, listAllOptions(&errAfter{0}, cf, ""), "write failed")
}

func TestConfigLs(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "format", "yaml")
	require.NoError(t, err)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "config", "ls")
	require.NoError(t, err)
	require.Equal(t, "format = yaml", strings.TrimSpace(out))

	// -v (the global --verbose) lists every option with its default and help.
	root3, _ := newRootCmd()
	out, err = runCmd(t, root3, "config", "ls", "-v")
	require.NoError(t, err)
	require.Contains(t, out, "* format = yaml")
	require.Contains(t, out, "timeout = 5s (default 5s)")
}

func TestConfigView(t *testing.T) {
	configEnv(t)
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("cache", "redis://u:secret@h:6379/0"))
	require.NoError(t, cf.SetOption("", "format", "yaml"))
	require.NoError(t, cf.Save())

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "view")
	require.NoError(t, err)
	require.Contains(t, out, "format = 'yaml'")
	require.Contains(t, out, "xxxxx", "password redacted by default")
	require.NotContains(t, out, "secret")

	// --reveal prints the inline password verbatim.
	root2, _ := newRootCmd()
	out, err = runCmd(t, root2, "config", "view", "--reveal")
	require.NoError(t, err)
	require.Contains(t, out, "secret")
}

func TestConfigEdit(t *testing.T) {
	p := configEnv(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "ed.sh")
	body := "#!/bin/sh\necho ran > '" + sentinel + "'\n"
	if runtime.GOOS == "windows" { // a batch file is the executable script there.
		script = filepath.Join(dir, "ed.cmd")
		body = "@echo ran> \"" + sentinel + "\"\r\n"
	}
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("IQ_EDITOR", script)

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "edit")
	require.NoError(t, err)

	require.FileExists(t, sentinel, "editor was invoked")
	require.FileExists(t, p, "a missing config file is created for the editor")
}

func TestConfigPathFlagOverridesEnv(t *testing.T) {
	// An env path is set; --config must win over it.
	t.Setenv(iqconfig.EnvConfig, filepath.Join(t.TempDir(), "env.toml"))
	flagPath := filepath.Join(t.TempDir(), "flag.toml")

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--config", flagPath, "config", "location")
	require.NoError(t, err)
	require.Equal(t, flagPath, strings.TrimSpace(out))
}

func TestApplyStoredOptions(t *testing.T) {
	t.Run("base option fills an unset flag", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.SetOption("", "format", "yaml"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "yaml", cfg.format)
	})

	t.Run("explicit flag wins over a stored option", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.SetOption("", "format", "yaml"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		require.NoError(t, root.Flags().Set("format", "json"))
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "json", cfg.format)
	})

	t.Run("per-source option beats base", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.Add("prod", "mongodb://h/db?collection=books"))
		require.NoError(t, cf.SetOption("", "format", "yaml"))
		require.NoError(t, cf.SetOption("prod", "format", "jsonl"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		cfg.src = "prod"
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "jsonl", cfg.format)
	})

	t.Run("per-source option applies to a collection-addressed src", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.Add("prod", "mongodb://h/db?collection=books"))
		require.NoError(t, cf.SetOption("", "format", "yaml"))
		require.NoError(t, cf.SetOption("prod", "format", "jsonl"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		cfg.src = "prod.authors"
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "jsonl", cfg.format)
	})

	t.Run("a stored value the flag rejects fails fast", func(t *testing.T) {
		// The hand-edited config the function comment promises to catch: the value
		// never went through `config set`, so the flag is the first to see it.
		configEnv(t)
		cf := &iqconfig.Config{
			Sources: map[string]iqconfig.Source{},
			Options: map[string]string{"timeout": "notaduration"},
		}
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		err := applyStoredOptions(root, cfg)
		require.ErrorContains(t, err, `stored option "timeout"`)
		require.ErrorContains(t, err, "notaduration", "the cause names the bad value")
	})

	t.Run("per-source persistent option (log.level) is applied", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.Add("prod", "mongodb://h/db?collection=books"))
		require.NoError(t, cf.SetOption("prod", "log.level", "WARN"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		cfg.src = "prod"
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "WARN", cfg.logLevel)
	})
}

// TestApplyStoredOptionsUsesTheActiveSource checks that, with no --src, the
// active source's option is the one applied.
func TestApplyStoredOptionsUsesTheActiveSource(t *testing.T) {
	configEnv(t)
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("prod", "mongodb://h/db?collection=books"))
	require.NoError(t, cf.SetOption("", "format", "yaml"))
	require.NoError(t, cf.SetOption("prod", "format", "jsonl"))
	cf.Active = "prod"
	require.NoError(t, cf.Save())

	root, cfg := newRootCmd()
	require.NoError(t, applyStoredOptions(root, cfg))
	require.Equal(t, "jsonl", cfg.format)
}

// TestApplyStoredOptionsKeepsTheCause pins that a rejected stored value stays
// reachable through errors.Is, so a caller can still match the parse failure.
func TestApplyStoredOptionsKeepsTheCause(t *testing.T) {
	configEnv(t)
	cf := &iqconfig.Config{
		Sources: map[string]iqconfig.Source{},
		Options: map[string]string{"compact": "maybe"},
	}
	require.NoError(t, cf.Save())

	root, cfg := newRootCmd()
	err := applyStoredOptions(root, cfg)
	require.ErrorIs(t, err, strconv.ErrSyntax)
}

// TestCanonicalOptionValueKeepsTheCause pins the same wrap for `config set`.
func TestCanonicalOptionValueKeepsTheCause(t *testing.T) {
	_, err := canonicalOptionValue("compact", "maybe")
	require.ErrorIs(t, err, strconv.ErrSyntax)
	require.ErrorContains(t, err, `invalid value for "compact"`)
}

// TestValidateLogFormat checks the log.format rule one value at a time.
func TestValidateLogFormat(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "text", value: "text"},
		{name: "json", value: "json"},
		{name: "upper case", value: "JSON"},
		{name: "padded", value: " text "},
		{name: "xml", value: "xml", wantErr: true},
		{name: "empty", value: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOptionSemantics("log.format", tt.value)
			if tt.wantErr {
				require.ErrorContains(t, err, "want text or json")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestResolveEditor checks the editor lookup order and the argument split.
func TestResolveEditor(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantCmd  string
		wantArgs []string
	}{
		{name: "fallback", wantCmd: "vi"},
		{name: "arguments are split off", env: map[string]string{"IQ_EDITOR": "code --wait -n"}, wantCmd: "code", wantArgs: []string{"--wait", "-n"}},
		{name: "IQ_EDITOR beats VISUAL", env: map[string]string{"IQ_EDITOR": "a", "VISUAL": "b", "EDITOR": "c"}, wantCmd: "a"},
		{name: "VISUAL beats EDITOR", env: map[string]string{"VISUAL": "b", "EDITOR": "c"}, wantCmd: "b"},
		{name: "EDITOR last", env: map[string]string{"EDITOR": "c"}, wantCmd: "c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"IQ_EDITOR", "VISUAL", "EDITOR"} {
				t.Setenv(k, tt.env[k])
			}
			gotCmd, gotArgs := resolveEditor()
			require.Equal(t, tt.wantCmd, gotCmd)
			require.Equal(t, len(tt.wantArgs), len(gotArgs))
			for i, want := range tt.wantArgs {
				require.Equal(t, want, gotArgs[i])
			}
		})
	}
}

// TestConfigSubcommandsRejectArgs pins the NoArgs guard of each subcommand that
// takes no argument.
func TestConfigSubcommandsRejectArgs(t *testing.T) {
	for _, sub := range []string{"ls", "view", "edit"} {
		t.Run(sub, func(t *testing.T) {
			configEnv(t)
			// A mutant that drops the guard would run the editor, so give it one
			// that returns at once instead of waiting on a terminal.
			t.Setenv("IQ_EDITOR", "true")
			root, _ := newRootCmd()
			_, err := runCmd(t, root, "config", sub, "extra")
			require.ErrorContains(t, err, `unknown command "extra"`)
		})
	}
}

// TestConfigCommandsReportACorruptConfig checks that each command which reads the
// config file stops with the load error. The commands run without the root, whose
// pre-run step would hit the same parse error first.
func TestConfigCommandsReportACorruptConfig(t *testing.T) {
	tests := []struct {
		name  string
		build func() *cobra.Command
		args  []string
	}{
		{name: "get", build: func() *cobra.Command { return newConfigGetCmd(&config{}) }, args: []string{"format"}},
		{name: "set", build: func() *cobra.Command { return newConfigSetCmd(&config{}) }, args: []string{"format", "yaml"}},
		{name: "delete", build: func() *cobra.Command { return newConfigSetCmd(&config{}) }, args: []string{"-D", "format"}},
		{name: "ls", build: func() *cobra.Command { return newConfigLsCmd(&config{}) }},
		{name: "view", build: func() *cobra.Command { return newConfigViewCmd(&config{}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := configEnv(t)
			require.NoError(t, os.WriteFile(p, []byte("not = [valid toml\n"), 0o600))
			_, err := runCmd(t, tt.build(), tt.args...)
			require.ErrorContains(t, err, "parse config")
		})
	}
}

// TestConfigUnknownSource checks the scoped commands name an unknown --src.
func TestConfigUnknownSource(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "get", args: []string{"--src", "nosuch", "config", "get", "format"}},
		{name: "ls", args: []string{"--src", "nosuch", "config", "ls"}},
		{name: "delete", args: []string{"--src", "nosuch", "config", "set", "-D", "format"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configEnv(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, "nosuch")
		})
	}
}

// TestConfigWriteErrors fails the confirmation or listing write of each config
// command and expects the error back.
func TestConfigWriteErrors(t *testing.T) {
	tests := []struct {
		name  string
		seed  func(t *testing.T)
		build func() *cobra.Command
		args  []string
	}{
		{
			name:  "get",
			build: func() *cobra.Command { return newConfigGetCmd(&config{}) },
			args:  []string{"format"},
		},
		{
			name: "ls",
			seed: func(t *testing.T) {
				cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
				require.NoError(t, cf.SetOption("", "format", "yaml"))
				require.NoError(t, cf.Save())
			},
			build: func() *cobra.Command { return newConfigLsCmd(&config{}) },
		},
		{
			name:  "view",
			build: func() *cobra.Command { return newConfigViewCmd(&config{}) },
		},
		{
			name:  "delete of an unset option",
			build: func() *cobra.Command { return newConfigSetCmd(&config{}) },
			args:  []string{"-D", "format"},
		},
		{
			name: "delete of a stored option",
			seed: func(t *testing.T) {
				cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
				require.NoError(t, cf.SetOption("", "format", "yaml"))
				require.NoError(t, cf.Save())
			},
			build: func() *cobra.Command { return newConfigSetCmd(&config{}) },
			args:  []string{"-D", "format"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configEnv(t)
			if tt.seed != nil {
				tt.seed(t)
			}
			c := tt.build()
			c.SetOut(&errAfter{0})
			c.SetErr(io.Discard)
			c.SetArgs(tt.args)
			require.ErrorContains(t, c.Execute(), "write failed")
		})
	}
}

// TestConfigDeleteReportsASaveFailure makes the save after a delete fail: the
// command must say so instead of reporting an option it did not remove.
func TestConfigDeleteReportsASaveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "iq.toml")
	t.Setenv(iqconfig.EnvConfig, p)
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.SetOption("", "format", "yaml"))
	require.NoError(t, cf.Save())
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "set", "-D", "format")
	require.ErrorContains(t, err, "create temp config")
	require.NotContains(t, out, "unset format")
}

// TestConfigEditRunsTheEditor checks the editor gets its own arguments and then
// the config path, and that its output reaches the command's stdout.
func TestConfigEditRunsTheEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the editor stub is a shell script")
	}
	p := configEnv(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ed.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho \"args: $*\"\n"), 0o755))
	t.Setenv("IQ_EDITOR", script+" --wait")

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "edit")
	require.NoError(t, err)
	require.Contains(t, out, "args: --wait "+p)
}

// TestConfigEditReportsAFailingEditor checks a non-zero editor exit fails the
// command and names the editor.
func TestConfigEditReportsAFailingEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the editor stub is a shell script")
	}
	configEnv(t)
	script := filepath.Join(t.TempDir(), "ed.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	t.Setenv("IQ_EDITOR", script)

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "edit")
	require.ErrorContains(t, err, "run editor")
}

// TestConfigEditReportsASaveFailure checks that a config file that cannot be
// created stops `edit` before the editor starts.
func TestConfigEditReportsASaveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	dir := t.TempDir()
	t.Setenv(iqconfig.EnvConfig, filepath.Join(dir, "iq.toml"))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "ed.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	t.Setenv("IQ_EDITOR", script)

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "edit")
	require.Error(t, err)
	require.NoFileExists(t, marker, "the editor must not start")
}

// TestConfigViewKeepsEverySection checks the redacted copy keeps the active
// source, the groups and the stored options, and prints nothing for an empty
// source table.
func TestConfigViewKeepsEverySection(t *testing.T) {
	configEnv(t)
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("a", "mongodb://h/db?collection=books"))
	require.NoError(t, cf.SetOption("", "format", "yaml"))
	cf.Active = "a"
	cf.Group = "all"
	require.NoError(t, cf.Save())

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "view")
	require.NoError(t, err)
	require.Contains(t, out, "active = 'a'")
	require.Contains(t, out, "group = 'all'")
	require.Contains(t, out, "format = 'yaml'")
}
