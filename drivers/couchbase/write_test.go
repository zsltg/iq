package couchbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestDocumentBody(t *testing.T) {
	tests := []struct {
		name    string
		record  query.Record
		want    map[string]any
		wantErr string
	}{
		{"object", query.Record{Key: "1", Value: map[string]any{"t": "x"}}, map[string]any{"t": "x"}, ""},
		{"scalar is rejected", query.Record{Key: "1", Value: "x"}, nil, "is not a JSON object"},
		{
			"string value that is not valid UTF-8",
			query.Record{Key: "1", Value: map[string]any{"t": []any{"x\xff"}}},
			nil,
			`couchbase: key "1": field "t[0]" holds a string that is not valid UTF-8`,
		},
		{
			"field name that is not valid UTF-8",
			query.Record{Key: "1", Value: map[string]any{"t\xff": 1}},
			nil,
			`couchbase: key "1": the field name "t\xff" is not valid UTF-8`,
		},
		{"a KV key is sent as bytes and is not checked", query.Record{Key: "1\xff", Value: map[string]any{"t": "x"}}, map[string]any{"t": "x"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := documentBody(tt.record)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
