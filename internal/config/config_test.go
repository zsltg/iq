package config_test

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestLoadMigratesLegacyCollection(t *testing.T) {
	t.Run("folds the field into the url query", func(t *testing.T) {
		p := tempConfig(t)
		raw := "[sources.books]\n" +
			"url = \"mongodb://h/db\"\n" +
			"collection = \"orders\"\n"
		require.NoError(t, os.WriteFile(p, []byte(raw), 0o600))

		c, err := config.Load()
		require.NoError(t, err)
		got := c.Sources["books"]
		require.Equal(t, "mongodb://h/db?collection=orders", got.URL)
		require.Empty(t, got.Collection)

		// The migration persists: a subsequent Save drops the legacy field.
		require.NoError(t, c.Save())
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		require.NotContains(t, string(data), "collection = ")
		require.Contains(t, string(data), "collection=orders")
	})

	t.Run("leaves a url that already carries a collection", func(t *testing.T) {
		p := tempConfig(t)
		raw := "[sources.books]\n" +
			"url = \"mongodb://h/db?collection=already\"\n" +
			"collection = \"orders\"\n"
		require.NoError(t, os.WriteFile(p, []byte(raw), 0o600))

		c, err := config.Load()
		require.NoError(t, err)
		require.Equal(t, "mongodb://h/db?collection=already", c.Sources["books"].URL)
		require.Empty(t, c.Sources["books"].Collection)
	})
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := tempConfig(t)

	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("cache", "redis://u:p@localhost:6379/0"))
	require.NoError(t, c.Add("prod/books", "mongodb://localhost:27017/app?collection=books"))
	require.NoError(t, c.SetActive("cache"))
	require.NoError(t, c.SetGroup("prod"))
	require.NoError(t, c.Save())

	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // Windows keeps no Unix mode bits to assert on.
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

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
	require.NoError(t, c.Add("cache", "redis://localhost:6379/0"))
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
			err := c.Add(tt.handle, tt.url)
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
	require.NoError(t, c.Add("books", "redis://h"))
	err := c.Add("books", "redis://other")
	require.ErrorIs(t, err, config.ErrDuplicate)
}

func TestUseKeyring(t *testing.T) {
	t.Run("marks source keyring-backed", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("sec", "redis://u@h"))
		require.NoError(t, c.UseKeyring("@sec"))
		require.True(t, c.Sources["sec"].Keyring)
	})

	t.Run("unknown source", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.UseKeyring("nope"), config.ErrUnknownSource)
	})
}

func TestClearKeyring(t *testing.T) {
	t.Run("clears the keyring flag", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("sec", "redis://u@h"))
		require.NoError(t, c.UseKeyring("sec"))
		require.NoError(t, c.ClearKeyring("@sec"))
		require.False(t, c.Sources["sec"].Keyring)
	})

	t.Run("unknown source", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.ClearKeyring("nope"), config.ErrUnknownSource)
	})
}

func TestSetSourceURL(t *testing.T) {
	t.Run("replaces the url, keeping other fields", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("sec", "redis://u:p@h"))
		require.NoError(t, c.UseKeyring("sec"))
		require.NoError(t, c.SetSourceURL("@sec", "redis://u@h"))
		got := c.Sources["sec"]
		require.Equal(t, "redis://u@h", got.URL)
		require.True(t, got.Keyring)
	})

	t.Run("blank url", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("sec", "redis://u@h"))
		require.ErrorIs(t, c.SetSourceURL("sec", "  "), config.ErrEmptyURL)
	})

	t.Run("unknown source", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.ErrorIs(t, c.SetSourceURL("nope", "redis://h"), config.ErrUnknownSource)
	})
}

func TestKeyringFieldRoundTrips(t *testing.T) {
	tempConfig(t)
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("sec", "redis://u@h"))
	require.NoError(t, c.UseKeyring("sec"))
	require.NoError(t, c.Save())

	got, err := config.Load()
	require.NoError(t, err)
	require.True(t, got.Sources["sec"].Keyring)
}

