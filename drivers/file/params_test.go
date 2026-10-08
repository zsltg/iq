package file

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestURIParamsAreRead makes sure that every option in the catalogue changes the
// parsed result, and that every row below names a catalogue option.
func TestURIParamsAreRead(t *testing.T) {
	const base = "file:///dumps/d.json"
	tests := []struct{ param, with string }{
		{paramFormat, base + "?format=bson"},
		{paramTypes, base + "?types=a=int"},
		{paramKeys, base + "?keys=pk"},
		{paramColumns, base + "?columns=a,b"},
		{paramLabel, base + "?label=Person"},
		{paramRel, base + "?rel=KNOWS"},
		{paramKey, base + "?key=id"},
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

	_, unsetFormat, unsetHints, err := parseFileURL(base)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.param, func(t *testing.T) {
			_, format, hints, err := parseFileURL(tt.with)
			require.NoError(t, err)
			require.NotEqual(t, []any{unsetFormat, unsetHints}, []any{format, hints})
		})
	}
}

// TestURIParamsClosedValues feeds every listed value of a catalogue option to the
// parser.
func TestURIParamsClosedValues(t *testing.T) {
	for _, p := range URIParams {
		for _, v := range p.Values {
			t.Run(p.Name+"="+v, func(t *testing.T) {
				_, format, _, err := parseFileURL("file:///dumps/d.json?" + p.Name + "=" + v)
				require.NoError(t, err)
				require.NotEqual(t, FormatUnknown, format)
				require.Equal(t, v, format.String(), "the listed value is the canonical name")
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
		{"format", false, []string{"jsonl", "yaml", "mongoexport", "bson", "rdb", "dynamodb-json", "cassandra-csv", "neo4j-json"}},
		{"types", false, nil},
		{"keys", false, nil},
		{"columns", false, nil},
		{"label", false, nil},
		{"rel", false, nil},
		{"key", false, nil},
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

// TestURIParamsRejectUnlisted makes sure that the parser rejects a value that a
// closed option does not list.
func TestURIParamsRejectUnlisted(t *testing.T) {
	for _, p := range URIParams {
		if len(p.Values) == 0 {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			_, _, _, err := parseFileURL("file:///dumps/d.json?" + p.Name + "=unlisted")
			require.Error(t, err)
		})
	}
}
