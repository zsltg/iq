package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// closeResources releases the diagnostics handles a root run opened in
// PersistentPreRunE (the --output file, the log file), as Execute does after
// ExecuteContext; a test that runs the root directly registers it so the temp
// dir can be removed on Windows, where an open file cannot be deleted.
func closeResources(t *testing.T, cfg *config) {
	t.Helper()
	t.Cleanup(func() {
		if cfg.logClose != nil {
			_ = cfg.logClose()
		}
		if cfg.outClose != nil {
			_ = cfg.outClose()
		}
	})
}

// runCmd executes c with args, capturing its combined output. The root PreRun
// resolves the global color mode for the invocation, so the prior value is
// restored after the test: a -C run must not tint a later test's output.
func runCmd(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })
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
		{"unparseable yields placeholder", "redis://u:%zz@h", "(unparseable URI)"},
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

	out, err := runCmd(t, newAddCmd(&config{}), "-n", "books", "mongodb://h/db?collection=books", "--skip-verify")
	require.NoError(t, err)
	require.Contains(t, out, "added source books")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	s, ok := cf.Sources["books"]
	require.True(t, ok)
	// The default collection rides in the URL now, not a separate field.
	require.Equal(t, "books", iqmongo.CollectionFromURI(s.URL))
	require.Empty(t, s.Collection)

	_, err = runCmd(t, newAddCmd(&config{}), "postgres://h/db", "--skip-verify")
	require.ErrorContains(t, err, "unsupported URI scheme")

	// A driver-owned collection param is rejected for a backend that has none.
	_, err = runCmd(t, newAddCmd(&config{}), "redis://h?collection=x", "--skip-verify")
	require.ErrorContains(t, err, "no collections")
}

// TestAddRejectsExtraArgs pins the ExactArgs(1) bound: the URI is the only
// positional, so a second word is a usage error, not ignored input.
func TestAddRejectsExtraArgs(t *testing.T) {
	seedConfig(t, newSeed())
	_, err := runCmd(t, newAddCmd(&config{}), "mongodb://h/db", "extra", "--skip-verify")
	require.ErrorContains(t, err, "accepts 1 arg(s), received 2")
}

// TestAddAcceptsAMatchingDriver is the other half of TestAddDriverMismatch: -d
// must accept the driver the URI scheme names, not refuse every -d.
func TestAddAcceptsAMatchingDriver(t *testing.T) {
	seedConfig(t, newSeed())
	out, err := runCmd(t, newAddCmd(&config{}), "-d", "mongo", "-n", "books", "mongodb://h/db?collection=books", "--skip-verify")
	require.NoError(t, err)
	require.Contains(t, out, "added source books")
}

// TestAddRefusesADuplicateHandle proves the add fails on a name in use and keeps
// the source that holds the name.
func TestAddRefusesADuplicateHandle(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://first:6379/0"))
	seedConfig(t, c)

	_, err := runCmd(t, newAddCmd(&config{}), "-n", "cache", "redis://second:6379/0", "--skip-verify")
	require.ErrorContains(t, err, "source already exists")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Equal(t, "redis://first:6379/0", cf.Sources["cache"].URL, "the stored source is untouched")
}

// TestAddReportsAKeyringFailure drives a keyring that refuses the write: the add
// must fail and save nothing, so no source points at a password that is not there.
func TestAddReportsAKeyringFailure(t *testing.T) {
	seedConfig(t, newSeed())
	fk := useFakeKeyring(t)
	fk.setErr = errors.New("keyring locked")

	c := newAddCmd(&config{})
	c.SetIn(strings.NewReader("s3cret\n"))
	_, err := runCmd(t, c, "-n", "cache", "redis://u@h:6379/0", "-p", "--store", "keyring", "--skip-verify")
	require.ErrorContains(t, err, "keyring locked")

	cf, lerr := iqconfig.Load()
	require.NoError(t, lerr)
	require.NotContains(t, cf.Sources, "cache", "a refused keyring write leaves no saved source")
}

