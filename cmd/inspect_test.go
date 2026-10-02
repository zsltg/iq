package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestInspectFileSourceErrors(t *testing.T) {
	// A file source has no live server metadata to introspect; inspect points the
	// user at query/diff instead of connecting.
	seedFileSource(t, moveDump)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--src", "snap", "inspect")
	require.ErrorContains(t, err, "live server metadata")
}

// TestDispatchInspectForwardsContext proves the dispatch hands the caller's
// context to the driver's inspector rather than one of its own, so a deadline or
// a cancellation reaches the backend commands. The default branch (MongoDB) is
// the one exercised: it issues its diagnostic commands through Store.Query, and
// the fake records the context each call received.
func TestDispatchInspectForwardsContext(t *testing.T) {
	st := &fakeStore{}
	cfg := &config{url: "mongodb://h/db?collection=books", handle: "shop"}
	ctx := t.Context()

	var buf bytes.Buffer
	require.NoError(t, dispatchInspect(ctx, inspectRequest{out: &buf, st: st, cfg: cfg, only: []string{"dbStats"}, jsonOut: true}))
	require.Equal(t, ctx, st.gotCtx)
	require.NotNil(t, st.gotCtx)
}

// fakeInspectStore is a minimal store for exercising renderInspectResults without
// a backend: the helper only calls FormatRaw, so the read/write ports are inert.
type fakeInspectStore struct{}

func (fakeInspectStore) Get(context.Context, []string) (map[string]any, error) { return nil, nil }

func (fakeInspectStore) ScanBatches(context.Context, func(map[string]any) error) error { return nil }

func (fakeInspectStore) Query(context.Context, []string) (any, error) { return nil, nil }
func (fakeInspectStore) Close() error                                 { return nil }
func (fakeInspectStore) FormatRaw(v any, _ bool) string               { return fmt.Sprintf("<%v>", v) }

func TestRenderInspectResults(t *testing.T) {
	results := []inspectResult{{sub: "alpha", value: "A"}, {sub: "beta", value: "B"}}
	src := iqconfig.Source{URL: "redis://localhost:6379/0"}

	t.Run("text writes a header then each result in order", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config{source: src, handle: "cache"}
		require.NoError(t, renderInspectResults(inspectRequest{out: &buf, st: fakeInspectStore{}, cfg: cfg}, results))
		out := buf.String()
		require.Contains(t, out, "# alpha")
		require.Contains(t, out, "<A>")
		require.Contains(t, out, "# beta")
		require.Contains(t, out, "<B>")
		require.Less(t, strings.Index(out, "# alpha"), strings.Index(out, "# beta"))
	})

	t.Run("yaml emits a name-keyed structure without the header", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config{source: src, handle: "cache"}
		require.NoError(t, renderInspectResults(inspectRequest{out: &buf, st: fakeInspectStore{}, cfg: cfg, yamlOut: true}, results))
		out := buf.String()
		require.Contains(t, out, "alpha")
		require.Contains(t, out, "beta")
		require.NotContains(t, out, "# alpha") // the text header is not written in structured output
	})
}

func TestParseRedisInfo(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.0\r\nos:Linux\r\n\r\n# Memory\r\nused_memory:12345\r\n"
	got := parseRedisInfo(info)
	require.Equal(t, "7.2.0", got["Server"]["redis_version"])
	require.Equal(t, "Linux", got["Server"]["os"])
	require.Equal(t, "12345", got["Memory"]["used_memory"])
}

func TestMongoInspectDoc(t *testing.T) {
	require.Equal(t, `{"dbStats":1}`, mongoInspectDoc("dbStats", ""))
	require.JSONEq(t, `{"collStats":"books"}`, mongoInspectDoc("collStats", "books"))
}

func TestIsMongoInspectCmd(t *testing.T) {
	require.True(t, isMongoInspectCmd("dbStats"))
	require.True(t, isMongoInspectCmd("collStats"))
	require.False(t, isMongoInspectCmd("dropDatabase"))
}

func TestInspectMongoRejectsUnknown(t *testing.T) {
	// Validation happens before any store access, so a nil store is never used.
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), inspectRequest{out: &buf, cfg: &config{url: "mongodb://h/db"}, only: []string{"dropDatabase"}})
	require.ErrorContains(t, err, "unknown inspect subcommand")
}

