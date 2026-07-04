package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// runCmd executes c with args, capturing its combined output.
func runCmd(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"password redacted", "redis://u:p@h:6379/0", "redis://u:xxxxx@h:6379/0"},
		{"user only unchanged", "redis://u@h:6379", "redis://u@h:6379"},
		{"no userinfo unchanged", "mongodb://h:27017/db", "mongodb://h:27017/db"},
		{"multi-host with creds", "mongodb://u:p@h1,h2/db", "mongodb://u:xxxxx@h1,h2/db"},
		{"multi-host no creds", "mongodb://h1,h2/db", "mongodb://h1,h2/db"},
		{"no scheme unchanged", "just-a-string", "just-a-string"},
		{"unparseable yields placeholder", "redis://u:%zz@h", "(unparseable url)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, redactURL(tt.in))
		})
	}
}

func TestSupportedScheme(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"redis://h", true},
		{"rediss://h", true},
		{"mongodb://h/db", true},
		{"mongodb+srv://h/db", true},
		{"postgres://h", false},
		{"", false},
		{"noscheme", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			require.Equal(t, tt.want, supportedScheme(tt.url))
		})
	}
}

func TestAddCommand(t *testing.T) {
	seedConfig(t, newSeed())

	out, err := runCmd(t, newAddCmd(), "books", "mongodb://h/db", "-c", "books")
	require.NoError(t, err)
	require.Contains(t, out, "added source books")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	s, ok := cf.Sources["books"]
	require.True(t, ok)
	require.Equal(t, "books", s.Collection)

	_, err = runCmd(t, newAddCmd(), "bad", "postgres://h/db")
	require.ErrorContains(t, err, "unsupported url scheme")
}

func TestLsMarksActiveAndRedacts(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://u:secret@h:6379/0", ""))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
	require.NoError(t, c.SetActive("cache"))
	require.NoError(t, c.SetGroup("prod"))
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd())
	require.NoError(t, err)
	require.Contains(t, out, "active group: prod")
	require.Contains(t, out, "* cache")
	require.NotContains(t, out, "secret")
	require.Contains(t, out, "xxxxx")
	require.Contains(t, out, "(books)")
}

func TestSrcCommand(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h", ""))
	seedConfig(t, c)

	out, err := runCmd(t, newSrcCmd(), "cache")
	require.NoError(t, err)
	require.Contains(t, out, "active source: cache")

	out, err = runCmd(t, newSrcCmd())
	require.NoError(t, err)
	require.Contains(t, out, "cache")

	_, err = runCmd(t, newSrcCmd(), "nope")
	require.ErrorContains(t, err, "unknown source")
}

func TestGroupCommand(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("prod/db", "redis://h", ""))
	seedConfig(t, c)

	out, err := runCmd(t, newGroupCmd(), "prod")
	require.NoError(t, err)
	require.Contains(t, out, "active group: prod")

	_, err = runCmd(t, newGroupCmd(), "dev")
	require.ErrorContains(t, err, "unknown group")

	out, err = runCmd(t, newGroupCmd(), "--clear")
	require.NoError(t, err)
	require.Contains(t, out, "cleared active group")
}

func TestMvCommand(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("books", "mongodb://h/db", "books"))
	require.NoError(t, c.SetActive("books"))
	seedConfig(t, c)

	out, err := runCmd(t, newMvCmd(), "books", "prod/library")
	require.NoError(t, err)
	require.Contains(t, out, "moved books to prod/library")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Contains(t, cf.Sources, "prod/library")
	require.NotContains(t, cf.Sources, "books")
	require.Equal(t, "prod/library", cf.Active)

	_, err = runCmd(t, newMvCmd(), "nope", "other")
	require.ErrorContains(t, err, "unknown source")
}

func TestRmCommand(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h", ""))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)

	out, err := runCmd(t, newRmCmd(), "cache")
	require.NoError(t, err)
	require.Contains(t, out, "removed source cache")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Empty(t, cf.Sources)
	require.Empty(t, cf.Active)

	_, err = runCmd(t, newRmCmd(), "nope")
	require.ErrorContains(t, err, "unknown source")
}

func TestRmMultipleAndGroup(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h", ""))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
	require.NoError(t, c.Add("prod/cache", "redis://h", ""))
	seedConfig(t, c)

	out, err := runCmd(t, newRmCmd(), "cache", "prod")
	require.NoError(t, err)
	require.Contains(t, out, "removed source cache")
	require.Contains(t, out, "removed source prod/books")
	require.Contains(t, out, "removed source prod/cache")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Empty(t, cf.Sources)
}

func TestRmAtomicOnUnknown(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h", ""))
	seedConfig(t, c)

	_, err := runCmd(t, newRmCmd(), "cache", "nope")
	require.ErrorContains(t, err, "unknown source")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Contains(t, cf.Sources, "cache", "nothing removed when a name is unknown")
}
