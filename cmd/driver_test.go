package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDriverForScheme(t *testing.T) {
	tests := []struct {
		name     string
		scheme   string
		wantName string
		wantOK   bool
	}{
		{"mongodb", "mongodb", "mongo", true},
		{"mongodb srv", "mongodb+srv", "mongo", true},
		{"redis", "redis", "redis", true},
		{"redis tls", "rediss", "redis", true},
		{"cassandra", "cassandra", "cassandra", true},
		{"dynamodb", "dynamodb", "dynamodb", true},
		{"hbase", "hbase", "hbase", true},
		{"couchbase", "couchbase", "couchbase", true},
		{"couchbase tls", "couchbases", "couchbase", true},
		{"unknown", "surrealdb", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := driverForScheme(tt.scheme)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantName, d.name)
		})
	}
}

func TestDriverName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"redis", "redis://h:6379/0", "redis"},
		{"redis tls normalizes", "rediss://h:6379/0", "redis"},
		{"mongodb normalizes", "mongodb://h/db", "mongo"},
		{"mongodb srv normalizes", "mongodb+srv://h/db", "mongo"},
		{"cassandra normalizes", "cassandra://h/ks", "cassandra"},
		{"dynamodb normalizes", "dynamodb://us-east-1/?table=t", "dynamodb"},
		{"hbase normalizes", "hbase://h:2181/?table=t", "hbase"},
		{"couchbase normalizes", "couchbases://h/?bucket=b", "couchbase"},
		{"unknown falls back to scheme", "surrealdb://h", "surrealdb"},
		{"schemeless is empty", "just-a-string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, driverName(tt.url))
		})
	}
}

func TestExpectedSchemes(t *testing.T) {
	require.Equal(t, "expected one of mongodb://, cassandra://, dynamodb://, hbase://, couchdb://, couchbase://, neo4j://, elasticsearch://, opensearch://, redis://", expectedSchemes())
}

// TestDriverRegistryInvariants guards the single source of truth: every driver
// is fully described, names are unique, and every registered scheme resolves
// back to its own driver.
func TestDriverRegistryInvariants(t *testing.T) {
	seenName := map[string]bool{}
	seenScheme := map[string]bool{}
	for _, d := range drivers {
		require.NotEmpty(t, d.name)
		require.NotEmpty(t, d.desc)
		require.NotEmpty(t, d.schemes)
		require.NotNil(t, d.open)
		if !d.readOnly {
			// A connectable backend advertises upstream docs and a supported server
			// version range; a read-only local driver (file://) has neither.
			require.NotEmpty(t, d.doc)
			require.NotEmpty(t, d.versions)
		}
		require.False(t, seenName[d.name], "duplicate driver name %q", d.name)
		seenName[d.name] = true
		for _, s := range d.schemes {
			require.False(t, seenScheme[s], "scheme %q claimed by two drivers", s)
			seenScheme[s] = true
			got, ok := driverForScheme(s)
			require.True(t, ok)
			require.Equal(t, d.name, got.name)
		}
	}
}

func TestDriverLsTable(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(&config{}), "ls")
	require.NoError(t, err)
	for _, want := range []string{
		"DRIVER", "DESCRIPTION", "SCHEMES", "VERSIONS", "DOC",
		"mongo", "MongoDB document store", "mongodb, mongodb+srv", "4.2+", "https://www.mongodb.com/docs/",
		"redis", "Redis key-value store", "redis, rediss", "7.0+", "https://redis.io/docs/",
		"cassandra", "Apache Cassandra wide-column store", "3.11+", "https://cassandra.apache.org/doc/",
		"dynamodb", "Amazon DynamoDB key-value and document store", "AWS (managed)", "https://docs.aws.amazon.com/dynamodb/",
		"hbase", "Apache HBase wide-column store", "1.0+", "https://hbase.apache.org/book.html",
		"couchdb", "Apache CouchDB document store", "couchdb, couchdbs", "2.x, 3.x", "https://docs.couchdb.org/",
		"couchbase", "Couchbase document store", "couchbase, couchbases", "7.x, 8.x (Community or Enterprise)", "https://docs.couchbase.com/",
		"neo4j", "Neo4j property graph store", "neo4j, neo4j+s, neo4j+ssc, bolt, bolt+s, bolt+ssc", "5.x", "https://neo4j.com/docs/",
		"elasticsearch", "Elasticsearch search engine and document store", "elasticsearch, elasticsearch+s", "8.x", "https://www.elastic.co/docs/",
		"opensearch", "OpenSearch search engine and document store", "opensearch, opensearch+s", "2.x, 3.x", "https://opensearch.org/docs/",
	} {
		require.Contains(t, out, want)
	}
}

