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
