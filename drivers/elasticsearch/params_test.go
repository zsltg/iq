package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result for both URI schemes, and that every row below names a catalogue
// option.
func TestURIParamsAreRead(t *testing.T) {
	tests := []struct{ param, query string }{
		{paramIndex, "?index=books"},
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

	for _, scheme := range []string{"elasticsearch", "opensearch"} {
		base := scheme + "://h:9200/"
		unset, err := parseURL(base, "")
		require.NoError(t, err)
		for _, tt := range tests {
			t.Run(scheme+"/"+tt.param, func(t *testing.T) {
				set, err := parseURL(base+tt.query, "")
				require.NoError(t, err)
				require.NotEqual(t, unset, set)
			})
		}
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
		{"index", true, nil},
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

// TestURIParamsKeyspace makes sure that each keyspace option sets the index that
// parseURL returns, for both URI schemes.
func TestURIParamsKeyspace(t *testing.T) {
	for _, scheme := range []string{"elasticsearch", "opensearch"} {
		for _, p := range URIParams {
			if !p.Keyspace {
				continue
			}
			t.Run(scheme+"/"+p.Name, func(t *testing.T) {
				cc, err := parseURL(scheme+"://h:9200/?"+p.Name+"=books", "")
				require.NoError(t, err)
				require.Equal(t, "books", cc.index)
			})
		}
	}
}