func TestDriverLsJSON(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(&config{}), "ls", "--json")
	require.NoError(t, err)

	var rows []driverRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 11)

	byName := map[string]driverRow{}
	for _, r := range rows {
		byName[r.Driver] = r
	}
	require.Equal(t, []string{"mongodb", "mongodb+srv"}, byName["mongo"].Schemes)
	require.Equal(t, "MongoDB document store", byName["mongo"].Description)
	require.Equal(t, "4.2+", byName["mongo"].Versions)
	require.Equal(t, "https://redis.io/docs/", byName["redis"].Doc)
	require.Equal(t, []string{"redis", "rediss"}, byName["redis"].Schemes)
	require.Equal(t, "7.0+", byName["redis"].Versions)
	require.Equal(t, []string{"cassandra"}, byName["cassandra"].Schemes)
	require.Equal(t, "Apache Cassandra wide-column store", byName["cassandra"].Description)
	require.Equal(t, "https://cassandra.apache.org/doc/", byName["cassandra"].Doc)
	require.Equal(t, []string{"dynamodb"}, byName["dynamodb"].Schemes)
	require.Equal(t, "Amazon DynamoDB key-value and document store", byName["dynamodb"].Description)
	require.Equal(t, "AWS (managed)", byName["dynamodb"].Versions)
	require.Equal(t, "https://docs.aws.amazon.com/dynamodb/", byName["dynamodb"].Doc)
	require.Equal(t, []string{"hbase"}, byName["hbase"].Schemes)
	require.Equal(t, "Apache HBase wide-column store", byName["hbase"].Description)
	require.Equal(t, "1.0+", byName["hbase"].Versions)
	require.Equal(t, "https://hbase.apache.org/book.html", byName["hbase"].Doc)
	require.Equal(t, []string{"couchdb", "couchdbs"}, byName["couchdb"].Schemes)
	require.Equal(t, "Apache CouchDB document store", byName["couchdb"].Description)
	require.Equal(t, "2.x, 3.x", byName["couchdb"].Versions)
	require.Equal(t, "https://docs.couchdb.org/", byName["couchdb"].Doc)
	require.Equal(t, []string{"couchbase", "couchbases"}, byName["couchbase"].Schemes)
	require.Equal(t, "Couchbase document store", byName["couchbase"].Description)
	require.Equal(t, "7.x, 8.x (Community or Enterprise)", byName["couchbase"].Versions)
	require.Equal(t, "https://docs.couchbase.com/", byName["couchbase"].Doc)
	require.Equal(t, []string{"elasticsearch", "elasticsearch+s"}, byName["elasticsearch"].Schemes)
	require.Equal(t, "Elasticsearch search engine and document store", byName["elasticsearch"].Description)
	require.Equal(t, "8.x", byName["elasticsearch"].Versions)
	require.Equal(t, "https://www.elastic.co/docs/", byName["elasticsearch"].Doc)
	require.Equal(t, []string{"opensearch", "opensearch+s"}, byName["opensearch"].Schemes)
	require.Equal(t, "OpenSearch search engine and document store", byName["opensearch"].Description)
	require.Equal(t, "2.x, 3.x", byName["opensearch"].Versions)
	require.Equal(t, "https://opensearch.org/docs/", byName["opensearch"].Doc)
	require.Empty(t, byName["file"].Formats, "the dump-format catalogue is verbose-only")
}

func TestDriverLsVerbose(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(&config{verbose: true}), "ls")
	require.NoError(t, err)
	require.Contains(t, out, "file dump formats")
	// The caption teaches both URL shapes.
	require.Contains(t, out, "file:///<file_path>")
	require.Contains(t, out, "file:///<file_path>?format=<source_format>")
	// An auto-detected format shows a bare name; a forced one shows the full,
	// copy-pasteable ?format=<name>.
	require.NotContains(t, lineWith(t, out, "jsonl"), "?format=")
	require.NotContains(t, lineWith(t, out, "yaml"), "?format=") // YAML now content-detects
	require.Contains(t, lineWith(t, out, "cassandra-csv"), "?format=cassandra-csv")
	require.Contains(t, lineWith(t, out, "cassandra-csv"), "cqlsh COPY TO CSV")

	// A driver with no dump formats gets no caption and no empty block, so the
	// verbose listing stays a catalogue of what exists.
	require.NotContains(t, out, "redis dump formats")
	require.NotContains(t, out, "mongo dump formats")

	// The default (non-verbose) listing stays a clean one-row-per-driver overview.
	plain, err := runCmd(t, newDriverCmd(&config{}), "ls")
	require.NoError(t, err)
	require.NotContains(t, plain, "file dump formats")
	require.NotContains(t, plain, "?format=")
}

