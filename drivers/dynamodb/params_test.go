package dynamodb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "dynamodb://us-east-1/"
	tests := []struct{ param, with string }{
		{paramTable, base + "?table=orders"},
		{paramEndpoint, base + "?endpoint=http://localhost:8000"},
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