func TestMoveSource(t *testing.T) {
	t.Run("rename keeps data and follows active", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.SetActive("books"))

		moved, err := c.Move("books", "library")
		require.NoError(t, err)
		require.Equal(t, []config.Rename{{Old: "books", New: "library"}}, moved)
		require.NotContains(t, c.Sources, "books")
		require.Equal(t, "mongodb://h/db?collection=books", c.Sources["library"].URL)
		require.Equal(t, "library", c.Active)
	})

	t.Run("move into group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("books", "redis://h"))

		_, err := c.Move("books", "prod/books")
		require.NoError(t, err)
		require.Contains(t, c.Sources, "prod/books")
	})

	t.Run("moving last member clears active group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "redis://h"))
		require.NoError(t, c.SetGroup("prod"))

		_, err := c.Move("prod/books", "books")
		require.NoError(t, err)
		require.Empty(t, c.Group)
	})

	t.Run("unknown source", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		_, err := c.Move("nope", "other")
		require.ErrorIs(t, err, config.ErrUnknownSource)
	})

	t.Run("duplicate target", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("a", "redis://h"))
		require.NoError(t, c.Add("b", "redis://h"))
		_, err := c.Move("a", "b")
		require.ErrorIs(t, err, config.ErrDuplicate)
	})

	t.Run("malformed target", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("a", "redis://h"))
		_, err := c.Move("a", "bad/")
		require.ErrorIs(t, err, config.ErrBadHandle)
	})

	t.Run("same handle is a no-op", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("a", "redis://h"))
		moved, err := c.Move("a", "a")
		require.NoError(t, err)
		require.Empty(t, moved)
		require.Contains(t, c.Sources, "a")
	})
}

func TestMoveGroup(t *testing.T) {
	t.Run("rewrites every member and follows active", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.Add("prod/cache", "redis://h"))
		require.NoError(t, c.SetActive("prod/cache"))
		require.NoError(t, c.SetGroup("prod"))

		moved, err := c.Move("prod", "staging")
		require.NoError(t, err)
		require.Len(t, moved, 2)
		require.Contains(t, c.Sources, "staging/books")
		require.Contains(t, c.Sources, "staging/cache")
		require.NotContains(t, c.Sources, "prod/books")
		require.Equal(t, "staging/cache", c.Active)
		require.Equal(t, "staging", c.Group)
	})

	t.Run("single active member follows the move", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/only", "redis://h"))
		require.NoError(t, c.SetActive("prod/only"))
		require.NoError(t, c.SetGroup("prod"))

		_, err := c.Move("prod", "staging")
		require.NoError(t, err)
		require.Equal(t, "staging/only", c.Active)
		require.Equal(t, "staging", c.Group)
	})

	t.Run("nested subgroup re-prefixes and follows active group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/eu/books", "redis://h"))
		require.NoError(t, c.SetGroup("prod/eu"))

		_, err := c.Move("prod", "staging")
		require.NoError(t, err)
		require.Contains(t, c.Sources, "staging/eu/books")
		require.Equal(t, "staging/eu", c.Group)
	})

	t.Run("duplicate target member", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "redis://h"))
		require.NoError(t, c.Add("staging/books", "redis://h"))
		_, err := c.Move("prod", "staging")
		require.ErrorIs(t, err, config.ErrDuplicate)
	})
}