func TestInspectMongoCollStatsNeedsCollection(t *testing.T) {
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), inspectRequest{out: &buf, cfg: &config{url: "mongodb://h/db"}, only: []string{"collStats"}})
	require.ErrorContains(t, err, "needs a collection")
}

func TestInspectMongoList(t *testing.T) {
	// The list path prints the supported set and never touches the store, so a
	// nil store proves it stays offline.
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), inspectRequest{out: &buf, cfg: &config{url: "mongodb://h/db"}, list: true})
	require.NoError(t, err)
	for _, sub := range mongoInspectCmds {
		require.Contains(t, buf.String(), sub)
	}
}

func TestInspectHeader(t *testing.T) {
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("sec", "secret"))

	inline := iqconfig.Source{URL: "redis://u:secret@h:6379/0"}
	keyring := iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}

	tests := []struct {
		name         string
		source       iqconfig.Source
		handle       string
		reveal       bool
		expand       bool
		wantContains string
		wantAbsent   string
	}{
		{"inline redacted by default", inline, "cache", false, false, "redis://u:xxxxx@h:6379/0", "secret"},
		{"inline reveal un-redacts", inline, "cache", true, false, "redis://u:secret@h:6379/0", "xxxxx"},
		{"keyring hidden by default", keyring, "sec", false, false, "redis://u@h:6379/0", "secret"},
		{"keyring reveal alone stays hidden", keyring, "sec", true, false, "redis://u@h:6379/0", "secret"},
		{"keyring expand alone redacts", keyring, "sec", false, true, "redis://u:xxxxx@h:6379/0", "secret"},
		{"keyring reveal and expand", keyring, "sec", true, true, "redis://u:secret@h:6379/0", "xxxxx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := &config{source: tt.source, handle: tt.handle, reveal: tt.reveal, expand: tt.expand}
			require.NoError(t, inspectHeader(&buf, cfg))
			require.Contains(t, buf.String(), "redis  ") // driver from the source scheme
			require.Contains(t, buf.String(), tt.wantContains)
			require.NotContains(t, buf.String(), tt.wantAbsent)
		})
	}
}

func TestRedisInfoSections(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.0\r\n\r\n# Memory\r\nused_memory:12345\r\n"
	require.Equal(t, []string{"Memory", "Server"}, redisInfoSections(info))
}

func TestWriteInspectList(t *testing.T) {
	t.Run("plain is newline separated", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writeInspectList(&buf, []string{"a", "b"}, false, false))
		require.Equal(t, "a\nb\n", buf.String())
	})
	t.Run("json is an array", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writeInspectList(&buf, []string{"a", "b"}, true, false))
		var got []string
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		require.Equal(t, []string{"a", "b"}, got)
	})
}

func TestInspectRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 3 * time.Second}

	out, err := runCmd(t, newInspectCmd(cfg), "live", "--only", "server")
	require.NoError(t, err)
	require.Contains(t, out, "redis_version")

	jsonOut, err := runCmd(t, newInspectCmd(cfg), "live", "--only", "server", "--json")
	require.NoError(t, err)
	var sections map[string]map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &sections))
	require.Contains(t, sections, "Server")
	require.NotEmpty(t, sections["Server"]["redis_version"])

	listOut, err := runCmd(t, newInspectCmd(cfg), "--list")
	require.NoError(t, err)
	require.Contains(t, listOut, "Server")
	require.Contains(t, listOut, "Memory")

	listJSON, err := runCmd(t, newInspectCmd(cfg), "--list", "--json")
	require.NoError(t, err)
	var names []string
	require.NoError(t, json.Unmarshal([]byte(listJSON), &names))
	require.Contains(t, names, "Server")
}

func TestInspectMongoIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	url := os.Getenv("IQ_MONGO_URL")
	if url == "" {
		url = "mongodb://localhost:27017/iq"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url+"?collection=books"))
	require.NoError(t, c.Add("nocoll", url))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}

	out, err := runCmd(t, newInspectCmd(cfg), "live", "--only", "dbStats")
	require.NoError(t, err)
	require.Contains(t, out, "dbStats")

	// `<source>.<collection>` addressing supplies the collection collStats needs,
	// even for a source that stores none.
	collText, err := runCmd(t, newInspectCmd(cfg), "nocoll.books", "--only", "collStats")
	require.NoError(t, err)
	require.Contains(t, collText, "collStats")

	// Only collStats needs a collection: every other subcommand runs against a
	// source that names none.
	noCollText, err := runCmd(t, newInspectCmd(cfg), "nocoll", "--only", "dbStats")
	require.NoError(t, err)
	require.Contains(t, noCollText, "dbStats")

	// No args runs every subcommand; the text output must show them all, not stop
	// after the first.
	allText, err := runCmd(t, newInspectCmd(cfg))
	require.NoError(t, err)
	require.Contains(t, allText, "# dbStats")
	require.Contains(t, allText, "# buildInfo")

	all, err := runCmd(t, newInspectCmd(cfg), "--json")
	require.NoError(t, err)
	var byName map[string]any
	require.NoError(t, json.Unmarshal([]byte(all), &byName))
	require.Contains(t, byName, "dbStats")
	require.Contains(t, byName, "listCollections")

	// --list runs through RunE (opening the store) and prints the supported set.
	listOut, err := runCmd(t, newInspectCmd(cfg), "--list")
	require.NoError(t, err)
	require.Contains(t, listOut, "dbStats")
	require.Contains(t, listOut, "buildInfo")
}

// recordingInspector is a store that implements every driver-method inspector
// port and the raw Query port. Each read records its name. A read returns the
// error that errs sets for its name, else a value that is the name.
type recordingInspector struct {
	fakeInspectStore
	calls []string
	ctxs  []context.Context
	errs  map[string]error
}

func (r *recordingInspector) read(ctx context.Context, name string) (any, error) {
	r.calls = append(r.calls, name)
	r.ctxs = append(r.ctxs, ctx)
	if err := r.errs[name]; err != nil {
		return nil, err
	}
	return name, nil
}

func (r *recordingInspector) Query(ctx context.Context, args []string) (any, error) {
	return r.read(ctx, args[0])
}

func (r *recordingInspector) InspectTables(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectTables")
}

func (r *recordingInspector) InspectTable(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectTable")
}

func (r *recordingInspector) InspectServer(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectServer")
}

func (r *recordingInspector) InspectDatabases(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectDatabases")
}

func (r *recordingInspector) InspectDBInfo(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectDBInfo")
}

func (r *recordingInspector) InspectIndexes(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectIndexes")
}

func (r *recordingInspector) InspectCluster(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectCluster")
}

func (r *recordingInspector) InspectBuckets(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectBuckets")
}

func (r *recordingInspector) InspectCollections(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectCollections")
}

func (r *recordingInspector) InspectIndices(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectIndices")
}

func (r *recordingInspector) InspectMapping(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectMapping")
}

func (r *recordingInspector) InspectAliases(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectAliases")
}

func (r *recordingInspector) InspectLabels(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectLabels")
}

func (r *recordingInspector) InspectRelationshipTypes(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectRelationshipTypes")
}

func (r *recordingInspector) InspectConstraints(ctx context.Context) (any, error) {
	return r.read(ctx, "InspectConstraints")
}

// dispatchInspectText runs dispatchInspect with text output for a source URL and
// returns the output without color escapes.
func dispatchInspectText(t *testing.T, st store, url string, only []string) (string, error) {
	t.Helper()
	cfg := &config{url: url, source: iqconfig.Source{URL: url}, handle: "src"}
	var buf bytes.Buffer
	err := dispatchInspect(t.Context(), inspectRequest{out: &buf, st: st, cfg: cfg, only: only})
	return stripANSI(buf.String()), err
}

