package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// listDispatch runs the dispatch with list set, no store, and no color, and
// returns the names that the chosen inspector printed.
func listDispatch(t *testing.T, url string) []string {
	t.Helper()
	cfg := &config{url: url, source: iqconfig.Source{URL: url}, handle: "src"}
	var buf bytes.Buffer

	err := dispatchInspect(t.Context(), &buf, nil, cfg, nil, false, false, true)

	require.NoError(t, err)
	return strings.Fields(stripANSI(buf.String()))
}

// TestDispatchInspectRoutesEveryScheme pins the driver table. Each scheme,
// including the alias schemes, reaches the inspector of its own driver, and
// the names that the inspector lists equal the names that completion offers for
// the driver. A wrong entry sends a driver to another inspector.
func TestDispatchInspectRoutesEveryScheme(t *testing.T) {
	tests := []struct {
		driver string
		url    string
		want   []string
	}{
		{"mongo", "mongodb://h/db", mongoInspectCmds},
		{"mongo", "mongodb+srv://h/db", mongoInspectCmds},
		{"cassandra", "cassandra://h/ks", cassandraInspectCmds},
		{"dynamodb", "dynamodb://h", dynamoInspectCmds},
		{"hbase", "hbase://h", hbaseInspectCmds},
		{"couchdb", "couchdb://h", couchInspectCmds},
		{"couchdb", "couchdbs://h", couchInspectCmds},
		{"couchbase", "couchbase://h", couchbaseInspectCmds},
		{"couchbase", "couchbases://h", couchbaseInspectCmds},
		{"neo4j", "neo4j://h", neo4jInspectCmds},
		{"neo4j", "bolt://h", neo4jInspectCmds},
		{"neo4j", "neo4j+s://h", neo4jInspectCmds},
		{"elasticsearch", "elasticsearch://h", elasticInspectCmds},
		{"elasticsearch", "elasticsearch+s://h", elasticInspectCmds},
		{"opensearch", "opensearch://h", elasticInspectCmds},
		{"opensearch", "opensearch+s://h", elasticInspectCmds},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			require.Equal(t, tt.driver, driverName(tt.url))

			got := listDispatch(t, tt.url)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want, inspectSubcommands(tt.driver))
		})
	}
}

// TestDispatchInspectDefaultsToMongo pins the default: a driver name that the
// dispatch does not list reaches the MongoDB inspector.
func TestDispatchInspectDefaultsToMongo(t *testing.T) {
	require.Equal(t, mongoInspectCmds, listDispatch(t, "nosuch://h"))
}

// TestDispatchInspectRedisRoutesToInfo pins the Redis entry: both schemes
// reach the INFO inspector, which runs one INFO with the --only names.
func TestDispatchInspectRedisRoutesToInfo(t *testing.T) {
	for _, url := range []string{"redis://h:6379/0", "rediss://h:6379/0"} {
		t.Run(url, func(t *testing.T) {
			st := &fakeStore{}
			cfg := &config{url: url, source: iqconfig.Source{URL: url}}
			var buf bytes.Buffer

			err := dispatchInspect(t.Context(), &buf, st, cfg, []string{"server", "memory"}, false, false, false)

			require.NoError(t, err)
			require.Equal(t, []string{"INFO", "server", "memory"}, st.gotQuery)
		})
	}
}

// TestDispatchInspectFileIsRefused pins the fixed error for a file source: it
// comes before any store use, so a nil store is safe.
func TestDispatchInspectFileIsRefused(t *testing.T) {
	cfg := &config{url: "file:///dump.jsonl"}
	var buf bytes.Buffer

	err := dispatchInspect(t.Context(), &buf, nil, cfg, nil, false, false, false)

	require.EqualError(t, err, "inspect reports live server metadata, and a file source has none; "+
		"query it with a jq filter (`iq '.[]' --src <name>`) or compare it with `iq diff`")
	require.Empty(t, buf.String())
}

// TestInspectCommandGivesFlagsToTheRequest proves that the flags --only, -j,
// -y, and --list reach the inspector, and that the opened store renders the
// text reply. The ctxrec driver is not a listed driver, so the run reaches the
// MongoDB inspector over a fake store that records each query.
func TestInspectCommandGivesFlagsToTheRequest(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        string
		wantQueries []string
	}{
		{"text", []string{"a", "--only", "buildInfo"}, "# buildInfo\n<map[ok:1]>\n\n", []string{`{"buildInfo":1}`}},
		{"json", []string{"a", "--only", "buildInfo", "-j"}, "{\n  \"buildInfo\": {\n    \"ok\": 1\n  }\n}\n", []string{`{"buildInfo":1}`}},
		{"yaml", []string{"a", "--only", "buildInfo", "-y"}, "buildInfo:\n    ok: 1\n", []string{`{"buildInfo":1}`}},
		{"list", []string{"a", "--list", "--only", "buildInfo"}, "dbStats\nserverStatus\nlistCollections\ncollStats\nbuildInfo\nhostInfo\n", nil},
		{"list json", []string{"a", "--list", "-j"}, "[\n  \"dbStats\",\n  \"serverStatus\",\n  \"listCollections\",\n  \"collStats\",\n  \"buildInfo\",\n  \"hostInfo\"\n]\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := useCtxDriver(t)

			out, err := runCmd(t, newInspectCmd(&config{timeout: time.Minute}), tt.args...)

			require.NoError(t, err)
			require.Contains(t, stripANSI(out), tt.want)
			require.Equal(t, tt.wantQueries, rec.queries["ctxrec://a"])
		})
	}
}

// TestInspectCommandRunsEverySectionByDefault proves that no --only runs each
// MongoDB section in order, with the collStats section skipped for a source
// that has no collection.
func TestInspectCommandRunsEverySectionByDefault(t *testing.T) {
	rec := useCtxDriver(t)

	_, err := runCmd(t, newInspectCmd(&config{timeout: time.Minute}), "a")

	require.NoError(t, err)
	require.Equal(t, []string{
		`{"dbStats":1}`, `{"serverStatus":1}`, `{"listCollections":1}`, `{"buildInfo":1}`, `{"hostInfo":1}`,
	}, rec.queries["ctxrec://a"])
}