func TestRemoveAll(t *testing.T) {
	newCfg := func() *config.Config {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("cache", "redis://h"))
		require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
		require.NoError(t, c.Add("prod/cache", "redis://h"))
		return c
	}

	t.Run("multiple sources", func(t *testing.T) {
		c := newCfg()
		removed, err := c.RemoveAll([]string{"cache", "prod/books"})
		require.NoError(t, err)
		require.Len(t, removed, 2)
		require.NotContains(t, c.Sources, "cache")
		require.NotContains(t, c.Sources, "prod/books")
		require.Contains(t, c.Sources, "prod/cache")
	})

	t.Run("group removes all members", func(t *testing.T) {
		c := newCfg()
		removed, err := c.RemoveAll([]string{"prod"})
		require.NoError(t, err)
		require.Len(t, removed, 2)
		require.Contains(t, c.Sources, "cache")
		require.NotContains(t, c.Sources, "prod/books")
		require.NotContains(t, c.Sources, "prod/cache")
	})

	t.Run("mixed source and group", func(t *testing.T) {
		c := newCfg()
		removed, err := c.RemoveAll([]string{"cache", "prod"})
		require.NoError(t, err)
		require.Len(t, removed, 3)
		require.Empty(t, c.Sources)
	})

	t.Run("overlap de-duplicates", func(t *testing.T) {
		c := newCfg()
		removed, err := c.RemoveAll([]string{"prod", "prod/books"})
		require.NoError(t, err)
		require.Len(t, removed, 2)
	})

	t.Run("unknown among valid removes nothing", func(t *testing.T) {
		c := newCfg()
		_, err := c.RemoveAll([]string{"cache", "nope"})
		require.ErrorIs(t, err, config.ErrUnknownSource)
		require.ErrorContains(t, err, "nope")
		require.Contains(t, c.Sources, "cache", "atomic: nothing removed on error")
	})

	t.Run("clears active and group", func(t *testing.T) {
		c := newCfg()
		require.NoError(t, c.SetActive("prod/cache"))
		require.NoError(t, c.SetGroup("prod"))
		_, err := c.RemoveAll([]string{"prod"})
		require.NoError(t, err)
		require.Empty(t, c.Active)
		require.Empty(t, c.Group)
	})

	t.Run("clears active when the only removed source was active", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("only", "redis://h"))
		require.NoError(t, c.SetActive("only"))
		_, err := c.RemoveAll([]string{"only"})
		require.NoError(t, err)
		require.Empty(t, c.Active)
	})

	t.Run("removed sources come out sorted", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("b", "redis://h"))
		require.NoError(t, c.Add("a", "redis://h"))
		require.NoError(t, c.Add("c", "redis://h"))
		removed, err := c.RemoveAll([]string{"c", "a", "b"})
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b", "c"},
			[]string{removed[0].Handle, removed[1].Handle, removed[2].Handle})
	})
}

func TestGroups(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("top", "redis://h"))
	require.NoError(t, c.Add("prod/books", "redis://h"))
	require.NoError(t, c.Add("prod/eu/cache", "redis://h"))
	require.NoError(t, c.Add("dev/cache", "redis://h"))

	require.Equal(t, []string{"dev", "prod", "prod/eu"}, c.Groups())
	require.Equal(t, 2, c.CountGroup("prod")) // prod/books, prod/eu/cache
	require.Equal(t, 1, c.CountGroup("prod/eu"))
	require.Equal(t, 0, c.CountGroup("nope"))
}

func TestGroupsEmpty(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("top", "redis://h"))
	require.Empty(t, c.Groups())
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
		require.NoError(t, c.Add("books", "redis://h"))
		require.NoError(t, c.SetActive("books"))
		require.NoError(t, c.Remove("books"))
		require.Empty(t, c.Active)
	})

	t.Run("clears dangling group", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "redis://h"))
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
		require.NoError(t, c.Add("prod/db", "redis://h"))
		require.NoError(t, c.SetGroup("prod"))
		require.NoError(t, c.SetActive("db"))
		require.Equal(t, "prod/db", c.Active)
	})
}

func TestSetGroup(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("prod/db", "redis://h"))

	require.ErrorIs(t, c.SetGroup("dev"), config.ErrUnknownGroup)

	require.NoError(t, c.SetGroup("prod"))
	require.Equal(t, "prod", c.Group)

	require.NoError(t, c.SetGroup(""))
	require.Empty(t, c.Group)
}

func TestResolve(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	require.NoError(t, c.Add("cache", "redis://top"))
	require.NoError(t, c.Add("prod/cache", "redis://prod"))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.Add("widgets", "redis://widgets")) // top-level only

	t.Run("absolute handle", func(t *testing.T) {
		s, full, ok := c.Resolve("prod/books")
		require.True(t, ok)
		require.Equal(t, "prod/books", full)
		require.Equal(t, "mongodb://h/db?collection=books", s.URL)
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