// TestDispatchInspectListComesFirst pins rule 1: with list, every inspector
// returns its supported names before it uses the store and before it validates
// a name. A nil store and an unknown name do not cause an error.
func TestDispatchInspectListComesFirst(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"mongo", "mongodb://h/db", "dbStats\nserverStatus\nlistCollections\ncollStats\nbuildInfo\nhostInfo\n"},
		{"cassandra", "cassandra://h/ks", "local\ntables\ncolumns\n"},
		{"dynamodb", "dynamodb://h", "tables\ntable\n"},
		{"hbase", "hbase://h", "tables\n"},
		{"couchdb", "couchdb://h", "server\ndatabases\ndbinfo\nindexes\n"},
		{"couchbase", "couchbase://h", "cluster\nbuckets\ncollections\nindexes\n"},
		{"elasticsearch", "elasticsearch://h", "server\nindices\nmapping\naliases\n"},
		{"opensearch", "opensearch://h", "server\nindices\nmapping\naliases\n"},
		{"neo4j", "neo4j://h", "server\ndatabases\nlabels\nreltypes\nconstraints\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config{url: tt.url, source: iqconfig.Source{URL: tt.url}, handle: "src"}
			var buf bytes.Buffer
			err := dispatchInspect(t.Context(), inspectRequest{out: &buf, cfg: cfg, only: []string{"bogus"}, list: true})
			out := stripANSI(buf.String())

			require.NoError(t, err)
			require.Equal(t, tt.want, out)
		})
	}
}

// TestDispatchInspectStoreCheckBeforeValidation pins the order in the
// driver-method inspectors: the store check comes before the name check, so a
// store without the port reports that, even for an unknown name.
func TestDispatchInspectStoreCheckBeforeValidation(t *testing.T) {
	for _, url := range []string{"dynamodb://h", "hbase://h", "couchdb://h", "couchbase://h", "elasticsearch://h", "neo4j://h"} {
		t.Run(url, func(t *testing.T) {
			out, err := dispatchInspectText(t, nil, url, []string{"bogus"})

			require.EqualError(t, err, "inspect is not supported for this source")
			require.Empty(t, out)
		})
	}
}

// TestDispatchInspectValidatesBeforeReads pins rule 2: an unknown name after a
// valid name fails with the exact error text, and no read runs.
func TestDispatchInspectValidatesBeforeReads(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		only    []string
		wantErr string
	}{
		{"mongo", "mongodb://h/db", []string{"dbStats", "bogus"}, `unknown inspect subcommand "bogus"; want one of dbStats, serverStatus, listCollections, collStats, buildInfo, hostInfo`},
		{"cassandra", "cassandra://h/ks", []string{"local", "bogus"}, `unknown inspect subcommand "bogus"; want one of local, tables, columns`},
		{"dynamodb", "dynamodb://h", []string{"tables", "bogus"}, `unknown inspect subcommand "bogus"; want one of tables, table`},
		{"hbase", "hbase://h", []string{"tables", "bogus"}, `unknown inspect subcommand "bogus"; want one of tables`},
		{"couchdb", "couchdb://h", []string{"server", "bogus"}, `unknown inspect subcommand "bogus"; want one of server, databases, dbinfo, indexes`},
		{"couchbase", "couchbase://h", []string{"cluster", "bogus"}, `unknown inspect subcommand "bogus"; want one of cluster, buckets, collections, indexes`},
		{"elasticsearch", "elasticsearch://h", []string{"server", "bogus"}, `unknown inspect subcommand "bogus"; want one of server, indices, mapping, aliases`},
		{"neo4j", "neo4j://h", []string{"server", "bogus"}, `unknown inspect subcommand "bogus"; want one of server, databases, labels, reltypes, constraints`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &recordingInspector{}

			out, err := dispatchInspectText(t, st, tt.url, tt.only)

			require.EqualError(t, err, tt.wantErr)
			require.Empty(t, out)
			require.Empty(t, st.calls)
		})
	}
}

