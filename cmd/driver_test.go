package cmd

import (
	"encoding/json"
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
		{"unknown", "couchbase", "", false},
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
		{"unknown falls back to scheme", "couchbase://h", "couchbase"},
		{"schemeless is empty", "just-a-string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, driverName(tt.url))
		})
	}
}

func TestExpectedSchemes(t *testing.T) {
	require.Equal(t, "expected one of mongodb://, cassandra://, dynamodb://, hbase://, couchdb://, neo4j://, elasticsearch://, redis://", expectedSchemes())
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
	out, err := runCmd(t, newDriverCmd(), "ls")
	require.NoError(t, err)
	for _, want := range []string{
		"DRIVER", "DESCRIPTION", "SCHEMES", "VERSIONS", "DOC",
		"mongo", "MongoDB document store", "mongodb, mongodb+srv", "4.2+", "https://www.mongodb.com/docs/",
		"redis", "Redis key-value store", "redis, rediss", "7.0+", "https://redis.io/docs/",
		"cassandra", "Apache Cassandra wide-column store", "3.11+", "https://cassandra.apache.org/doc/",
		"dynamodb", "Amazon DynamoDB key-value and document store", "AWS (managed)", "https://docs.aws.amazon.com/dynamodb/",
		"hbase", "Apache HBase wide-column store", "1.0+", "https://hbase.apache.org/book.html",
		"couchdb", "Apache CouchDB document store", "couchdb, couchdbs", "2.x, 3.x", "https://docs.couchdb.org/",
		"neo4j", "Neo4j property graph store", "neo4j, neo4j+s, neo4j+ssc, bolt, bolt+s, bolt+ssc", "5.x", "https://neo4j.com/docs/",
		"elasticsearch", "Elasticsearch search engine and document store", "elasticsearch, elasticsearch+s", "8.x", "https://www.elastic.co/docs/",
	} {
		require.Contains(t, out, want)
	}
}

func TestDriverLsJSON(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(), "ls", "--json")
	require.NoError(t, err)

	var rows []driverRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 9)

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
	require.Equal(t, []string{"elasticsearch", "elasticsearch+s"}, byName["elasticsearch"].Schemes)
	require.Equal(t, "Elasticsearch search engine and document store", byName["elasticsearch"].Description)
	require.Equal(t, "8.x", byName["elasticsearch"].Versions)
	require.Equal(t, "https://www.elastic.co/docs/", byName["elasticsearch"].Doc)
}
