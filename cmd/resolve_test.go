package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// seedConfig points config at a fresh temp file and saves c there.
func seedConfig(t *testing.T, c *iqconfig.Config) {
	t.Helper()
	t.Setenv(iqconfig.EnvConfig, filepath.Join(t.TempDir(), "iq.toml"))
	require.NoError(t, c.Save())
}

// resolveCmd builds a command carrying the --src and --collection flags that
// resolveSource reads, bound to cfg.
func resolveCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{Use: "iq", RunE: func(*cobra.Command, []string) error { return nil }}
	c.Flags().StringVarP(&cfg.src, "src", "s", "", "")
	c.Flags().StringVarP(&cfg.collection, "collection", "c", "", "")
	return c
}

func newSeed() *iqconfig.Config {
	return &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
}

func TestResolveSource(t *testing.T) {
	t.Run("--src selects source and its collection", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("books", "mongodb://h/db", "books"))
		seedConfig(t, c)

		cfg := &config{}
		cmd := resolveCmd(cfg)
		cfg.src = "books"
		require.NoError(t, resolveSource(cmd, cfg))
		require.Equal(t, "mongodb://h/db", cfg.url)
		require.Equal(t, "books", cfg.collection)
	})

	t.Run("active source used without --src", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h", ""))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		cfg := &config{}
		require.NoError(t, resolveSource(resolveCmd(cfg), cfg))
		require.Equal(t, "redis://h", cfg.url)
	})

	t.Run("--src overrides the active source", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h", ""))
		require.NoError(t, c.Add("books", "mongodb://h/db", "books"))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		cfg := &config{}
		cmd := resolveCmd(cfg)
		cfg.src = "books"
		require.NoError(t, resolveSource(cmd, cfg))
		require.Equal(t, "mongodb://h/db", cfg.url)
	})

	t.Run("unknown --src errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		cfg := &config{}
		cmd := resolveCmd(cfg)
		cfg.src = "nope"
		require.ErrorContains(t, resolveSource(cmd, cfg), "unknown source")
	})

	t.Run("no source selected errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		cfg := &config{}
		require.ErrorContains(t, resolveSource(resolveCmd(cfg), cfg), "no source selected")
	})

	t.Run("--collection overrides the source collection", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("books", "mongodb://h/db", "books"))
		require.NoError(t, c.SetActive("books"))
		seedConfig(t, c)

		cfg := &config{}
		cmd := resolveCmd(cfg)
		require.NoError(t, cmd.Flags().Set("collection", "override"))
		require.NoError(t, resolveSource(cmd, cfg))
		require.Equal(t, "override", cfg.collection)
	})

	t.Run("active group namespaces a bare --src name", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
		require.NoError(t, c.SetGroup("prod"))
		seedConfig(t, c)

		cfg := &config{}
		cmd := resolveCmd(cfg)
		cfg.src = "books"
		require.NoError(t, resolveSource(cmd, cfg))
		require.Equal(t, "mongodb://h/db", cfg.url)
		require.Equal(t, "books", cfg.collection)
	})
}