// TestDispatchInspectRunsInRequestOrder pins rule 3: the reads run in the
// order of the request, and a repeated name runs again.
func TestDispatchInspectRunsInRequestOrder(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		only      []string
		wantCalls []string
		wantOut   string
	}{
		{
			"mongo",
			"mongodb://h/db",
			[]string{"buildInfo", "dbStats", "buildInfo"},
			[]string{`{"buildInfo":1}`, `{"dbStats":1}`, `{"buildInfo":1}`},
			"mongo  mongodb://h/db\n\n" +
				"# buildInfo\n<{\"buildInfo\":1}>\n\n" +
				"# dbStats\n<{\"dbStats\":1}>\n\n" +
				"# buildInfo\n<{\"buildInfo\":1}>\n\n",
		},
		{
			"cassandra",
			"cassandra://h/ks",
			[]string{"tables", "local", "tables"},
			[]string{
				"SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'ks'",
				"SELECT cluster_name, release_version, cql_version FROM system.local",
				"SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'ks'",
			},
			"cassandra  cassandra://h/ks\n\n" +
				"# tables\n<SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'ks'>\n\n" +
				"# local\n<SELECT cluster_name, release_version, cql_version FROM system.local>\n\n" +
				"# tables\n<SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'ks'>\n\n",
		},
		{
			"dynamodb",
			"dynamodb://h",
			[]string{"table", "tables", "table"},
			[]string{"InspectTable", "InspectTables", "InspectTable"},
			"dynamodb  dynamodb://h\n\n" +
				"# table\n<InspectTable>\n\n" +
				"# tables\n<InspectTables>\n\n" +
				"# table\n<InspectTable>\n\n",
		},
		{
			"hbase",
			"hbase://h",
			[]string{"tables", "tables"},
			[]string{"InspectTables", "InspectTables"},
			"hbase  hbase://h\n\n" +
				"# tables\n<InspectTables>\n\n" +
				"# tables\n<InspectTables>\n\n",
		},
		{
			"couchdb",
			"couchdb://h",
			[]string{"indexes", "server", "indexes"},
			[]string{"InspectIndexes", "InspectServer", "InspectIndexes"},
			"couchdb  couchdb://h\n\n" +
				"# indexes\n<InspectIndexes>\n\n" +
				"# server\n<InspectServer>\n\n" +
				"# indexes\n<InspectIndexes>\n\n",
		},
		{
			"couchbase",
			"couchbase://h",
			[]string{"indexes", "cluster", "indexes"},
			[]string{"InspectIndexes", "InspectCluster", "InspectIndexes"},
			"couchbase  couchbase://h\n\n" +
				"# indexes\n<InspectIndexes>\n\n" +
				"# cluster\n<InspectCluster>\n\n" +
				"# indexes\n<InspectIndexes>\n\n",
		},
		{
			"elasticsearch",
			"elasticsearch://h",
			[]string{"aliases", "mapping", "aliases"},
			[]string{"InspectAliases", "InspectMapping", "InspectAliases"},
			"elasticsearch  elasticsearch://h\n\n" +
				"# aliases\n<InspectAliases>\n\n" +
				"# mapping\n<InspectMapping>\n\n" +
				"# aliases\n<InspectAliases>\n\n",
		},
		{
			"neo4j",
			"neo4j://h",
			[]string{"constraints", "reltypes", "constraints"},
			[]string{"InspectConstraints", "InspectRelationshipTypes", "InspectConstraints"},
			"neo4j  neo4j://h\n\n" +
				"# constraints\n<InspectConstraints>\n\n" +
				"# reltypes\n<InspectRelationshipTypes>\n\n" +
				"# constraints\n<InspectConstraints>\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &recordingInspector{}

			out, err := dispatchInspectText(t, st, tt.url, tt.only)

			require.NoError(t, err)
			require.Equal(t, tt.wantCalls, st.calls)
			require.Equal(t, tt.wantOut, out)
		})
	}
}

