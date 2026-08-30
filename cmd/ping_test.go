package cmd

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestPingTargets(t *testing.T) {
	seed := func() *iqconfig.Config {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.Add("prod/cache", "redis://h"))
		return c
	}

	t.Run("no args uses active", func(t *testing.T) {
		c := seed()
		require.NoError(t, c.SetActive("cache"))
		targets, err := pingTargets(c, nil, false)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		require.Equal(t, "cache", targets[0].handle)
	})

	t.Run("no args without active errors", func(t *testing.T) {
		_, err := pingTargets(seed(), nil, false)
		require.ErrorContains(t, err, "no active source")
	})

	t.Run("named source", func(t *testing.T) {
		targets, err := pingTargets(seed(), []string{"cache"}, false)
		require.NoError(t, err)
		require.Len(t, targets, 1)
	})

	t.Run("group expands to members", func(t *testing.T) {
		targets, err := pingTargets(seed(), []string{"prod"}, false)
		require.NoError(t, err)
		require.Len(t, targets, 2)
	})

	t.Run("unknown errors", func(t *testing.T) {
		_, err := pingTargets(seed(), []string{"nope"}, false)
		require.ErrorContains(t, err, "unknown source or group")
	})

	t.Run("all takes every source sorted, ignoring the active one", func(t *testing.T) {
		c := seed()
		require.NoError(t, c.SetActive("cache"))
		targets, err := pingTargets(c, nil, true)
		require.NoError(t, err)
		handles := make([]string, 0, len(targets))
		for _, tgt := range targets {
			handles = append(handles, tgt.handle)
			require.NotEmpty(t, tgt.source.URL)
		}
		require.Equal(t, []string{"cache", "prod/books", "prod/cache"}, handles)
	})

	t.Run("all needs no active source", func(t *testing.T) {
		targets, err := pingTargets(seed(), nil, true)
		require.NoError(t, err)
		require.Len(t, targets, 3)
	})

	t.Run("all with args errors", func(t *testing.T) {
		_, err := pingTargets(seed(), []string{"cache"}, true)
		require.ErrorContains(t, err, "--all pings every source")
	})

	t.Run("all without saved sources errors", func(t *testing.T) {
		_, err := pingTargets(newSeed(), nil, true)
		require.ErrorContains(t, err, "no sources")
	})
}

func TestPingAllFlag(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("dead", "redis://127.0.0.1:1"))
	require.NoError(t, c.Add("prod/alsodead", "redis://127.0.0.1:1"))
	require.NoError(t, c.SetActive("dead"))
	seedConfig(t, c)

	t.Run("all reaches every source", func(t *testing.T) {
		out, err := runCmd(t, newPingCmd(&config{timeout: 2 * time.Second}), "--all")
		require.Error(t, err)
		require.Contains(t, out, "dead")
		require.Contains(t, out, "prod/alsodead")
	})

	t.Run("without all only the active source", func(t *testing.T) {
		out, err := runCmd(t, newPingCmd(&config{timeout: 2 * time.Second}))
		require.Error(t, err)
		require.Contains(t, out, "dead")
		require.NotContains(t, out, "prod/alsodead")
	})
}

func TestPingUnreachable(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("dead", "redis://127.0.0.1:1"))
	seedConfig(t, c)

	cfg := &config{timeout: 2 * time.Second}
	out, err := runCmd(t, newPingCmd(cfg), "dead")
	require.Error(t, err)
	require.Contains(t, out, "dead")
	require.Contains(t, out, "error")
}

func TestPingIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 3 * time.Second}
	out, err := runCmd(t, newPingCmd(cfg))
	require.NoError(t, err)
	require.Contains(t, out, "ok")
}

// TestRedactErr proves the URI is masked only when the message carries it, and
// that an empty URI (a cross-source run resolves none) leaves the error chain
// untouched instead of flattening it: strings.Contains matches "" everywhere.
func TestRedactErr(t *testing.T) {
	sentinel := errors.New("boom")
	tests := []struct {
		name      string
		err       error
		rawURL    string
		wantMsg   string
		keepChain bool
	}{
		{"uri in message is masked", fmt.Errorf("dial redis://u:hunter2@h:6379/0: %w", sentinel), "redis://u:hunter2@h:6379/0", "dial redis://u:xxxxx@h:6379/0: boom", false},
		{"uri absent keeps the error", fmt.Errorf("dial: %w", sentinel), "redis://u:hunter2@h:6379/0", "dial: boom", true},
		{"empty uri keeps the chain", fmt.Errorf("dial: %w", sentinel), "", "dial: boom", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactErr(tc.err, tc.rawURL)
			require.EqualError(t, got, tc.wantMsg)
			require.Equal(t, tc.keepChain, errors.Is(got, sentinel))
		})
	}
}
