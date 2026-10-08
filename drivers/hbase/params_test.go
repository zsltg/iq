package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "hbase://h:2181/"
	tests := []struct{ param, with string }{
		{paramTable, base + "?table=books"},
		{paramZnode, base + "?znode=/hbase-secure"},
		{paramTypes, base + "?types=cf:age=long"},
		{paramKeytype, base + "?keytype=long"},
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

// TestURIParamsClosedValues feeds every listed value of a catalogue option to the
// parser.
func TestURIParamsClosedValues(t *testing.T) {
	for _, p := range URIParams {
		for _, v := range p.Values {
			t.Run(p.Name+"="+v, func(t *testing.T) {
				_, err := parseURL("hbase://h:2181/?"+p.Name+"="+v, "")
				require.NoError(t, err)
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
		{"table", true, nil},
		{"znode", false, nil},
		{"types", false, nil},
		{"keytype", false, []string{"text", "bytes", "int", "long", "double", "bool"}},
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

// TestURIParamsKeyspace makes sure that each keyspace option sets the table that
// parseURL returns.
func TestURIParamsKeyspace(t *testing.T) {
	for _, p := range URIParams {
		if !p.Keyspace {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			cc, err := parseURL("hbase://h:2181/?"+p.Name+"=ns:books", "")
			require.NoError(t, err)
			require.Equal(t, "ns:books", cc.table)
		})
	}
}

// TestURIParamsRejectUnlisted makes sure that the parser rejects a value that a
// closed option does not list.
func TestURIParamsRejectUnlisted(t *testing.T) {
	for _, p := range URIParams {
		if len(p.Values) == 0 {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			_, err := parseURL("hbase://h:2181/?"+p.Name+"=unlisted", "")
			require.Error(t, err)
		})
	}
}
