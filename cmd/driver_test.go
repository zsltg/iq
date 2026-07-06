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
		{"unknown", "dynamodb", "", false},
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
		{"unknown falls back to scheme", "dynamodb://h", "dynamodb"},
		{"schemeless is empty", "just-a-string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, driverName(tt.url))
		})
	}
}

func TestExpectedSchemes(t *testing.T) {
	require.Equal(t, "expected one of mongodb://, cassandra://, redis://", expectedSchemes())
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
	} {
		require.Contains(t, out, want)
	}
}

func TestDriverLsJSON(t *testing.T) {
	out, err := runCmd(t, newDriverCmd(), "ls", "--json")
	require.NoError(t, err)

	var rows []driverRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 4)

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
}
