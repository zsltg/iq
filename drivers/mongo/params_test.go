package mongo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "mongodb://h/db"
	tests := []struct{ param, with string }{
		{collectionParam, base + "?collection=orders"},
	}
	var names []string
	for _, p := range URIParams {
		names = append(names, p.Name)
	}
	var rows []string
	for _, tt := range tests {
		rows = append(rows, tt.param)
	}
	require.ElementsMatch(t, names, rows, "one row for each catalogue option")

	_, unset := SplitCollection(base)
	for _, tt := range tests {
		t.Run(tt.param, func(t *testing.T) {
			_, set := SplitCollection(tt.with)
			require.NotEqual(t, unset, set)
		})
	}
}

// TestURIParamsCatalogue pins what each catalogue entry says: the option name,
// whether it names a keyspace, a description, and the closed set of values.
func TestURIParamsCatalogue(t *testing.T) {
	tests := []struct {
		name     string
		keyspace bool
		values   []string
	}{
		{"collection", true, nil},
	}
	require.Len(t, URIParams, len(tests))
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := URIParams[i]
			require.Equal(t, tt.name, got.Name)
			require.Equal(t, tt.keyspace, got.Keyspace)
			require.NotEmpty(t, got.Desc, "an option needs a description")
			require.Equal(t, tt.values, got.Values)
		})
	}
}

// TestURIParamsKeyspace makes sure that each keyspace option sets the collection
// that SplitCollection returns.
func TestURIParamsKeyspace(t *testing.T) {
	for _, p := range URIParams {
		if !p.Keyspace {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			_, got := SplitCollection("mongodb://h/db?" + p.Name + "=orders")
			require.Equal(t, "orders", got)
		})
	}
}
