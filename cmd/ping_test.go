package cmd

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestPingTargets(t *testing.T) {
	seed := func() *iqconfig.Config {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h", ""))
		require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
		require.NoError(t, c.Add("prod/cache", "redis://h", ""))
		return c
	}

	t.Run("no args uses active", func(t *testing.T) {
		c := seed()
		require.NoError(t, c.SetActive("cache"))
		targets, err := pingTargets(c, nil)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		require.Equal(t, "cache", targets[0].handle)
	})

	t.Run("no args without active errors", func(t *testing.T) {
		_, err := pingTargets(seed(), nil)
		require.ErrorContains(t, err, "no active source")
	})

	t.Run("named source", func(t *testing.T) {
		targets, err := pingTargets(seed(), []string{"cache"})
		require.NoError(t, err)
		require.Len(t, targets, 1)
	})

	t.Run("group expands to members", func(t *testing.T) {
		targets, err := pingTargets(seed(), []string{"prod"})
		require.NoError(t, err)
		require.Len(t, targets, 2)
	})

	t.Run("unknown errors", func(t *testing.T) {
		_, err := pingTargets(seed(), []string{"nope"})
		require.ErrorContains(t, err, "unknown source or group")
	})
}

func TestPingUnreachable(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("dead", "redis://127.0.0.1:1", ""))
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
	require.NoError(t, c.Add("live", url, ""))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 3 * time.Second}
	out, err := runCmd(t, newPingCmd(cfg))
	require.NoError(t, err)
	require.Contains(t, out, "ok")
}
