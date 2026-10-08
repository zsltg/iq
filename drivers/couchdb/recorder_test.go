package couchdb

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"
)

// ctxKey keys the marker that keyedCtx stores, so a test can tell that a call got the
// caller's context and not a fresh one.
type ctxKey struct{}

// keyedCtx returns a context that carries a marker value.
func keyedCtx() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "marker")
}

// requireKeyed fails when ctx is not derived from keyedCtx.
func requireKeyed(t *testing.T, ctx context.Context) {
	t.Helper()
	require.Equal(t, "marker", ctx.Value(ctxKey{}), "the call must carry the caller's context")
}

// bookmarkRows adds the paging bookmark that a _find reply carries to mock rows.
type bookmarkRows struct {
	driver.Rows
	mark string
}

// Bookmark returns the paging bookmark of the reply.
func (b bookmarkRows) Bookmark() string { return b.mark }

// withBookmark returns rows as a _find reply that carries the bookmark mark.
func withBookmark(rows *mockdb.Rows, mark string) driver.Rows {
	return bookmarkRows{Rows: rows.Final(), mark: mark}
}

// statusError is an error that carries an HTTP status, like a driver error.
type statusError struct{ code int }

func (e statusError) Error() string { return "status error" }

// HTTPStatus returns the status code, as kivik.HTTPStatus expects.
func (e statusError) HTTPStatus() int { return e.code }

// queryMap decodes the _find request that the driver received. The mock hands it over
// as JSON, so numbers decode as float64.
func queryMap(t *testing.T, query any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(query)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
