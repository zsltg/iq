package cmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// seedConfig points config at a fresh temp file and saves c there.
func seedConfig(t *testing.T, c *iqconfig.Config) {
	t.Helper()
	t.Setenv(iqconfig.EnvConfig, filepath.Join(t.TempDir(), "iq.toml"))
	require.NoError(t, c.Save())
}

func newSeed() *iqconfig.Config {
	return &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
}

func TestResolveSource(t *testing.T) {
	t.Run("--src selects a source; its default collection stays in the url", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		seedConfig(t, c)

		cfg := &config{src: "books"}
		require.NoError(t, resolveSource(cfg))
		require.Equal(t, "mongodb://h/db?collection=books", cfg.url)
		require.Empty(t, cfg.address)
		require.Equal(t, "books", mongoCollection(cfg))
	})

	t.Run("dotted --src sets the address override", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		seedConfig(t, c)

		cfg := &config{src: "books.chapters"}
		require.NoError(t, resolveSource(cfg))
		require.Equal(t, "chapters", cfg.address)
		require.Equal(t, "chapters", mongoCollection(cfg))
	})

	t.Run("active source used without --src", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		cfg := &config{}
		require.NoError(t, resolveSource(cfg))
		require.Equal(t, "redis://h", cfg.url)
	})

	t.Run("--src overrides the active source", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		cfg := &config{src: "books"}
		require.NoError(t, resolveSource(cfg))
		require.Equal(t, "mongodb://h/db?collection=books", cfg.url)
	})

	t.Run("unknown --src errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		cfg := &config{src: "nope"}
		require.ErrorContains(t, resolveSource(cfg), "unknown source")
	})

	t.Run("no source selected errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		require.ErrorContains(t, resolveSource(&config{}), "no source selected")
	})

	t.Run("redis rejects a dotted address", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		seedConfig(t, c)

		cfg := &config{src: "cache.foo"}
		require.ErrorContains(t, resolveSource(cfg), "no collections")
	})

	t.Run("active group namespaces a bare --src name", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.SetGroup("prod"))
		seedConfig(t, c)

		cfg := &config{src: "books"}
		require.NoError(t, resolveSource(cfg))
		require.Equal(t, "mongodb://h/db?collection=books", cfg.url)
		require.Equal(t, "books", mongoCollection(cfg))
	})
}

func TestSplitSourceArg(t *testing.T) {
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{
		"books":     {URL: "mongodb://h/db?collection=books"},
		"a.b":       {URL: "mongodb://h/db"},
		"cache":     {URL: "redis://h"},
		"prod/blog": {URL: "mongodb://h/db"},
	}}
	tests := []struct {
		name       string
		arg        string
		wantName   string
		wantColl   string
		wantHasCol bool
	}{
		{"empty falls through to caller", "", "", "", false},
		{"plain known source", "books", "books", "", false},
		{"source and collection split on last dot", "books.chapters", "books", "chapters", true},
		{"dotted handle resolves whole", "a.b", "a.b", "", false},
		{"at-prefixed splits, name keeps the at", "@books.chapters", "@books", "chapters", true},
		{"unknown bare name passes through", "nope", "nope", "", false},
		{"leading dot is not a split", ".x", ".x", "", false},
		{"trailing dot is not a split", "x.", "x.", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, coll, has := splitSourceArg(cf, tt.arg)
			require.Equal(t, tt.wantName, name)
			require.Equal(t, tt.wantColl, coll)
			require.Equal(t, tt.wantHasCol, has)
		})
	}
}

func TestResolveInspectSource(t *testing.T) {
	t.Run("positional source overrides the active source", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		cfg := &config{}
		require.NoError(t, resolveInspectSource(cfg, "books"))
		require.Equal(t, "mongodb://h/db?collection=books", cfg.url)
		require.Empty(t, cfg.address)
		require.Equal(t, "books", mongoCollection(cfg))
	})

	t.Run("dot addressing overrides the url default collection", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.SetActive("books"))
		seedConfig(t, c)

		cfg := &config{}
		require.NoError(t, resolveInspectSource(cfg, "books.chapters"))
		require.Equal(t, "chapters", cfg.address)
		require.Equal(t, "chapters", mongoCollection(cfg))
	})

	t.Run("no source selected errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		require.ErrorContains(t, resolveInspectSource(&config{}, ""), "no source selected")
	})

	t.Run("unknown positional source errors", func(t *testing.T) {
		seedConfig(t, newSeed())
		require.ErrorContains(t, resolveInspectSource(&config{}, "nope"), "unknown source")
	})

	t.Run("redis rejects a collection suffix", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.SetActive("cache"))
		seedConfig(t, c)

		require.ErrorContains(t, resolveInspectSource(&config{}, "cache.foo"), "no collections")
	})
}
