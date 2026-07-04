package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedLs builds a config with a mix of grouped and top-level sources.
func seedLs(t *testing.T) {
	t.Helper()
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://u:secret@h:6379/0", ""))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db", "books"))
	require.NoError(t, c.Add("prod/cache", "redis://h", ""))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)
}

func TestLsVerbose(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "-v")
	require.NoError(t, err)
	require.Contains(t, out, "redis")   // driver column
	require.Contains(t, out, "mongodb") // driver column
	require.Contains(t, out, "xxxxx")   // still redacted
	require.NotContains(t, out, "secret")
}

func TestLsGroups(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "-g")
	require.NoError(t, err)
	require.Contains(t, out, "prod")
	require.NotContains(t, out, "cache") // groups, not sources
}

func TestLsGroupFilter(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "prod")
	require.NoError(t, err)
	require.Contains(t, out, "prod/books")
	require.Contains(t, out, "prod/cache")
	require.NotContains(t, out, "* cache") // top-level cache excluded
}

func TestLsGroupFilterEmpty(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "dev")
	require.NoError(t, err)
	require.Contains(t, out, "no sources in group dev")
}

func TestLsJSON(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "--json")
	require.NoError(t, err)

	var rows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 3)

	byHandle := map[string]sourceRow{}
	for _, r := range rows {
		byHandle[r.Handle] = r
	}
	require.Equal(t, "redis", byHandle["cache"].Driver)
	require.True(t, byHandle["cache"].Active)
	require.Equal(t, "redis://u:xxxxx@h:6379/0", byHandle["cache"].Location)
	require.NotContains(t, out, "secret")
	require.Equal(t, "books", byHandle["prod/books"].Collection)
}

func TestLsReveal(t *testing.T) {
	seedLs(t)

	redacted, err := runCmd(t, newLsCmd())
	require.NoError(t, err)
	require.NotContains(t, redacted, "secret")
	require.Contains(t, redacted, "xxxxx")

	revealed, err := runCmd(t, newLsCmd(), "--reveal")
	require.NoError(t, err)
	require.Contains(t, revealed, "redis://u:secret@h:6379/0")
	require.NotContains(t, revealed, "xxxxx")
}

func TestLsGroupsJSON(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(), "-g", "--json")
	require.NoError(t, err)

	var rows []groupRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "prod", rows[0].Group)
	require.Equal(t, 2, rows[0].Sources)
}
