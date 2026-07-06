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

// TestLsVerbose exercises `iq ls -v` through the root tree: -v is now the global
// --verbose (ls no longer owns a local -v), and the ls driver column reads it.
func TestLsVerbose(t *testing.T) {
	seedLs(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "ls", "-v")
	require.NoError(t, err)
	require.Contains(t, out, "redis") // driver column (exact assertion in TestLsJSON)
	require.Contains(t, out, "mongo") // driver column, normalized from the mongodb scheme
	require.Contains(t, out, "xxxxx") // still redacted
	require.NotContains(t, out, "secret")
}

func TestLsGroups(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(&config{}), "-g")
	require.NoError(t, err)
	require.Contains(t, out, "prod")
	require.NotContains(t, out, "cache") // groups, not sources
}

func TestLsGroupFilter(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(&config{}), "prod")
	require.NoError(t, err)
	require.Contains(t, out, "prod/books")
	require.Contains(t, out, "prod/cache")
	require.NotContains(t, out, "* cache") // top-level cache excluded
}

func TestLsGroupFilterEmpty(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(&config{}), "dev")
	require.NoError(t, err)
	require.Contains(t, out, "no sources in group dev")
}

func TestLsJSON(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(&config{}), "--json")
	require.NoError(t, err)

	var rows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 3)

	byHandle := map[string]sourceRow{}
	for _, r := range rows {
		byHandle[r.Handle] = r
	}
	require.Equal(t, "redis", byHandle["cache"].Driver)
	require.Equal(t, "mongo", byHandle["prod/books"].Driver) // canonical name, not the mongodb scheme
	require.True(t, byHandle["cache"].Active)
	require.Equal(t, "redis://u:xxxxx@h:6379/0", byHandle["cache"].Location)
	require.NotContains(t, out, "secret")
	require.Equal(t, "books", byHandle["prod/books"].Collection)
}

func TestLsJSONYAMLFlags(t *testing.T) {
	seedLs(t)

	// -j is the short for --json.
	shortJSON, err := runCmd(t, newLsCmd(&config{}), "-j")
	require.NoError(t, err)
	var rows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(shortJSON), &rows))
	require.Len(t, rows, 3)

	// -y / --yaml emits YAML instead.
	yamlOut, err := runCmd(t, newLsCmd(&config{}), "-y")
	require.NoError(t, err)
	require.Contains(t, yamlOut, "handle: cache")
	require.Contains(t, yamlOut, "driver: redis")

	// The two structured formats are mutually exclusive.
	_, err = runCmd(t, newLsCmd(&config{}), "-j", "-y")
	require.Error(t, err)
}

func TestLsReveal(t *testing.T) {
	seedLs(t)

	redacted, err := runCmd(t, newLsCmd(&config{}))
	require.NoError(t, err)
	require.NotContains(t, redacted, "secret")
	require.Contains(t, redacted, "xxxxx")

	revealed, err := runCmd(t, newLsCmd(&config{}), "--reveal")
	require.NoError(t, err)
	require.Contains(t, revealed, "redis://u:secret@h:6379/0")
	require.NotContains(t, revealed, "xxxxx")

	// --expand alone is a no-op for an inline password: it resolves only
	// keyring-backed secrets, so the inline one stays redacted.
	expanded, err := runCmd(t, newLsCmd(&config{}), "--expand")
	require.NoError(t, err)
	require.NotContains(t, expanded, "secret")
	require.Contains(t, expanded, "xxxxx")
}

func TestLsGroupsMarksActiveAndListsAll(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("prod/a", "redis://h", ""))
	require.NoError(t, c.Add("dev/b", "redis://h", ""))
	require.NoError(t, c.SetGroup("prod"))
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{}), "-g")
	require.NoError(t, err)
	// Both groups are listed (a broken loop that stopped early would drop one).
	require.Contains(t, out, "prod")
	require.Contains(t, out, "dev")
	// The active group is marked, and only it.
	require.Contains(t, out, "* prod")
	require.NotContains(t, out, "* dev")
}

func TestLsGroupsJSON(t *testing.T) {
	seedLs(t)
	out, err := runCmd(t, newLsCmd(&config{}), "-g", "--json")
	require.NoError(t, err)

	var rows []groupRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "prod", rows[0].Group)
	require.Equal(t, 2, rows[0].Sources)
}
