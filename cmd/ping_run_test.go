package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
)

// seedPingMix saves the dead source "dead" and the dump source "ok", which
// opens without a backend.
func seedPingMix(t *testing.T) {
	t.Helper()
	dump := filepath.Join(t.TempDir(), "d.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(mcpDump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("dead", "redis://127.0.0.1:1"))
	require.NoError(t, c.Add("ok", iqfile.URL(dump)))
	seedConfig(t, c)
}

func TestRunPingOutcome(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"an unknown source", []string{"nosuch"}, "unknown source or group: nosuch"},
		{"a failure before the last success", []string{"dead", "ok"}, "one or more sources unreachable"},
		{"a failure after a success", []string{"ok", "dead"}, "one or more sources unreachable"},
		{"all reachable", []string{"ok"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedPingMix(t)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(&cappedBuffer{})

			err := runPing(cmd, &config{timeout: 2 * time.Second}, tt.args, false)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestRunPingTableWriteError(t *testing.T) {
	seedPingMix(t)
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	w := &failingWriter{err: errors.New("disk full")}
	cmd.SetOut(w)

	err := runPing(cmd, &config{timeout: 2 * time.Second}, []string{"ok"}, false)

	require.ErrorIs(t, err, w.err)
}

// TestRunPingReturnsAConfigLoadError makes sure that a config file that does not
// parse stops the command before any check runs.
func TestRunPingReturnsAConfigLoadError(t *testing.T) {
	path := configEnv(t)
	require.NoError(t, os.WriteFile(path, []byte("sources = [not toml"), 0o600))
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	out := &cappedBuffer{}
	cmd.SetOut(out)

	err := runPing(cmd, &config{timeout: 2 * time.Second}, nil, true)

	require.Error(t, err)
	require.Empty(t, out.String())
}
