package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/config"
)

// tempConfig points config at a fresh temp file and returns its path.
func tempConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "iq.toml")
	t.Setenv(config.EnvConfig, p)
	return p
}

func TestPath(t *testing.T) {
	t.Run("env override", func(t *testing.T) {
		t.Setenv(config.EnvConfig, "/custom/iq.toml")
		p, err := config.Path()
		require.NoError(t, err)
		require.Equal(t, "/custom/iq.toml", p)
	})

	t.Run("default under user config dir", func(t *testing.T) {
		t.Setenv(config.EnvConfig, "")
		p, err := config.Path()
		require.NoError(t, err)
		require.True(t, filepath.IsAbs(p))
		require.Equal(t, filepath.Join("iq", "iq.toml"), filepath.Join(filepath.Base(filepath.Dir(p)), filepath.Base(p)))
	})
}

func TestLoadMissing(t *testing.T) {
	tempConfig(t)
	c, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, c.Sources)
	require.Empty(t, c.Sources)
	require.Empty(t, c.Active)
	require.Empty(t, c.Group)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := tempConfig(t)

	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("cache", "redis://u:p@localhost:6379/0", ""))
	require.NoError(t, c.Add("prod/books", "mongodb://localhost:27017/app", "books"))
	require.NoError(t, c.SetActive("cache"))
	require.NoError(t, c.SetGroup("prod"))
	require.NoError(t, c.Save())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), "iq-*.toml"))
	require.NoError(t, err)
	require.Empty(t, leftovers, "atomic save left a temp file behind")

	got, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, c, got)
}

func TestSaveCreatesDir(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "iq.toml")
	t.Setenv(config.EnvConfig, nested)

	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("cache", "redis://localhost:6379/0", ""))
	require.NoError(t, c.Save())

	_, err := os.Stat(nested)
	require.NoError(t, err)
}

func TestAdd(t *testing.T) {
	tests := []struct {
		name       string
		handle     string
		url        string
		wantErr    error
		wantStored string
	}{
		{name: "ok", handle: "books", url: "mongodb://h/db", wantStored: "books"},
		{name: "leading @ stripped", handle: "@books", url: "redis://h", wantStored: "books"},
		{name: "grouped", handle: "prod/db", url: "redis://h", wantStored: "prod/db"},
		{name: "rich chars", handle: "A-b_C.1/d", url: "redis://h", wantStored: "A-b_C.1/d"},
		{name: "empty name", handle: "", url: "redis://h", wantErr: config.ErrEmptyHandle},
		{name: "space in name", handle: "a b", url: "redis://h", wantErr: config.ErrBadHandle},
		{name: "trailing slash", handle: "a/", url: "redis://h", wantErr: config.ErrBadHandle},
		{name: "empty url", handle: "books", url: "", wantErr: config.ErrEmptyURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &config.Config{Sources: map[string]config.Source{}}
			err := c.Add(tt.handle, tt.url, "")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			_, ok := c.Sources[tt.wantStored]
			require.True(t, ok, "expected source %q stored", tt.wantStored)
		})
	}
}

func TestAddDuplicate(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("books", "redis://h", ""))
	err := c.Add("books", "redis://other", "")
	require.ErrorIs(t, err, config.ErrDuplicate)
}

func TestUseKeyring(t *testing.T) {
	t.Run("marks source keyring-backed", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("sec", "redis://u@h", ""))
		require.NoError(t, c.UseKeyring("@sec"))
		require.True(t, c.Sources["sec"].Keyring)
	})

	t.Run("unknown source", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.UseKeyring("nope"), config.ErrUnknownSource)
	})
}

func TestKeyringFieldRoundTrips(t *testing.T) {
	tempConfig(t)
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("sec", "redis://u@h", ""))
	require.NoError(t, c.UseKeyring("sec"))
	require.NoError(t, c.Save())

	got, err := config.Load()
	require.NoError(t, err)
	require.True(t, got.Sources["sec"].Keyring)
}

func TestCleanHandle(t *testing.T) {
	require.Equal(t, "books", config.CleanHandle("  @books "))
	require.Equal(t, "prod/books", config.CleanHandle("prod/books"))
}

func TestRemove(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.Remove("nope"), config.ErrUnknownSource)
	})

	t.Run("clears dangling active", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("books", "redis://h", ""))
		require.NoError(t, c.SetActive("books"))
		require.NoError(t, c.Remove("books"))
		require.Empty(t, c.Active)
	})

	t.Run("clears dangling group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "redis://h", ""))
		require.NoError(t, c.SetGroup("prod"))
		require.NoError(t, c.Remove("prod/books"))
		require.Empty(t, c.Group)
	})
}

func TestSetActive(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.SetActive("nope"), config.ErrUnknownSource)
	})

	t.Run("stores full handle via group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/db", "redis://h", ""))
		require.NoError(t, c.SetGroup("prod"))
		require.NoError(t, c.SetActive("db"))
		require.Equal(t, "prod/db", c.Active)
	})
}

func TestSetGroup(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("prod/db", "redis://h", ""))

	require.ErrorIs(t, c.SetGroup("dev"), config.ErrUnknownGroup)

	require.NoError(t, c.SetGroup("prod"))
	require.Equal(t, "prod", c.Group)

	require.NoError(t, c.SetGroup(""))
	require.Empty(t, c.Group)
}

func TestResolve(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("cache", "redis://top", ""))
	require.NoError(t, c.Add("prod/cache", "redis://prod", ""))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
	require.NoError(t, c.Add("widgets", "redis://widgets", "")) // top-level only

	t.Run("absolute handle", func(t *testing.T) {
		s, full, ok := c.Resolve("prod/books")
		require.True(t, ok)
		require.Equal(t, "prod/books", full)
		require.Equal(t, "books", s.Collection)
	})

	t.Run("active group prefers group member", func(t *testing.T) {
		require.NoError(t, c.SetGroup("prod"))
		s, full, ok := c.Resolve("cache")
		require.True(t, ok)
		require.Equal(t, "prod/cache", full)
		require.Equal(t, "redis://prod", s.URL)
	})

	t.Run("active group falls back to top level", func(t *testing.T) {
		require.NoError(t, c.SetGroup("prod"))
		s, full, ok := c.Resolve("widgets") // only exists at top level
		require.True(t, ok)
		require.Equal(t, "widgets", full)
		require.Equal(t, "redis://widgets", s.URL)
	})

	t.Run("miss", func(t *testing.T) {
		c.Group = ""
		_, _, ok := c.Resolve("nope")
		require.False(t, ok)
	})
}
