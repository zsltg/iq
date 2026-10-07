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
