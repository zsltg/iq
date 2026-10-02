package elasticsearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// searchRecorder answers a raw _search and keeps the path and body it received.
type searchRecorder struct {
	path string
	body string
}

func (r *searchRecorder) handler(reply string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.path, r.body = req.URL.Path, string(b)
		esJSON(w, http.StatusOK, reply)
	}
}

func TestQueryRequestBody(t *testing.T) {
	// A bare query object is wrapped as {"query":...}. A body that has a query key
	// goes out as it is. The request goes to the index search path.
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{"bare query is wrapped", `{"match":{"title":"dune"}}`, `{"query":{"match":{"title":"dune"}}}`},
		{"full body is kept", `{"query":{"match_all":{}},"size":3}`, `{"query":{"match_all":{}},"size":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &searchRecorder{}
			st := newStubStore(t, "books", rec.handler(`{"hits":{"hits":[]}}`))

			_, err := st.Query(t.Context(), []string{tt.arg})
			require.NoError(t, err)
			require.Equal(t, "/books/_search", rec.path)
			require.JSONEq(t, tt.want, rec.body)
		})
	}
}

func TestQueryRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		index   string
		args    []string
		wantErr string
	}{
		{"no index", "", []string{`{}`}, "no index selected"},
		{"no argument", "books", nil, "raw exec expects one JSON search body"},
		{"two arguments", "books", []string{`{}`, `{}`}, "raw exec expects one JSON search body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStubStore(t, tt.index, func(w http.ResponseWriter, _ *http.Request) {
				esJSON(w, http.StatusOK, `{}`)
			})
			got, err := st.Query(t.Context(), tt.args)
			require.Nil(t, got)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestQueryReportsARefusedSearch(t *testing.T) {
	// A refused search carries the backend label, the operation, and the reason.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusBadRequest, `{"error":{"type":"parsing_exception","reason":"bad query"}}`)
	})
	got, err := st.Query(t.Context(), []string{`{"match_all":{}}`})
	require.Nil(t, got)
	require.ErrorContains(t, err, "elasticsearch search")
	require.ErrorContains(t, err, "parsing_exception")
}

func TestQueryReportsATransportFailure(t *testing.T) {
	// A canceled request is a transport failure, wrapped with the backend label and the operation.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{}`)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	defer cancel()
	got, err := st.Query(ctx, []string{`{"match_all":{}}`})
	require.Nil(t, got)
	require.ErrorContains(t, err, "elasticsearch search: ")
}

func TestQueryDecodesNumbersExactly(t *testing.T) {
	// The reply keeps big integers exact, so a count above 2^53 does not round.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{"hits":{"total":{"value":9007199254740993}}}`)
	})
	got, err := st.Query(t.Context(), []string{`{"match_all":{}}`})
	require.NoError(t, err)
	out := st.FormatRaw(got, false)
	require.Contains(t, out, "9007199254740993")
}

func TestQueryKeepsTheCauseOfATransportFailure(t *testing.T) {
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := st.Query(ctx, []string{`{"match_all":{}}`})

	require.ErrorIs(t, err, context.Canceled, "the wrap keeps the cause for errors.Is")
	require.ErrorContains(t, err, "elasticsearch search:")
}

func TestSearchBodyRejectsAnInvalidSearchAfter(t *testing.T) {
	st := &Store{client: esFlavor{}, pageSize: 1}

	raw, err := st.searchBody("PIT-1", nil, json.RawMessage(`{bad`))

	require.Nil(t, raw)
	require.ErrorContains(t, err, "encode search:")
	var syntax *json.MarshalerError
	require.ErrorAs(t, err, &syntax, "the wrap keeps the encoder error")
}