// TestDispatchInspectReadErrors pins rules 4 and 6 for the driver-method
// inspectors. A scoped read always runs. In run-all mode, any error from it
// skips the name. In explicit mode, the error object goes into the results. A
// failed read that is not scoped records the error object in both modes.
func TestDispatchInspectReadErrors(t *testing.T) {
	denied := errors.New("permission denied")
	tests := []struct {
		name      string
		url       string
		only      []string
		errs      map[string]error
		wantCalls []string
		wantOut   string
	}{
		{
			"dynamodb table error skips in run-all",
			"dynamodb://h",
			nil,
			map[string]error{"InspectTable": denied},
			[]string{"InspectTables", "InspectTable"},
			"dynamodb  dynamodb://h\n\n# tables\n<InspectTables>\n\n",
		},
		{
			"dynamodb table error records when explicit",
			"dynamodb://h",
			[]string{"table"},
			map[string]error{"InspectTable": denied},
			[]string{"InspectTable"},
			"dynamodb  dynamodb://h\n\n# table\n<map[error:permission denied]>\n\n",
		},
		{
			"dynamodb tables error records in run-all",
			"dynamodb://h",
			nil,
			map[string]error{"InspectTables": denied},
			[]string{"InspectTables", "InspectTable"},
			"dynamodb  dynamodb://h\n\n# tables\n<map[error:permission denied]>\n\n# table\n<InspectTable>\n\n",
		},
		{
			"hbase tables error records in run-all",
			"hbase://h",
			nil,
			map[string]error{"InspectTables": denied},
			[]string{"InspectTables"},
			"hbase  hbase://h\n\n# tables\n<map[error:permission denied]>\n\n",
		},
		{
			"couchdb dbinfo and indexes errors skip in run-all",
			"couchdb://h",
			nil,
			map[string]error{"InspectDBInfo": denied, "InspectIndexes": denied},
			[]string{"InspectServer", "InspectDatabases", "InspectDBInfo", "InspectIndexes"},
			"couchdb  couchdb://h\n\n# server\n<InspectServer>\n\n# databases\n<InspectDatabases>\n\n",
		},
		{
			"couchdb dbinfo and indexes errors record when explicit",
			"couchdb://h",
			[]string{"dbinfo", "indexes"},
			map[string]error{"InspectDBInfo": denied, "InspectIndexes": denied},
			[]string{"InspectDBInfo", "InspectIndexes"},
			"couchdb  couchdb://h\n\n# dbinfo\n<map[error:permission denied]>\n\n# indexes\n<map[error:permission denied]>\n\n",
		},
		{
			"couchdb dbinfo error records redacted when explicit",
			"couchdb://u:pw@h",
			[]string{"dbinfo"},
			map[string]error{"InspectDBInfo": errors.New("get couchdb://u:pw@h/db: denied")},
			[]string{"InspectDBInfo"},
			"couchdb  couchdb://u:xxxxx@h\n\n# dbinfo\n<map[error:get couchdb://u:xxxxx@h/db: denied]>\n\n",
		},
		{
			"couchdb server error records in run-all",
			"couchdb://h",
			nil,
			map[string]error{"InspectServer": denied},
			[]string{"InspectServer", "InspectDatabases", "InspectDBInfo", "InspectIndexes"},
			"couchdb  couchdb://h\n\n# server\n<map[error:permission denied]>\n\n# databases\n<InspectDatabases>\n\n" +
				"# dbinfo\n<InspectDBInfo>\n\n# indexes\n<InspectIndexes>\n\n",
		},
		{
			"couchbase collections error skips in run-all",
			"couchbase://h",
			nil,
			map[string]error{"InspectCollections": denied},
			[]string{"InspectCluster", "InspectBuckets", "InspectCollections", "InspectIndexes"},
			"couchbase  couchbase://h\n\n# cluster\n<InspectCluster>\n\n# buckets\n<InspectBuckets>\n\n# indexes\n<InspectIndexes>\n\n",
		},
		{
			"couchbase collections error records when explicit",
			"couchbase://h",
			[]string{"collections"},
			map[string]error{"InspectCollections": denied},
			[]string{"InspectCollections"},
			"couchbase  couchbase://h\n\n# collections\n<map[error:permission denied]>\n\n",
		},
		{
			"couchbase indexes error records in run-all",
			"couchbase://h",
			nil,
			map[string]error{"InspectIndexes": denied},
			[]string{"InspectCluster", "InspectBuckets", "InspectCollections", "InspectIndexes"},
			"couchbase  couchbase://h\n\n# cluster\n<InspectCluster>\n\n# buckets\n<InspectBuckets>\n\n" +
				"# collections\n<InspectCollections>\n\n# indexes\n<map[error:permission denied]>\n\n",
		},
		{
			"elasticsearch mapping cancellation skips in run-all",
			"elasticsearch://h",
			nil,
			map[string]error{"InspectMapping": context.Canceled},
			[]string{"InspectServer", "InspectIndices", "InspectMapping", "InspectAliases"},
			"elasticsearch  elasticsearch://h\n\n# server\n<InspectServer>\n\n# indices\n<InspectIndices>\n\n# aliases\n<InspectAliases>\n\n",
		},
		{
			"elasticsearch mapping error records when explicit",
			"elasticsearch://h",
			[]string{"mapping"},
			map[string]error{"InspectMapping": denied},
			[]string{"InspectMapping"},
			"elasticsearch  elasticsearch://h\n\n# mapping\n<map[error:permission denied]>\n\n",
		},
		{
			"neo4j labels error records in run-all",
			"neo4j://h",
			nil,
			map[string]error{"InspectLabels": denied},
			[]string{"InspectServer", "InspectDatabases", "InspectLabels", "InspectRelationshipTypes", "InspectConstraints"},
			"neo4j  neo4j://h\n\n# server\n<InspectServer>\n\n# databases\n<InspectDatabases>\n\n# labels\n<map[error:permission denied]>\n\n" +
				"# reltypes\n<InspectRelationshipTypes>\n\n# constraints\n<InspectConstraints>\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &recordingInspector{errs: tt.errs}

			out, err := dispatchInspectText(t, st, tt.url, tt.only)

			require.NoError(t, err)
			require.Equal(t, tt.wantCalls, st.calls)
			require.Equal(t, tt.wantOut, out)
		})
	}
}

