package couchdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "couchdb://h:5984/"
	tests := []struct{ param, with string }{
		{paramDatabase, base + "?database=orders"},
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

	unset, err := parseURL(base, "")
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.param, func(t *testing.T) {
			set, err := parseURL(tt.with, "")
			require.NoError(t, err)
			require.NotEqual(t, unset, set)
		})
	}
}
