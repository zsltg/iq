package file

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// readExtJSON reads every record of a mongoexport body.
func readExtJSON(t *testing.T, body string) []query.Record {
	t.Helper()
	var out []query.Record
	err := extJSONSource(strings.NewReader(body), pageSize, numfmt.DecimalAuto)(context.Background(), func(b []query.Record) error {
		out = append(out, b...)
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestExtJSONSourceReadsRelaxedAndCanonical pins that one document gives the
// same record in the relaxed form (the mongoexport default) and the canonical
// form. A line dump and a --jsonArray dump share the reader.
func TestExtJSONSourceReadsRelaxedAndCanonical(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		relaxed   string
	}{
		{
			name:      "date",
			canonical: `{"_id":1,"d":{"$date":{"$numberLong":"1672628645000"}}}`,
			relaxed:   `{"_id":1,"d":{"$date":"2023-01-02T03:04:05Z"}}`,
		},
		{
			name:      "int32",
			canonical: `{"_id":1,"n":{"$numberInt":"5"}}`,
			relaxed:   `{"_id":1,"n":5}`,
		},
		{
			name:      "int64",
			canonical: `{"_id":1,"n":{"$numberLong":"9007199254740993"}}`,
			relaxed:   `{"_id":1,"n":{"$numberLong":"9007199254740993"}}`,
		},
		{
			name:      "double",
			canonical: `{"_id":1,"x":{"$numberDouble":"1.5"}}`,
			relaxed:   `{"_id":1,"x":1.5}`,
		},
		{
			name:      "object id key",
			canonical: `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"a":"b"}`,
			relaxed:   `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"a":"b"}`,
		},
		{
			name:      "nested document and array",
			canonical: `{"_id":1,"o":{"d":{"$date":{"$numberLong":"0"}},"l":[{"$numberInt":"1"},{"$numberLong":"2"}]}}`,
			relaxed:   `{"_id":1,"o":{"d":{"$date":"1970-01-01T00:00:00Z"},"l":[1,{"$numberLong":"2"}]}}`,
		},
	}
	read := func(t *testing.T, body string) []query.Record {
		t.Helper()
		return readExtJSON(t, body)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := read(t, tt.canonical)
			require.Len(t, want, 1)
			require.Equal(t, want, read(t, tt.relaxed), "relaxed line")
			require.Equal(t, want, read(t, "["+tt.relaxed+"]"), "relaxed --jsonArray")
			require.Equal(t, want, read(t, "["+tt.canonical+"]"), "canonical --jsonArray")
		})
	}
}

// TestExtJSONSourceRelaxedDateValue pins the decoded value of a relaxed date.
func TestExtJSONSourceRelaxedDateValue(t *testing.T) {
	recs := readExtJSON(t, `{"_id":1,"d":{"$date":"2023-01-02T03:04:05Z"}}`)
	require.Len(t, recs, 1)
	require.Equal(t, "document", recs[0].Type)
	m, ok := recs[0].Value.(map[string]any)
	require.True(t, ok, "%T", recs[0].Value)
	require.Equal(t, "2023-01-02T03:04:05Z", m["d"])
}
