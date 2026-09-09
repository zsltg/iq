package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectReportsARefusedRead(t *testing.T) {
	// Each inspect call must give a refused read back to the caller. A guard that stops
	// looking at the error returns an empty result and no error, which the caller cannot
	// tell from a server that holds nothing.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusForbidden, `{"error":{"type":"security_exception","reason":"denied"}}`)
	})
	tests := []struct {
		name string
		call func(context.Context) (any, error)
	}{
		{name: "server info", call: st.InspectServer},
		{name: "indices", call: st.InspectIndices},
		{name: "mapping", call: st.InspectMapping},
		{name: "aliases", call: st.InspectAliases},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call(t.Context())
			require.Nil(t, got, "a refused read gives no partial result")
			require.ErrorContains(t, err, "security_exception")
			require.ErrorContains(t, err, "denied")
		})
	}
}

func TestInspectCopiesEveryCatRow(t *testing.T) {
	// The _cat tables are copied row by row into the returned list. A loop that stops
	// before the first copy, or one that drops the copy, still returns a list of the
	// correct length with empty rows in it. Only the content of each row shows this.
	st := newStubStore(t, "books", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_cat/indices":
			esJSON(w, http.StatusOK, `[{"index":"books"},{"index":"authors"}]`)
		case "/_cat/aliases":
			esJSON(w, http.StatusOK, `[{"alias":"current"},{"alias":"previous"}]`)
		default:
			esJSON(w, http.StatusOK, `{}`)
		}
	})
	tests := []struct {
		name  string
		call  func(context.Context) (any, error)
		key   string
		field string
		want  []string
	}{
		{
			name: "indices", call: st.InspectIndices,
			key: "indices", field: "index", want: []string{"books", "authors"},
		},
		{
			name: "aliases", call: st.InspectAliases,
			key: "aliases", field: "alias", want: []string{"current", "previous"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call(t.Context())
			require.NoError(t, err)
			rows, ok := got.(map[string]any)[tt.key].([]any)
			require.True(t, ok, "the reply carries the %s list", tt.key)
			require.Len(t, rows, len(tt.want))
			names := make([]string, 0, len(rows))
			for _, r := range rows {
				row, ok := r.(map[string]any)
				require.True(t, ok, "every row is copied, none is left empty")
				names = append(names, fmt.Sprint(row[tt.field]))
			}
			require.ElementsMatch(t, tt.want, names)
		})
	}
}

func TestInspectMappingDecodesEveryIndexEntry(t *testing.T) {
	// The mapping reply holds one raw entry per index. Each entry is decoded into plain
	// values, so the caller reads a map, not the raw bytes.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{"books":{"mappings":{"properties":{"title":{"type":"text"}}}}}`)
	})
	got, err := st.InspectMapping(t.Context())
	require.NoError(t, err)
	out, ok := got.(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{
		"books": map[string]any{
			"mappings": map[string]any{
				"properties": map[string]any{
					"title": map[string]any{"type": "text"},
				},
			},
		},
	}, out)
}

func TestInspectMappingNeedsAnIndex(t *testing.T) {
	// The mapping is index-scoped, so a store with no index selected must say so before
	// it builds a request path.
	st := newStubStore(t, "", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{}`)
	})
	got, err := st.InspectMapping(t.Context())
	require.Nil(t, got)
	require.ErrorIs(t, err, errNoIndex)
}
