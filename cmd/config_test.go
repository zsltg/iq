package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	require.NoError(t, cf.Add(handle, "mongodb://h/db", "books"))
	require.NoError(t, cf.Save())
}

func TestConfigLocation(t *testing.T) {
	p := configEnv(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "config", "location")
	require.NoError(t, err)
	require.Equal(t, p, strings.TrimSpace(out))
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

func TestConfigUnset(t *testing.T) {
	configEnv(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "config", "set", "compact", "true")
	require.NoError(t, err)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "config", "unset", "compact")
	require.NoError(t, err)
	require.Contains(t, out, "unset compact")

	root3, _ := newRootCmd()
	out, err = runCmd(t, root3, "config", "unset", "compact")
	require.NoError(t, err)
	require.Contains(t, out, "was not set")
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
	require.NoError(t, cf.Add("cache", "redis://u:secret@h:6379/0", ""))
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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho ran > '"+sentinel+"'\n"), 0o755))
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
		require.NoError(t, cf.Add("prod", "mongodb://h/db", "books"))
		require.NoError(t, cf.SetOption("", "format", "yaml"))
		require.NoError(t, cf.SetOption("prod", "format", "jsonl"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		cfg.src = "prod"
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "jsonl", cfg.format)
	})

	t.Run("per-source persistent option (log.level) is applied", func(t *testing.T) {
		configEnv(t)
		cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
		require.NoError(t, cf.Add("prod", "mongodb://h/db", "books"))
		require.NoError(t, cf.SetOption("prod", "log.level", "WARN"))
		require.NoError(t, cf.Save())

		root, cfg := newRootCmd()
		cfg.src = "prod"
		require.NoError(t, applyStoredOptions(root, cfg))
		require.Equal(t, "WARN", cfg.logLevel)
	})
}
