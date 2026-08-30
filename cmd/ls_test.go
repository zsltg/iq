package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// seedLs builds a config with a mix of grouped and top-level sources.
func seedLs(t *testing.T) {
	t.Helper()
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://u:secret@h:6379/0"))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.Add("prod/cache", "redis://h"))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)
}

// TestLsDefaultColumns pins the default layout: marker+handle, driver, then the
// url — no header, driver no longer verbose-only. One source keeps the tabwriter
// widths exact.
func TestLsDefaultColumns(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h:6379/0"))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{}))
	require.NoError(t, err)
	require.Equal(t, "* cache  redis  redis://h:6379/0\n", out)
}

// TestLsVerbose exercises `iq ls -v` through the root tree: -v is now the global
// --verbose (ls no longer owns a local -v), and it adds the header row plus the
// FORMAT and OPTIONS columns on top of the default driver column.
func TestLsVerbose(t *testing.T) {
	seedLs(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "ls", "-v")
	require.NoError(t, err)
	require.Contains(t, out, "HANDLE") // verbose adds a header row
	require.Contains(t, out, "DRIVER")
	require.Contains(t, out, "LOCATION")
	require.Contains(t, out, "FORMAT")
	require.Contains(t, out, "OPTIONS")
	require.Contains(t, out, "redis") // driver column (exact assertion in TestLsJSON)
	require.Contains(t, out, "mongo") // driver column, normalized from the mongodb scheme
	require.Contains(t, out, "xxxxx") // still redacted
	require.NotContains(t, out, "secret")
}

// TestLsVerboseFileFormat proves `iq ls -v` detects and shows a file source's
// dump format, and marks a non-file source with an em dash.
func TestLsVerboseFileFormat(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "snapshot.rdb")
	require.NoError(t, os.WriteFile(dump, []byte("REDIS0011"), 0o600))

	c := newSeed()
	require.NoError(t, c.Add("dump", "file://"+dump))
	require.NoError(t, c.Add("cache", "redis://h"))
	require.NoError(t, c.Add("gone", "file:///no/such/dump.rdb")) // a moved/missing dump
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{verbose: true}))
	require.NoError(t, err)
	require.Contains(t, out, "rdb") // detected format for the file source
	require.Contains(t, out, "—")   // em dash for the non-file source
	require.Contains(t, out, "?")   // undetectable (missing) dump, without failing the listing
}

// TestLsColorRoles pins the sq-mirrored color of each column when color is on, so
// a swapped or dropped role is caught (the plain-text tests can't see color). The
// exact escape sequences match fatih/color's output for each attribute.
func TestLsColorRoles(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	dump := filepath.Join(t.TempDir(), "d.rdb")
	require.NoError(t, os.WriteFile(dump, []byte("REDIS0011"), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h"))
	require.NoError(t, c.Add("dump", "file://"+dump))
	require.NoError(t, c.SetOption("@cache", "timeout", "30s"))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{verbose: true}))
	require.NoError(t, err)
	require.Contains(t, out, "\x1b[36;1mHANDLE\x1b[0;22m")  // header: cyan+bold
	require.Contains(t, out, "\x1b[32;1m* cache\x1b[0;22m") // active handle: green+bold
	require.Contains(t, out, "\x1b[34m  dump\x1b[0m")       // other handle: blue
	require.Contains(t, out, "\x1b[2mredis\x1b[22m")        // driver: faint
	require.Contains(t, out, "\x1b[32mredis://h\x1b[0m")    // location: green
	require.Contains(t, out, "\x1b[33mrdb\x1b[0m")          // file format: yellow
	require.Contains(t, out, "\x1b[2m—\x1b[22m")            // non-file format: faint dash
	require.Contains(t, out, "\x1b[2mtimeout=30s\x1b[22m")  // options: faint
	// A source with no options gets a bare empty cell, not an empty faint span.
	require.NotContains(t, out, "\x1b[2m\x1b[22m")
}

// TestLsVerboseKeyringTag proves a keyring-backed source shows the [keyring] tag
// on its location in the verbose view.
func TestLsVerboseKeyringTag(t *testing.T) {
	c := newSeed()
	c.Sources["kr"] = iqconfig.Source{URL: "redis://h", Keyring: true}
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{verbose: true}))
	require.NoError(t, err)
	require.Contains(t, out, "[keyring]")
}

// TestLsVerboseOptions proves a source's stored options render in `iq ls -v` and
// stay hidden in the default view.
func TestLsVerboseOptions(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("shop", "redis://h"))
	require.NoError(t, c.SetOption("@shop", "format", "yaml"))
	require.NoError(t, c.SetOption("@shop", "timeout", "30s"))
	seedConfig(t, c)

	verbose, err := runCmd(t, newLsCmd(&config{verbose: true}))
	require.NoError(t, err)
	// Multiple options are space-joined in persistableOptions order (format before timeout).
	require.Contains(t, verbose, "format=yaml timeout=30s")

	plain, err := runCmd(t, newLsCmd(&config{}))
	require.NoError(t, err)
	require.NotContains(t, plain, "timeout=30s") // options are verbose-only
	require.NotContains(t, plain, "OPTIONS")
}

// TestLsJSONVerbose proves the verbose-only Format and Options fields ride in
// `iq ls -v --json` and are omitted from the plain `--json`.
func TestLsJSONVerbose(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "d.rdb")
	require.NoError(t, os.WriteFile(dump, []byte("REDIS0011"), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("dump", "file://"+dump))
	require.NoError(t, c.SetOption("@dump", "timeout", "30s"))
	seedConfig(t, c)

	plain, err := runCmd(t, newLsCmd(&config{}), "--json")
	require.NoError(t, err)
	var prows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(plain), &prows))
	require.Len(t, prows, 1)
	require.Empty(t, prows[0].Format)
	require.Empty(t, prows[0].Options)

	vout, err := runCmd(t, newLsCmd(&config{verbose: true}), "--json")
	require.NoError(t, err)
	var vrows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(vout), &vrows))
	require.Len(t, vrows, 1)
	require.Equal(t, "rdb", vrows[0].Format)
	require.Equal(t, "30s", vrows[0].Options["timeout"])
}

// TestLsJSONKeyringFlag proves the machine-readable listing marks which sources
// keep their password in the OS keyring, the flag the text view renders as the
// [keyring] tag.
func TestLsJSONKeyringFlag(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("inline", "redis://u:p@h:6379/0"))
	c.Sources["kr"] = iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{}), "--json")
	require.NoError(t, err)
	var rows []sourceRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 2)
	byHandle := map[string]sourceRow{}
	for _, r := range rows {
		byHandle[r.Handle] = r
	}
	require.True(t, byHandle["kr"].Keyring)
	require.False(t, byHandle["inline"].Keyring)
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
	require.Contains(t, out, "collection=books") // the collection shows once, in the URL
	require.NotContains(t, out, "(books)")       // not repeated as a separate tag
	require.NotContains(t, out, "* cache")       // top-level cache excluded
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
	require.NoError(t, c.Add("prod/a", "redis://h"))
	require.NoError(t, c.Add("dev/b", "redis://h"))
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