// TestAddReportsASaveFailure makes the config write fail after every check has
// passed: the add must report it, never claim a source it did not store.
func TestAddReportsASaveFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(iqconfig.EnvConfig, filepath.Join(dir, "iq.toml"))
	require.NoError(t, newSeed().Save())
	require.NoError(t, os.Chmod(dir, 0o500)) // readable and listable, but not writable.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	out, err := runCmd(t, newAddCmd(&config{}), "-n", "cache", "redis://h:6379/0", "--skip-verify")
	require.ErrorContains(t, err, "create temp config")
	require.NotContains(t, out, "added source cache", "no success line for a source that was not stored")
}

func TestAddSuggestsHandle(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"mongo db name", "mongodb://h:27017/catalog", "catalog"},
		{"mongo srv db name", "mongodb+srv://u:p@c.example.net/inventory", "inventory"},
		{"mongo no db falls back to driver", "mongodb://h:27017", "mongo"},
		{"mongo collection beats db", "mongodb://h:27017/shop?collection=orders", "orders"},
		{"elasticsearch index", "elasticsearch://h:9200/?index=books", "books"},
		{"file dump stem", "file:///dumps/catalog.json", "catalog"},
		{"redis numeric db falls back to driver", "redis://h:6379/0", "redis"},
		{"redis no db falls back to driver", "rediss://h:6379", "redis"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedConfig(t, newSeed())
			out, err := runCmd(t, newAddCmd(&config{}), tt.url, "--skip-verify")
			require.NoError(t, err)
			require.Contains(t, out, "added source "+tt.want)
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			_, ok := cf.Sources[tt.want]
			require.True(t, ok, "expected source %q", tt.want)
		})
	}
}

func TestSuggestHandle(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"mongo db name", "mongodb://h:27017/catalog", "catalog"},
		{"nested db path takes first segment", "mongodb://h/catalog/extra", "catalog"},
		{"drops chars outside the handle alphabet", "mongodb://h/foo%20bar!", "foobar"},
		{"multi-host db name", "mongodb://h1,h2/inventory", "inventory"},
		{"numeric db 0 falls back to driver", "redis://h:6379/0", "redis"},
		{"numeric db 9 falls back to driver", "redis://h:6379/9", "redis"},
		{"no path falls back to driver", "mongodb://h:27017", "mongo"},
		{"unparseable url falls back to driver", "redis://%zz@h/0", "redis"},
		// The keyspace a driver-owned param pins is the most specific container the
		// URL names, so it wins over the database or keyspace in the path.
		{"mongo collection beats db", "mongodb://h/shop?collection=orders", "orders"},
		{"mongo empty collection falls back to db", "mongodb://h/shop?collection=", "shop"},
		{"cassandra table beats keyspace", "cassandra://h/ks?table=events", "events"},
		{"cassandra without table takes the keyspace", "cassandra://h/ks", "ks"},
		{"dynamodb table beats the region host", "dynamodb://us-east-1/?table=orders", "orders"},
		{"dynamodb without table falls back to driver", "dynamodb://us-east-1/", "dynamodb"},
		{"hbase table", "hbase://h:2181/?table=books", "books"},
		{"couchdb database", "couchdb://h:5984/?database=orders", "orders"},
		{"couchbase collection beats bucket", "couchbase://h/?bucket=iq&collection=sales.orders", "orders"},
		{"leading-dot spec keeps only the segment after it", "couchbase://h/?collection=.orders", "orders"},
		{"couchbase bucket when no collection", "couchbase://h/?bucket=iq", "iq"},
		{"neo4j label", "neo4j://h:7687/?label=Movie", "Movie"},
		{"neo4j rel", "neo4j://h:7687/?rel=KNOWS", "KNOWS"},
		{"neo4j database when neither label nor rel", "neo4j://h:7687/?database=movies", "movies"},
		{"elasticsearch index", "elasticsearch://h:9200/?index=books", "books"},
		{"elasticsearch without index falls back to driver", "elasticsearch://h:9200/", "elasticsearch"},
		{"opensearch index", "opensearch://h:9200/?index=books", "books"},
		// A file source's container is the dump itself, so its stem names it.
		{"file dump stem", "file:///home/user/dump.json", "dump"},
		{"file dump stem ignores the format param", "file:///home/user/dump.bin?format=bson", "dump"},
		{"file hint params are not a keyspace", "file:///home/user/graph.json?label=Movie", "graph"},
		{"file without an extension", "file:///dumps/books", "books"},
		// file://segment/... puts the first segment in the host; DumpPath folds it
		// back into the path, so the stem is still the file's.
		{"file two-slash form", "file://dumps/books.json", "books"},
		// A file url naming no usable stem falls through to the path, then the driver.
		{"file bare root falls back to driver", "file:///", "file"},
		{"file dotfile is all extension, falls back to the path", "file:///dumps/.json", "dumps"},
		{"file naming no path segment falls back to driver", "file://.", "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, suggestHandle(newSeed(), tt.url))
		})
	}
}