// TestDispatchInspectStatementMissingTarget pins rule 5 for the statement
// inspectors. In run-all mode, a name that needs a missing target is skipped.
// In explicit mode, the run stops at that name: the reads before it run, their
// results are discarded, nothing renders, and the error has one frame.
func TestDispatchInspectStatementMissingTarget(t *testing.T) {
	const (
		collStatsErr = "collStats needs a collection; address it as handle.collection or set ?collection= on the source URI"
		columnsErr   = "columns needs a table; address it as handle.table or set ?table= on the source URI"
		localStmt    = "SELECT cluster_name, release_version, cql_version FROM system.local"
		tablesStmt   = "SELECT table_name FROM system_schema.tables WHERE keyspace_name = 'ks'"
	)
	tests := []struct {
		name      string
		url       string
		only      []string
		wantCalls []string
		wantOut   string
		wantErr   string
	}{
		{
			"mongo collStats with no collection stops when explicit",
			"mongodb://h/db",
			[]string{"dbStats", "collStats"},
			[]string{`{"dbStats":1}`},
			"",
			collStatsErr,
		},
		{
			"mongo collStats with no collection skips in run-all",
			"mongodb://h/db",
			nil,
			[]string{`{"dbStats":1}`, `{"serverStatus":1}`, `{"listCollections":1}`, `{"buildInfo":1}`, `{"hostInfo":1}`},
			"mongo  mongodb://h/db\n\n" +
				"# dbStats\n<{\"dbStats\":1}>\n\n" +
				"# serverStatus\n<{\"serverStatus\":1}>\n\n" +
				"# listCollections\n<{\"listCollections\":1}>\n\n" +
				"# buildInfo\n<{\"buildInfo\":1}>\n\n" +
				"# hostInfo\n<{\"hostInfo\":1}>\n\n",
			"",
		},
		{
			"cassandra columns with no table stops when explicit",
			"cassandra://h/ks",
			[]string{"local", "columns"},
			[]string{localStmt},
			"",
			columnsErr,
		},
		{
			"cassandra columns with no table skips in run-all",
			"cassandra://h/ks",
			nil,
			[]string{localStmt, tablesStmt},
			"cassandra  cassandra://h/ks\n\n# local\n<" + localStmt + ">\n\n# tables\n<" + tablesStmt + ">\n\n",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &recordingInspector{}

			out, err := dispatchInspectText(t, st, tt.url, tt.only)

			require.Equal(t, tt.wantCalls, st.calls)
			require.Equal(t, tt.wantOut, out)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
			require.NoError(t, errors.Unwrap(err), "the error must wrap no cause")
			require.Nil(t, stackFrames(err))
			var rendered bytes.Buffer
			renderErrorJSON(&rendered, err)
			require.Equal(t, `{"error":{"message":"`+tt.wantErr+`"}}`+"\n", rendered.String())
		})
	}
}

// TestDispatchInspectStatementReadError pins rule 6 for the statement
// inspectors: a failed read records the error object and the run continues.
func TestDispatchInspectStatementReadError(t *testing.T) {
	st := &recordingInspector{errs: map[string]error{`{"dbStats":1}`: errors.New("permission denied")}}

	out, err := dispatchInspectText(t, st, "mongodb://h/db", []string{"dbStats", "buildInfo"})

	require.NoError(t, err)
	require.Equal(t, []string{`{"dbStats":1}`, `{"buildInfo":1}`}, st.calls)
	require.Equal(t, "mongo  mongodb://h/db\n\n"+
		"# dbStats\n<map[error:run query \"{\\\"dbStats\\\":1}\": permission denied]>\n\n"+
		"# buildInfo\n<{\"buildInfo\":1}>\n\n", out)
}

