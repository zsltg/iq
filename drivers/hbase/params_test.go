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
