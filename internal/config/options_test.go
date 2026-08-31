package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/config"
)

func newSourceConfig(t *testing.T) *config.Config {
	t.Helper()
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	return c
}

func TestSetOptionBase(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.SetOption("", "format", "yaml"))

	got, ok := c.GetOption("", "format")
	require.True(t, ok)
	require.Equal(t, "yaml", got)
	require.Equal(t, "yaml", c.Options["format"])
}

func TestSetOptionEmptyKey(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.ErrorIs(t, c.SetOption("", "", "yaml"), config.ErrEmptyOption)
}

func TestSetOptionPerSource(t *testing.T) {
	c := newSourceConfig(t)
	// A group-relative "@"-prefixed name resolves to the stored handle.
	require.NoError(t, c.SetOption("@prod/books", "timeout", "30s"))

	require.Equal(t, "30s", c.Sources["prod/books"].Options["timeout"])
	got, ok := c.GetOption("prod/books", "timeout")
	require.True(t, ok)
	require.Equal(t, "30s", got)
}

func TestSetOptionUnknownSource(t *testing.T) {
	c := newSourceConfig(t)
	require.ErrorIs(t, c.SetOption("nope", "format", "yaml"), config.ErrUnknownSource)
}

func TestGetOptionMisses(t *testing.T) {
	c := newSourceConfig(t)
	_, ok := c.GetOption("", "format")
	require.False(t, ok, "unset base option")
	_, ok = c.GetOption("prod/books", "format")
	require.False(t, ok, "unset source option")
	_, ok = c.GetOption("nope", "format")
	require.False(t, ok, "unknown source reads as unset, no panic")
}

func TestUnsetOption(t *testing.T) {
	t.Run("base existed", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.SetOption("", "compact", "true"))
		existed, err := c.UnsetOption("", "compact")
		require.NoError(t, err)
		require.True(t, existed)
		_, ok := c.GetOption("", "compact")
		require.False(t, ok)
	})

	t.Run("base absent", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		existed, err := c.UnsetOption("", "compact")
		require.NoError(t, err)
		require.False(t, existed)
	})

	t.Run("source existed", func(t *testing.T) {
		c := newSourceConfig(t)
		require.NoError(t, c.SetOption("prod/books", "format", "jsonl"))
		existed, err := c.UnsetOption("prod/books", "format")
		require.NoError(t, err)
		require.True(t, existed)
		require.Empty(t, c.Sources["prod/books"].Options["format"])
	})

	t.Run("unknown source", func(t *testing.T) {
		c := newSourceConfig(t)
		_, err := c.UnsetOption("nope", "format")
		require.ErrorIs(t, err, config.ErrUnknownSource)
	})
}

func TestOptionList(t *testing.T) {
	c := newSourceConfig(t)
	require.NoError(t, c.SetOption("", "format", "yaml"))
	require.NoError(t, c.SetOption("", "compact", "true"))
	require.NoError(t, c.SetOption("prod/books", "timeout", "30s"))

	base, err := c.OptionList("")
	require.NoError(t, err)
	require.Equal(t, []config.Option{
		{Key: "compact", Value: "true"},
		{Key: "format", Value: "yaml"},
	}, base, "sorted by key")

	src, err := c.OptionList("prod/books")
	require.NoError(t, err)
	require.Equal(t, []config.Option{{Key: "timeout", Value: "30s"}}, src)

	_, err = c.OptionList("nope")
	require.ErrorIs(t, err, config.ErrUnknownSource)
}

func TestOptionListSortsByKey(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	// Inserted so that no rotation of the insertion order is already sorted.
	for _, k := range []string{"beta", "alpha", "gamma", "delta", "epsilon"} {
		require.NoError(t, c.SetOption("", k, k+"-value"))
	}
	want := []config.Option{
		{Key: "alpha", Value: "alpha-value"},
		{Key: "beta", Value: "beta-value"},
		{Key: "delta", Value: "delta-value"},
		{Key: "epsilon", Value: "epsilon-value"},
		{Key: "gamma", Value: "gamma-value"},
	}

	// Map iteration order is randomized per range, so a comparison that never
	// orders anything returns the keys in a different order each call; repeat
	// until landing on the sorted one by chance is impossible.
	for range 10 {
		opts, err := c.OptionList("")
		require.NoError(t, err)
		require.Equal(t, want, opts)
	}
}

func TestOptionsRoundTrip(t *testing.T) {
	tempConfig(t)

	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.SetOption("", "format", "yaml"))
	require.NoError(t, c.SetOption("prod/books", "timeout", "30s"))
	require.NoError(t, c.Save())

	got, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "yaml", got.Options["format"])
	require.Equal(t, "30s", got.Sources["prod/books"].Options["timeout"])
	require.Equal(t, c, got)
}
