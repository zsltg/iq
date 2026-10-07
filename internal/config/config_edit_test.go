package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/config"
)

func newConfig(t *testing.T, handles ...string) *config.Config {
	t.Helper()
	c := &config.Config{Sources: map[string]config.Source{}}
	for _, h := range handles {
		require.NoError(t, c.Add(h, "redis://h"))
	}
	return c
}

func TestUnknownSourceErrorText(t *testing.T) {
	const want = `unknown source: "nope"`
	c := newConfig(t)
	require.EqualError(t, c.UseKeyring(" @nope "), want)
	require.EqualError(t, c.ClearKeyring(" @nope "), want)
	require.EqualError(t, c.SetSourceURL(" @nope ", "redis://h"), want)
	require.EqualError(t, c.Remove(" @nope "), want)
	require.EqualError(t, c.SetActive(" @nope "), want)
}

func TestSetSourceURLChecksTheSourceFirst(t *testing.T) {
	c := newConfig(t)
	err := c.SetSourceURL("nope", "  ")
	require.ErrorIs(t, err, config.ErrUnknownSource)
	require.NotErrorIs(t, err, config.ErrEmptyURL)
}

func TestRemoveAllUnknownText(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"two unknown names keep input order", []string{"b", "a"}, `unknown source: b, a`},
		{"a repeated unknown name is listed twice", []string{"x", "x"}, `unknown source: x, x`},
		{"names are cleaned", []string{" @x "}, `unknown source: x`},
		{"a group with no member is unknown", []string{"prod/"}, `unknown source: prod/`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newConfig(t, "keep")
			_, err := c.RemoveAll(tt.names)
			require.ErrorIs(t, err, config.ErrUnknownSource)
			require.EqualError(t, err, tt.want)
			require.Contains(t, c.Sources, "keep")
		})
	}
}

func TestHandleValidationText(t *testing.T) {
	tests := []struct {
		handle string
		want   string
	}{
		{"/", `invalid source name "/": misplaced '/'`},
		{"/a", `invalid source name "/a": misplaced '/'`},
		{"a/", `invalid source name "a/": misplaced '/'`},
		{"a//b", `invalid source name "a//b": misplaced '/'`},
		{"a/b//", `invalid source name "a/b//": misplaced '/'`},
		{"a b", `invalid source name "a b": only letters, digits, '.', '_', '-', '/' allowed`},
		{"a:b", `invalid source name "a:b": only letters, digits, '.', '_', '-', '/' allowed`},
	}
	for _, tt := range tests {
		t.Run(tt.handle, func(t *testing.T) {
			err := newConfig(t).Add(tt.handle, "redis://h")
			require.ErrorIs(t, err, config.ErrBadHandle)
			require.EqualError(t, err, tt.want)
		})
	}

	t.Run("deep valid handle", func(t *testing.T) {
		require.NoError(t, newConfig(t).Add("a/b/c", "redis://h"))
	})
}

func TestMoveGroupNeighbors(t *testing.T) {
	t.Run("a sibling prefix is not part of the group", func(t *testing.T) {
		c := newConfig(t, "prod/a", "prod2/x")
		require.NoError(t, c.SetGroup("prod2"))

		moved, err := c.Move("prod", "staging")
		require.NoError(t, err)
		require.Equal(t, []config.Rename{{Old: "prod/a", New: "staging/a"}}, moved)
		require.Contains(t, c.Sources, "prod2/x")
		require.Equal(t, "prod2", c.Group)
	})

	t.Run("an unrelated active group stays", func(t *testing.T) {
		c := newConfig(t, "prod/a", "dev/b")
		require.NoError(t, c.SetGroup("dev"))
		require.NoError(t, c.SetActive("dev/b"))

		_, err := c.Move("prod", "staging")
		require.NoError(t, err)
		require.Equal(t, "dev", c.Group)
		require.Equal(t, "dev/b", c.Active)
	})

	t.Run("collision text names the target", func(t *testing.T) {
		c := newConfig(t, "prod/books", "staging/books")
		_, err := c.Move("prod", "staging")
		require.EqualError(t, err, `source already exists: "staging/books"`)
		require.Contains(t, c.Sources, "prod/books", "nothing moves on a collision")
	})

	t.Run("a source move keeps an unrelated active source", func(t *testing.T) {
		c := newConfig(t, "a", "b")
		require.NoError(t, c.SetActive("b"))
		_, err := c.Move("a", "c")
		require.NoError(t, err)
		require.Equal(t, "b", c.Active)
	})
}

func TestRemoveKeepsUnrelatedState(t *testing.T) {
	c := newConfig(t, "a", "b", "prod/x")
	require.NoError(t, c.SetActive("b"))
	require.NoError(t, c.SetGroup("prod"))

	require.NoError(t, c.Remove("a"))
	require.Equal(t, "b", c.Active)
	require.Equal(t, "prod", c.Group)

	_, err := c.RemoveAll([]string{"a2"})
	require.Error(t, err)
	removed, err := c.RemoveAll([]string{"b"})
	require.NoError(t, err)
	require.Len(t, removed, 1)
	require.Empty(t, c.Active)
	require.Equal(t, "prod", c.Group)
}

func TestCountGroupCleansTheName(t *testing.T) {
	c := newConfig(t, "prod/a", "prod/b", "prod2/c")
	require.Equal(t, 2, c.CountGroup("@prod"))
	require.Equal(t, 2, c.CountGroup(" prod "))
	require.Equal(t, 0, c.CountGroup(""))
}
