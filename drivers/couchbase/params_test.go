package couchbase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "couchbase://h/"
	tests := []struct{ param, with string }{
		{paramCollection, base + "?collection=sales.orders"},
		{paramBucket, base + "?bucket=iq"},
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

// TestURIParamsCatalogue pins what each catalogue entry says: the option name,
// whether it names a keyspace, a description, and the closed set of values.
func TestURIParamsCatalogue(t *testing.T) {
	tests := []struct {
		name     string
		keyspace bool
		values   []string
	}{
		{"collection", true, nil},
		{"bucket", true, nil},
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

// TestURIParamsKeyspace makes sure that each keyspace option sets the part of the
// keyspace that parseURL returns.
func TestURIParamsKeyspace(t *testing.T) {
	tests := []struct {
		param string
		value string
		field func(connConfig) string
	}{
		{paramCollection, "sales.orders", func(c connConfig) string { return c.scope + "." + c.coll }},
		{paramBucket, "iq", func(c connConfig) string { return c.bucket }},
	}
	var names []string
	for _, p := range URIParams {
		if p.Keyspace {
			names = append(names, p.Name)
		}
	}
	var rows []string
	for _, tt := range tests {
		rows = append(rows, tt.param)
	}
	require.ElementsMatch(t, names, rows, "one row for each keyspace option")
	for _, tt := range tests {
		t.Run(tt.param, func(t *testing.T) {
			cc, err := parseURL("couchbase://h/?"+tt.param+"="+tt.value, "")
			require.NoError(t, err)
			require.Equal(t, tt.value, tt.field(cc))
		})
	}
}