// inspectCtxKey is the context key that TestDispatchInspectForwardsContextToReads
// uses to mark the context of the caller.
type inspectCtxKey struct{}

// TestDispatchInspectForwardsContextToReads proves that every family gives the
// context of the caller to each read, so a deadline or a cancellation reaches
// the backend.
func TestDispatchInspectForwardsContextToReads(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantReads int
	}{
		{"redis", "redis://h:6379/0", 1},
		{"mongo", "mongodb://h/db?collection=books", 6},
		{"cassandra", "cassandra://h/ks?table=t", 3},
		{"dynamodb", "dynamodb://h", 2},
		{"hbase", "hbase://h", 1},
		{"couchdb", "couchdb://h", 4},
		{"couchbase", "couchbase://h", 4},
		{"elasticsearch", "elasticsearch://h", 4},
		{"neo4j", "neo4j://h", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &recordingInspector{}
			ctx := context.WithValue(context.Background(), inspectCtxKey{}, "marker")
			cfg := &config{url: tt.url, source: iqconfig.Source{URL: tt.url}, handle: "src"}
			var buf bytes.Buffer

			err := dispatchInspect(ctx, inspectRequest{out: &buf, st: st, cfg: cfg, jsonOut: true})

			require.NoError(t, err)
			require.Len(t, st.ctxs, tt.wantReads)
			for _, got := range st.ctxs {
				require.NotNil(t, got)
				require.Equal(t, "marker", got.Value(inspectCtxKey{}))
			}
		})
	}
}

// TestDispatchInspectListYAML proves that dispatchInspect gives the yaml flag to
// the inspector: the list comes out as a YAML sequence.
func TestDispatchInspectListYAML(t *testing.T) {
	cfg := &config{url: "dynamodb://h", source: iqconfig.Source{URL: "dynamodb://h"}}
	var buf bytes.Buffer

	err := dispatchInspect(t.Context(), inspectRequest{out: &buf, cfg: cfg, yamlOut: true, list: true})

	require.NoError(t, err)
	require.Equal(t, "- tables\n- table\n", buf.String())
}

// redisInfoStore is a store whose Query returns a fixed INFO reply.
type redisInfoStore struct {
	fakeInspectStore
	info string
}

func (s redisInfoStore) Query(context.Context, []string) (any, error) { return s.info, nil }

// TestDispatchInspectRedisYAML proves that Redis INFO renders as YAML with
// yamlOut and no list.
func TestDispatchInspectRedisYAML(t *testing.T) {
	st := redisInfoStore{info: "# Server\r\nredis_version:7.2.0\r\n\r\n# Memory\r\nused_memory:12345\r\n"}
	cfg := &config{url: "redis://h:6379/0", source: iqconfig.Source{URL: "redis://h:6379/0"}}
	var buf bytes.Buffer

	err := dispatchInspect(t.Context(), inspectRequest{out: &buf, st: st, cfg: cfg, yamlOut: true})

	require.NoError(t, err)
	require.Equal(t, "Memory:\n    used_memory: \"12345\"\nServer:\n    redis_version: 7.2.0\n", buf.String())
}

// failingWriter fails every write and counts the writes.
type failingWriter struct {
	writes int
	err    error
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, w.err
}

// TestDispatchInspectRedisHeaderWriteError proves that Redis stops and returns
// the error when the header write fails.
func TestDispatchInspectRedisHeaderWriteError(t *testing.T) {
	st := redisInfoStore{info: "# Server\r\nredis_version:7.2.0\r\n"}
	cfg := &config{url: "redis://h:6379/0", source: iqconfig.Source{URL: "redis://h:6379/0"}}
	w := &failingWriter{err: errors.New("disk full")}

	err := dispatchInspect(t.Context(), inspectRequest{out: w, st: st, cfg: cfg})

	require.ErrorIs(t, err, w.err)
	require.Equal(t, 1, w.writes)
}