func TestSuggestHandleDisambiguatesAgainstExisting(t *testing.T) {
	cf := newSeed()
	require.NoError(t, cf.Add("catalog", "mongodb://h/catalog"))
	require.NoError(t, cf.Add("catalog2", "mongodb://h/catalog"))
	require.Equal(t, "catalog3", suggestHandle(cf, "mongodb://other/catalog"))
}

func TestAddSuggestedHandleDisambiguates(t *testing.T) {
	seedConfig(t, newSeed())
	_, err := runCmd(t, newAddCmd(&config{}), "mongodb://h/shop", "--skip-verify")
	require.NoError(t, err)
	out, err := runCmd(t, newAddCmd(&config{}), "mongodb://other/shop", "--skip-verify")
	require.NoError(t, err)
	require.Contains(t, out, "added source shop2")
}

func TestAddActiveMakesActive(t *testing.T) {
	seedConfig(t, newSeed())
	_, err := runCmd(t, newAddCmd(&config{}), "-n", "cache", "redis://h:6379/0", "-a", "--skip-verify")
	require.NoError(t, err)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Equal(t, "cache", cf.Active)
}

func TestAddDriverMismatch(t *testing.T) {
	seedConfig(t, newSeed())
	_, err := runCmd(t, newAddCmd(&config{}), "-d", "mongo", "redis://h:6379/0", "--skip-verify")
	require.ErrorContains(t, err, "does not match URI scheme")

	_, err = runCmd(t, newAddCmd(&config{}), "-d", "surrealdb", "redis://h:6379/0", "--skip-verify")
	require.ErrorContains(t, err, "unknown driver")
	// The message must name the drivers that do exist, so the suggestion stays
	// useful and in sync with the registry.
	for _, name := range driverNameList() {
		require.ErrorContainsf(t, err, name, "unknown-driver error omits %q", name)
	}
}

func TestAddPasswordPromptFromStdin(t *testing.T) {
	tests := []struct {
		name  string
		stdin string
	}{
		{"trailing newline stripped", "s3cret\n"},
		{"no trailing newline (EOF)", "s3cret"},
		{"trailing crlf stripped", "s3cret\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedConfig(t, newSeed())
			c := newAddCmd(&config{})
			c.SetIn(strings.NewReader(tt.stdin))
			out, err := runCmd(t, c, "-n", "cache", "redis://u@h:6379/0", "-p", "--skip-verify")
			require.NoError(t, err)
			require.Contains(t, out, "added source cache")
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			require.Equal(t, "redis://u:s3cret@h:6379/0", cf.Sources["cache"].URL)
		})
	}
}

func TestLsMarksActiveAndRedacts(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://u:secret@h:6379/0"))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.SetActive("cache"))
	require.NoError(t, c.SetGroup("prod"))
	seedConfig(t, c)

	out, err := runCmd(t, newLsCmd(&config{}))
	require.NoError(t, err)
	require.Contains(t, out, "active group: prod")
	require.Contains(t, out, "* cache")
	require.NotContains(t, out, "secret")
	require.Contains(t, out, "xxxxx")
	require.Contains(t, out, "collection=books") // the collection shows in the URL, not as a duplicate tag
}

func TestSrcCommand(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h"))
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
	require.NoError(t, c.Add("prod/db", "redis://h"))
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
	require.NoError(t, c.Add("books", "mongodb://h/db?collection=books"))
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
	require.NoError(t, c.Add("cache", "redis://h"))
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
	require.NoError(t, c.Add("cache", "redis://h"))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.Add("prod/cache", "redis://h"))
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
	require.NoError(t, c.Add("cache", "redis://h"))
	seedConfig(t, c)

	_, err := runCmd(t, newRmCmd(), "cache", "nope")
	require.ErrorContains(t, err, "unknown source")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Contains(t, cf.Sources, "cache", "nothing removed when a name is unknown")
}