func TestDriverLsVerboseJSON(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(&config{verbose: true}), "ls", "--json")
	require.NoError(t, err)
	var rows []driverRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	byName := map[string]driverRow{}
	for _, r := range rows {
		byName[r.Driver] = r
	}
	// Only the read-only file driver advertises formats; a live backend has none.
	require.Empty(t, byName["redis"].Formats)
	byFmt := map[string]driverFormatRow{}
	for _, f := range byName["file"].Formats {
		byFmt[f.Name] = f
	}
	require.Len(t, byFmt, 8)
	require.True(t, byFmt["jsonl"].Auto)
	require.True(t, byFmt["yaml"].Auto)
	require.False(t, byFmt["cassandra-csv"].Auto)
	require.Equal(t, "cqlsh COPY TO CSV", byFmt["cassandra-csv"].Source)
}

// lineWith returns the single output line containing sub, failing the test if none does.
func lineWith(t *testing.T, out, sub string) string {
	t.Helper()
	for ln := range strings.SplitSeq(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	t.Fatalf("no line contains %q in:\n%s", sub, out)
	return ""
}

// TestRegistryAddressParams pins the registry invariants the handle derivation and
// the keyspace guard both rely on: a backend is addressable exactly when it
// declares a keyspace param, and only a keyspace-less backend claims query params
// of its own.
func TestRegistryAddressParams(t *testing.T) {
	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			require.Equal(t, d.addressable, len(d.addressParams) > 0,
				"addressable and addressParams must agree")
			for _, p := range d.addressParams {
				require.NotEmpty(t, p, "a keyspace param name must not be empty")
			}
			if len(d.addressParams) > 0 {
				require.Empty(t, d.urlParams, "a keyspace-bearing backend owns its whole query string")
			}
		})
	}
}

func TestAllAddressParams(t *testing.T) {
	params := allAddressParams()
	require.Equal(t, []string{"collection", "table", "database", "bucket", "label", "rel", "index"}, params,
		"every keyspace spelling in the registry, deduped, in registry order")
}

// TestWriteDriverFormatsPropagatesACaptionWriteError asserts the caption's write
// result is returned. failAt fails only the first write, so a dropped error
// return shows up as a nil result when the table below still writes cleanly.
func TestWriteDriverFormatsPropagatesACaptionWriteError(t *testing.T) {
	d, ok := driverByName("file")
	require.True(t, ok, "the file driver is the one with dump formats")
	require.Error(t, writeDriverFormats(&failAt{at: 1}, d))
}

