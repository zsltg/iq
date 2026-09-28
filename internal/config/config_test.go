package config_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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

func TestPathWithoutUserConfigDir(t *testing.T) {
	// With no override and no home-like variable on any platform, os.UserConfigDir
	// fails and Path must surface that rather than join a relative fallback.
	t.Setenv(config.EnvConfig, "")
	t.Setenv("XDG_CONFIG_HOME", "") // unix
	t.Setenv("HOME", "")            // unix and darwin
	t.Setenv("AppData", "")         // windows

	p, err := config.Path()
	require.Error(t, err)
	require.ErrorContains(t, err, "locate user config dir")
	require.Empty(t, p)

	_, want := os.UserConfigDir()
	require.Error(t, want)
	require.EqualError(t, errors.Unwrap(err), want.Error(), "the cause is wrapped, not flattened")
}

func TestLoadRejectsMalformedTOML(t *testing.T) {
	p := tempConfig(t)
	require.NoError(t, os.WriteFile(p, []byte("this is = = not toml\n"), 0o600))

	c, err := config.Load()
	require.Error(t, err)
	require.Nil(t, c)
	require.ErrorContains(t, err, "parse config")
	require.ErrorContains(t, err, p)

	var decode *toml.DecodeError
	require.ErrorAs(t, err, &decode, "the decoder's error is wrapped, not flattened")
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

	t.Run("migrates every legacy source, whatever the map order", func(t *testing.T) {
		p := tempConfig(t)
		var raw strings.Builder
		for i := range 4 {
			fmt.Fprintf(&raw, "[sources.plain%d]\nurl = \"redis://h\"\n", i)
			fmt.Fprintf(&raw, "[sources.legacy%d]\nurl = \"mongodb://h/db\"\ncollection = \"c%d\"\n", i, i)
		}
		require.NoError(t, os.WriteFile(p, []byte(raw.String()), 0o600))

		// Map iteration order is randomized per range, so a migration that gave
		// up at the first collection-less source instead of skipping it would
		// only sometimes miss one; repeat until that is certain.
		for range 10 {
			c, err := config.Load()
			require.NoError(t, err)
			for i := range 4 {
				got := c.Sources[fmt.Sprintf("legacy%d", i)]
				require.Equal(t, fmt.Sprintf("mongodb://h/db?collection=c%d", i), got.URL)
				require.Empty(t, got.Collection)
				require.Equal(t, "redis://h", c.Sources[fmt.Sprintf("plain%d", i)].URL)
			}
		}
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

func TestSaveFailures(t *testing.T) {
	t.Run("parent path is a file", func(t *testing.T) {
		base := t.TempDir()
		notADir := filepath.Join(base, "notadir")
		require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
		t.Setenv(config.EnvConfig, filepath.Join(notADir, "iq.toml"))

		err := (&config.Config{Sources: map[string]config.Source{}}).Save()
		require.Error(t, err)
		require.ErrorContains(t, err, "create config dir")

		var pathErr *fs.PathError
		require.ErrorAs(t, err, &pathErr, "the mkdir failure is wrapped, not flattened")
		require.Equal(t, "mkdir", pathErr.Op)
	})

	t.Run("target path is a directory", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "iq.toml")
		require.NoError(t, os.Mkdir(target, 0o700))
		t.Setenv(config.EnvConfig, target)

		err := (&config.Config{Sources: map[string]config.Source{}}).Save()
		require.Error(t, err)
		require.ErrorContains(t, err, "replace config")

		var linkErr *os.LinkError
		require.ErrorAs(t, err, &linkErr, "the rename failure is wrapped, not flattened")
		require.Equal(t, target, linkErr.New)

		leftovers, globErr := filepath.Glob(filepath.Join(base, "iq-*.toml"))
		require.NoError(t, globErr)
		require.Empty(t, leftovers, "a failed save left a temp file behind")
	})
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
		{name: "leading slash", handle: "/a", url: "redis://h", wantErr: config.ErrBadHandle},
		{name: "doubled slash", handle: "a//b", url: "redis://h", wantErr: config.ErrBadHandle},
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

	t.Run("keeps an active group that still has members", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("prod/books", "redis://h"))
		require.NoError(t, c.Add("prod/cache", "redis://h"))
		require.NoError(t, c.SetGroup("prod"))
		require.NoError(t, c.SetActive("prod/books"))

		moved, err := c.Move("prod/books", "prod/library")
		require.NoError(t, err)
		require.Equal(t, []config.Rename{{Old: "prod/books", New: "prod/library"}}, moved)
		require.Equal(t, "prod", c.Group, "prod/cache still belongs to the group")
		require.Equal(t, "prod/library", c.Active)
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
		require.ElementsMatch(t, []config.Rename{
			{Old: "prod/books", New: "staging/books"},
			{Old: "prod/cache", New: "staging/cache"},
		}, moved)
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

	t.Run("a target inside the moved group is not a collision", func(t *testing.T) {
		c := &config.Config{Sources: map[string]config.Source{}}
		require.NoError(t, c.Add("a/x", "redis://x"))
		require.NoError(t, c.Add("a/b/x", "redis://bx"))

		// Moving group "a" under "a/b" re-targets "a/x" onto the existing
		// "a/b/x", which is itself part of the moved set and about to vacate
		// the name, so it is not a duplicate. Which member lands where depends
		// on map iteration order, so only order-independent facts are asserted.
		moved, err := c.Move("a", "a/b")
		require.NoError(t, err)
		require.Len(t, moved, 2)
		require.Contains(t, c.Sources, "a/b/b/x")
		for _, m := range moved {
			require.Equal(t, "a/b/", m.New[:4], "every member lands under the new prefix")
		}
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
		require.Equal(t, []config.Removed{
			{Handle: "cache", Source: config.Source{URL: "redis://h"}},
			{Handle: "prod/books", Source: config.Source{URL: "mongodb://h/db?collection=books"}},
		}, removed)
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
		// The handles are collected by ranging a map, whose iteration order is
		// randomized per range, so an unsorted result would pass by chance now
		// and then; repeat until that is impossible.
		for range 10 {
			c := &config.Config{Sources: map[string]config.Source{}}
			for _, h := range []string{"b", "a", "e", "c", "d"} {
				require.NoError(t, c.Add(h, "redis://h"))
			}

			removed, err := c.RemoveAll([]string{"c", "a", "e", "b", "d"})
			require.NoError(t, err)
			handles := make([]string, 0, len(removed))
			for _, r := range removed {
				handles = append(handles, r.Handle)
			}
			require.Equal(t, []string{"a", "b", "c", "d", "e"}, handles)
		}
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

func TestGroupsSorted(t *testing.T) {
	c := &config.Config{Sources: map[string]config.Source{}}
	for _, h := range []string{"beta/a", "alpha/a", "gamma/a", "delta/a", "epsilon/a"} {
		require.NoError(t, c.Add(h, "redis://h"))
	}
	want := []string{"alpha", "beta", "delta", "epsilon", "gamma"}

	// The groups are collected by ranging a map, whose iteration order is
	// randomized per range, so an unsorted result would pass by chance now and
	// then; repeat until that is impossible.
	for range 10 {
		require.Equal(t, want, c.Groups())
	}
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

func TestResolveNamespacing(t *testing.T) {
	t.Run("a name with a slash stays absolute under an active group", func(t *testing.T) {
		c := &config.Config{
			Group: "prod",
			Sources: map[string]config.Source{
				"eu/books":      {URL: "redis://top"},
				"prod/eu/books": {URL: "redis://prod"},
			},
		}

		s, full, ok := c.Resolve("eu/books")
		require.True(t, ok)
		require.Equal(t, "eu/books", full)
		require.Equal(t, "redis://top", s.URL)
	})

	t.Run("no active group prepends no separator", func(t *testing.T) {
		// A hand-written config file can hold a handle the API would reject;
		// with no group active, a bare name must not reach it as "/name".
		c := &config.Config{Sources: map[string]config.Source{"/books": {URL: "redis://h"}}}

		_, _, ok := c.Resolve("books")
		require.False(t, ok)
	})
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

// TestModeWarning asserts the warning appears only when the config file holds an
// inline password and its mode is wider than 0600.
func TestModeWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits, so ModeWarning never warns there")
	}
	const inline = "[sources.a]\nurl = 'redis://h:6379/0'\n[sources.b]\nurl = 'redis://u:p@h:6379/0'\n"
	tests := []struct {
		name string
		body string
		mode fs.FileMode
		want bool
	}{
		{"owner only", inline, 0o600, false},
		{"group can read", inline, 0o640, true},
		{"others can read", inline, 0o604, true},
		{"group can write", inline, 0o620, true},
		{"others can execute only", inline, 0o601, true},
		{"user without password", "[sources.a]\nurl = 'redis://u@h:6379/0'\n", 0o644, false},
		{"no user info", "[sources.a]\nurl = 'redis://h:6379/0'\n", 0o644, false},
		{"at sign in the path of a valid URL", "[sources.a]\nurl = 'redis://h:6379/a@b'\n", 0o644, false},
		{"URL that does not parse, then a password", "[sources.a]\nurl = '://bad'\n[sources.b]\nurl = 'redis://u:p@h:6379/0'\n", 0o644, true},
		{"password in a later source", "[sources.a]\nurl = 'redis://u@h:6379/0'\n[sources.z]\nurl = 'redis://u:p@h:6379/0'\n", 0o644, true},
		{"malformed TOML", "[sources.a\n", 0o644, false},
		{"bad escape with a password", "[sources.a]\nurl = 'redis://u:p@h/%ZZ'\n", 0o644, true},
		{"bad escape without user info", "[sources.a]\nurl = 'redis://h/%ZZ'\n", 0o644, false},
		{"bad escape with a user only", "[sources.a]\nurl = 'redis://u@h/%ZZ'\n", 0o644, false},
		{"bad escape, colon and at sign after the query", "[sources.a]\nurl = 'redis://h/%ZZ?x=a:b@c'\n", 0o644, false},
		{"bad escape, colon and at sign after the fragment", "[sources.a]\nurl = 'redis://h/%ZZ#a:b@c'\n", 0o644, false},
		{"bad escape, no scheme separator", "[sources.a]\nurl = 'u:p@h/%ZZ'\n", 0o644, false},
		{"bad escape in a fragment right after the scheme", "[sources.a]\nurl = 'redis://#a:b@c%ZZ'\n", 0o644, false},
		{"bad escape, colon without an at sign", "[sources.a]\nurl = 'redis://:x/%ZZ'\n", 0o644, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tempConfig(t)
			require.NoError(t, os.WriteFile(p, []byte(tt.body), 0o600))
			require.NoError(t, os.Chmod(p, tt.mode))

			got := config.ModeWarning()

			if !tt.want {
				require.Empty(t, got)
				return
			}
			want := fmt.Sprintf("warning: the config file %s holds an inline password and other users can access it (mode %04o); run: chmod 600 '%s'", p, tt.mode, p)
			require.Equal(t, want, got)
		})
	}
}

// TestModeWarningMissingFile asserts a missing config file gives no warning.
func TestModeWarningMissingFile(t *testing.T) {
	tempConfig(t)

	require.Empty(t, config.ModeWarning())
}

// TestModeWarningQuotesThePath asserts the suggested chmod takes the path as one
// shell word, also with a space and an apostrophe in it.
func TestModeWarningQuotesThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits, so ModeWarning never warns there")
	}
	dir := filepath.Join(t.TempDir(), "Application Support", "it's")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, "iq.toml")
	t.Setenv(config.EnvConfig, p)
	require.NoError(t, os.WriteFile(p, []byte("[sources.a]\nurl = 'redis://u:p@h:6379/0'\n"), 0o600))
	require.NoError(t, os.Chmod(p, 0o644))

	got := config.ModeWarning()

	require.Contains(t, got, "run: chmod 600 '"+filepath.Dir(dir))
	require.True(t, strings.HasSuffix(got, `/Application Support/it'\''s/iq.toml'`), got)
}