// TestDriverRegistryCapabilities pins each backend's capability flags and the
// write-side explain describers it registers. A flag or describer that drops out
// of the registry changes what `--explain`, `iq ping` and `iq add` do for that
// backend, so each row is one backend's contract.
func TestDriverRegistryCapabilities(t *testing.T) {
	tests := []struct {
		name           string
		addressable    bool
		addressParams  []string
		verifiesOnOpen bool
		filtersScan    bool
		write, clear   bool
		drop, del      bool
	}{
		{name: "mongo", addressable: true, addressParams: []string{"collection"}, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "cassandra", addressable: true, addressParams: []string{"table"}, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "dynamodb", addressable: true, addressParams: []string{"table"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "hbase", addressable: true, addressParams: []string{"table"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "couchdb", addressable: true, addressParams: []string{"database"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "couchbase", addressable: true, addressParams: []string{"collection", "bucket"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true},
		{name: "neo4j", addressable: true, addressParams: []string{"label", "rel", "database"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "elasticsearch", addressable: true, addressParams: []string{"index"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "opensearch", addressable: true, addressParams: []string{"index"}, verifiesOnOpen: true, filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "redis", filtersScan: true, write: true, clear: true, drop: true, del: true},
		{name: "file"},
	}
	require.Len(t, drivers, len(tests), "every registered driver needs a row")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d driver
			for _, c := range drivers {
				if c.name == tt.name {
					d = c
				}
			}
			require.Equal(t, tt.name, d.name)
			require.Equal(t, tt.addressable, d.addressable)
			require.Equal(t, tt.addressParams, d.addressParams)
			require.Equal(t, tt.verifiesOnOpen, d.verifiesOnOpen)
			require.Equal(t, tt.filtersScan, d.filtersScan)
			require.NotNil(t, d.explainPlan)
			require.Equal(t, tt.write, d.explainWrite != nil)
			require.Equal(t, tt.clear, d.explainClear != nil)
			require.Equal(t, tt.drop, d.explainDrop != nil)
			require.Equal(t, tt.del, d.explainDelete != nil)
		})
	}
}

// TestDriverOpenHonoursContext checks each connectable backend gets the caller's
// context. A cancelled context must make Open fail fast with a context error, so a
// registry opener that drops the context (and so could wait without a bound)
// fails here instead of hanging a real run.
func TestDriverOpenHonoursContext(t *testing.T) {
	urls := map[string]string{
		"mongo":         "mongodb://127.0.0.1:1/db",
		"cassandra":     "cassandra://127.0.0.1:1/ks",
		"dynamodb":      "dynamodb://127.0.0.1:1/?region=us-east-1",
		"hbase":         "hbase://127.0.0.1:1/",
		"couchdb":       "couchdb://127.0.0.1:1/",
		"couchbase":     "couchbase://127.0.0.1:1/",
		"neo4j":         "neo4j://127.0.0.1:1/",
		"elasticsearch": "elasticsearch://127.0.0.1:1/",
		"opensearch":    "opensearch://127.0.0.1:1/",
		"redis":         "redis://127.0.0.1:1/0",
	}
	for _, d := range drivers {
		if d.readOnly {
			continue
		}
		t.Run(d.name, func(t *testing.T) {
			url, ok := urls[d.name]
			require.True(t, ok, "add a URL row for the connectable driver %q", d.name)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := d.open(ctx, &config{url: url})
			require.Error(t, err)
			if d.name != "cassandra" { // gocql dials before it reads the context.
				require.Contains(t, err.Error(), "cancel")
			}
		})
	}
}

// countingWriter counts the Write calls that reach it.
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n++
	return len(p), nil
}

// TestListDriversPropagatesWriteErrors fails one write at a time: the table body,
// then the first write after the table, which is the verbose format caption.
func TestListDriversPropagatesWriteErrors(t *testing.T) {
	var cw countingWriter
	require.NoError(t, listDrivers(&cw, false, false, false))
	require.Positive(t, cw.n)

	tests := []struct {
		name    string
		failAt  int
		verbose bool
	}{
		{name: "table", failAt: 1},
		{name: "format block after the table", failAt: cw.n + 1, verbose: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := listDrivers(&failAt{at: tt.failAt}, tt.verbose, false, false)
			require.ErrorContains(t, err, "write failed")
		})
	}
}

// TestDriverLsFlags checks the command's argument and flag contract: no
// positional argument, -j and -y exclude each other, and -y alone selects the
// structured form.
func TestDriverLsFlags(t *testing.T) {
	t.Run("rejects a positional argument", func(t *testing.T) {
		_, err := runCmd(t, newDriverCmd(&config{}), "ls", "extra")
		require.Error(t, err)
	})
	t.Run("rejects json with yaml", func(t *testing.T) {
		_, err := runCmd(t, newDriverCmd(&config{}), "ls", "-j", "-y")
		require.ErrorContains(t, err, "none of the others can be")
	})
	t.Run("yaml alone is structured", func(t *testing.T) {
		out, err := runCmd(t, newDriverCmd(&config{}), "ls", "-y")
		require.NoError(t, err)
		require.Contains(t, out, "driver: mongo")
		require.NotContains(t, out, "DRIVER")
	})
}

// TestDriverRowsListFormatsOnlyWithVerbose checks that only the file driver lists
// formats, and only with -v, in JSON and in YAML.
func TestDriverRowsListFormatsOnlyWithVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		out, err := runCmd(t, newDriverCmd(&config{verbose: verbose}), "ls", "--json")
		require.NoError(t, err)
		var rows []driverRow
		require.NoError(t, json.Unmarshal([]byte(out), &rows))
		for _, r := range rows {
			if verbose && r.Driver == "file" {
				require.NotEmpty(t, r.Formats)
				continue
			}
			require.Empty(t, r.Formats, r.Driver)
		}
	}

	plain, err := runCmd(t, newDriverCmd(&config{}), "ls", "-y")
	require.NoError(t, err)
	require.Equal(t, 11, strings.Count(plain, "formats: []"))
	verbose, err := runCmd(t, newDriverCmd(&config{verbose: true}), "ls", "-y")
	require.NoError(t, err)
	require.Equal(t, 10, strings.Count(verbose, "formats: []"))
	require.Contains(t, verbose, "name: cassandra-csv")
}

// TestListDriversStructuredWriteError checks the write error of the JSON path.
func TestListDriversStructuredWriteError(t *testing.T) {
	require.ErrorContains(t, listDrivers(&failAt{at: 1}, false, true, false), "write failed")
}
