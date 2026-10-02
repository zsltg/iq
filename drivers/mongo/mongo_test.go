package mongo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// testURI returns the MongoDB URI for integration tests: the IQ_MONGO_URL override
// first, then the ephemeral container started in TestMain, then a local default.
// The iq_test database keeps the tests off any real data.
func testURI() string {
	if uri := os.Getenv("IQ_MONGO_URL"); uri != "" {
		return uri
	}
	if sharedURI != "" {
		return sharedURI
	}
	return "mongodb://localhost:27017/iq_test"
}

// openIntegration skips under -short, otherwise opens a Store against the test
// MongoDB and registers cleanup.
func openIntegration(t *testing.T, collection string) *Store {
	t.Helper()
	return openIntegrationTraced(t, collection, nil)
}

// openIntegrationTraced is openIntegration with a command trace, so a test can
// assert which commands a call issues — or that it issues none.
func openIntegrationTraced(t *testing.T, collection string, trace io.Writer) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	store, err := Open(ctx, testURI(), collection, trace, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// uriWithCollection appends a ?collection= default to the test URI, the form a
// source string carries when the collection is not addressed explicitly.
func uriWithCollection(t *testing.T, collection string) string {
	t.Helper()
	u, err := url.Parse(testURI())
	require.NoError(t, err)
	q := u.Query()
	q.Set("collection", collection)
	u.RawQuery = q.Encode()
	return u.String()
}

// seedDocs replaces the collection's contents with docs.
func seedDocs(t *testing.T, store *Store, docs []any) {
	t.Helper()
	ctx := context.Background()
	coll := store.db.Collection(store.collection)
	require.NoError(t, coll.Drop(ctx))
	if len(docs) > 0 {
		_, err := coll.InsertMany(ctx, docs)
		require.NoError(t, err)
	}
	t.Cleanup(func() { _ = coll.Drop(ctx) })
}

func TestDatabaseFromURI(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		want    string
		wantErr string
	}{
		{"database in path", "mongodb://localhost:27017/iq", "iq", ""},
		{"database with options", "mongodb://host:27017/iq?replicaSet=rs0", "iq", ""},
		{"srv scheme", "mongodb+srv://cluster.example.com/shop", "shop", ""},
		{"no database", "mongodb://localhost:27017", "", "must name a database"},
		{"trailing slash only", "mongodb://localhost:27017/", "", "must name a database"},
		{"extra path segment", "mongodb://localhost:27017/iq/books", "", "must name a database"},
		{"unparsable uri", "mongodb://localhost:27017/%zz", "", "parse mongodb uri"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := databaseFromURI(tt.uri)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetByID(t *testing.T) {
	store := openIntegration(t, "get_docs")
	oid := bson.NewObjectID()
	seedDocs(t, store, []any{
		bson.M{"_id": "b1", "title": "Go", "year": int32(2015), "tags": bson.A{"go"}},
		bson.M{"_id": oid, "title": "Mongo"},
		bson.M{"_id": "b2", "title": "Unasked"},
	})

	got, err := store.Get(context.Background(), []string{"b1", oid.Hex(), "missing"})
	require.NoError(t, err)

	require.Equal(t, map[string]any{
		"_id": "b1", "title": "Go", "year": 2015, "tags": []any{"go"},
	}, got["b1"])
	require.Equal(t, "Mongo", got[oid.Hex()].(map[string]any)["title"], "ObjectID key matched by hex")
	require.NotContains(t, got, "missing", "an absent _id is absent from the map")
	require.NotContains(t, got, "b2", "a document that was not asked for is not fetched")
}

// TestGetDecimalMode reads a stored Decimal128 through Get under each decimal
// mode, proving the flag threads from Open through normalization to the value the
// filter sees.
func TestGetDecimalMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		want any
	}{
		{name: "auto keeps an exact string", mode: numfmt.DecimalAuto, want: "3.14"},
		{name: "string keeps an exact string", mode: numfmt.DecimalString, want: "3.14"},
		{name: "number becomes a float", mode: numfmt.DecimalNumber, want: 3.14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			store, err := Open(ctx, testURI(), "decimal_docs", nil, tt.mode)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			seedDocs(t, store, []any{bson.M{"_id": "d1", "price": mustDecimal(t, "3.14")}})

			got, err := store.Get(ctx, []string{"d1"})
			require.NoError(t, err)
			require.Equal(t, tt.want, got["d1"].(map[string]any)["price"])
		})
	}
}

func TestGetEmptyKeysNoRoundTrip(t *testing.T) {
	var trace bytes.Buffer
	store := openIntegrationTraced(t, "empty_docs", &trace)

	got, err := store.Get(context.Background(), nil)

	require.NoError(t, err)
	require.Equal(t, map[string]any{}, got)
	require.Empty(t, trace.String(), "an empty key list issues no command at all")
}

func TestGetWithoutCollectionFails(t *testing.T) {
	store := openIntegration(t, "")

	_, err := store.Get(context.Background(), []string{"x"})

	require.ErrorIs(t, err, errNoCollection)
}

func TestScanBatchesYieldsAllDocs(t *testing.T) {
	store := openIntegration(t, "scan_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "n": int32(1)},
		bson.M{"_id": "2", "n": int32(2)},
		bson.M{"_id": "3", "n": int32(3)},
	})

	merged := map[string]any{}
	batches := 0
	err := store.ScanBatches(context.Background(), func(batch map[string]any) error {
		batches++
		maps.Copy(merged, batch)
		return nil
	})
	require.NoError(t, err)

	require.GreaterOrEqual(t, batches, 1)
	require.Len(t, merged, 3)
	require.Equal(t, map[string]any{"_id": "2", "n": 2}, merged["2"])
}

func TestEstimateCountReturnsCollectionSize(t *testing.T) {
	store := openIntegration(t, "estimate_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "n": int32(1)},
		bson.M{"_id": "2", "n": int32(2)},
		bson.M{"_id": "3", "n": int32(3)},
	})

	n, err := store.EstimateCount(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(3), n, "the estimate reflects the seeded collection size")
}

func TestEstimateCountWithoutCollectionFails(t *testing.T) {
	store := openIntegration(t, "")

	n, err := store.EstimateCount(context.Background())

	require.ErrorIs(t, err, errNoCollection)
	require.Zero(t, n, "the error path returns no count")
}

func TestScanBatchesBoundsPageSize(t *testing.T) {
	store := openIntegration(t, "page_docs")
	// Four documents at pageSize 2 means two full pages and an empty final page:
	// exact sizes pin the flush boundary — a page must flush at exactly pageSize,
	// and an empty final page must not be handed to fn.
	docs := make([]any, 4)
	for i := range docs {
		docs[i] = bson.M{"_id": bson.NewObjectID(), "n": int32(i)}
	}
	seedDocs(t, store, docs)
	store.pageSize = 2

	var sizes []int
	err := store.ScanBatches(context.Background(), func(batch map[string]any) error {
		sizes = append(sizes, len(batch))
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, []int{2, 2}, sizes, "two full pages, no empty trailing page")
}

func TestScanFilteredReturnsMatchingSubset(t *testing.T) {
	store := openIntegration(t, "filtered_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "author": "Kleppmann", "year": int32(2017)},
		bson.M{"_id": "2", "author": "Ousterhout", "year": int32(2018)},
		bson.M{"_id": "3", "author": "Kleppmann", "year": int32(2020)},
	})

	merged := map[string]any{}
	err := store.ScanFiltered(
		context.Background(),
		predicate.Eq{Path: []string{"author"}, Value: "Kleppmann"},
		func(batch map[string]any) error {
			maps.Copy(merged, batch)
			return nil
		},
	)
	require.NoError(t, err)

	require.Len(t, merged, 2, "only the matching documents come back")
	require.Contains(t, merged, "1")
	require.Contains(t, merged, "3")
	require.NotContains(t, merged, "2", "a non-matching document is filtered server-side")
}

func TestCompileEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "range_docs")
	// A document whose field f takes every jq type, plus a missing field, so a
	// pushed filter that is ever a subset (missing one) diverges from client-side.
	seedDocs(t, store, []any{
		bson.M{"_id": "num_lo", "f": int32(10)},
		bson.M{"_id": "num_hi", "f": int32(100)},
		bson.M{"_id": "str_lo", "f": "aaa"},
		bson.M{"_id": "str_hi", "f": "zzz"},
		bson.M{"_id": "arr", "f": bson.A{int32(1)}},
		bson.M{"_id": "obj", "f": bson.M{"x": int32(1)}},
		bson.M{"_id": "nul", "f": nil},
		bson.M{"_id": "tru", "f": true},
		bson.M{"_id": "miss"},
	})

	filters := []string{
		".[] | select(.f > 50) | ._id",
		".[] | select(.f >= 100) | ._id",
		".[] | select(.f < 50) | ._id",
		".[] | select(.f <= 10) | ._id",
		`.[] | select(.f > "mmm") | ._id`,
		`.[] | select(.f < "mmm") | ._id`,
		`.[] | select(.f == 100) | ._id`,
		`.[] | select(.f == "aaa") | ._id`,
		".[] | select(.f > 5 and .f < 50) | ._id",
		`.[] | select(.f > 50 or .f == "aaa") | ._id`,
		".[] | select(.f == 10 or .f == 100) | ._id", // same-field or -> $in
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed,
				"compiled result must equal the client-side result for every type")
		})
	}
}

func TestCompileExistsAndSizeEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "sizeexists_docs")
	// f takes every type jq length handles (array, string, object, number, null,
	// and a missing field); opt is present on only one document.
	seedDocs(t, store, []any{
		bson.M{"_id": "a3", "f": bson.A{int32(1), int32(2), int32(3)}, "opt": int32(1)},
		bson.M{"_id": "a2", "f": bson.A{int32(1), int32(2)}},
		bson.M{"_id": "a0", "f": bson.A{}},
		bson.M{"_id": "s3", "f": "abc"},
		bson.M{"_id": "o3", "f": bson.M{"a": int32(1), "b": int32(2), "c": int32(3)}},
		bson.M{"_id": "o1", "f": bson.M{"a": int32(1)}},
		bson.M{"_id": "n3", "f": int32(3)},
		bson.M{"_id": "n5", "f": int32(5)},
		bson.M{"_id": "nul", "f": nil},
		bson.M{"_id": "miss"},
	})

	filters := []string{
		`.[] | select(has("opt")) | ._id`,
		`.[] | select(has("f")) | ._id`,
		".[] | select(.f | length == 3) | ._id",
		".[] | select(.f | length == 0) | ._id",
		`.[] | select((.f | length == 1) or has("opt")) | ._id`,
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed,
				"pushed $exists/$size must select the same documents as jq")
		})
	}
}

func TestCompileNegationEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "negation_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "eq", "f": "x", "opt": int32(1)},
		bson.M{"_id": "other", "f": "y"},
		bson.M{"_id": "arr", "f": bson.A{"x"}}, // array containing the value
		bson.M{"_id": "nul", "f": nil},
		bson.M{"_id": "obj", "f": bson.M{"a": int32(1)}},
		bson.M{"_id": "miss"},
	})

	filters := []string{
		`.[] | select(.f != "x") | ._id`,
		".[] | select(.f != null) | ._id",
		`.[] | select(has("opt") | not) | ._id`,
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed, "pushed negation must equal client-side")
		})
	}
}

func TestCompileNoneMatchEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "nonematch_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "hit", "items": bson.A{bson.M{"p": int32(6)}, bson.M{"p": int32(2)}}},
		bson.M{"_id": "nohit", "items": bson.A{bson.M{"p": int32(2)}, bson.M{"p": int32(3)}}},
		bson.M{"_id": "elem_arr", "items": bson.A{bson.M{"p": bson.A{int32(6)}}}}, // element p is an array
		bson.M{"_id": "obj_hit", "items": bson.M{"x": bson.M{"p": int32(6)}}},
		bson.M{"_id": "obj_nohit", "items": bson.M{"x": bson.M{"p": int32(2)}}},
		bson.M{"_id": "empty", "items": bson.A{}},
	})

	filters := []string{
		".[] | select(.items | any(.p == 6) | not) | ._id",
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed,
				"pushed $not/$elemMatch must equal client-side, including the array-element and object edges")
		})
	}
}

func TestCompileElemMatchEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "elemmatch_docs")
	// items is an array of objects for most docs, but an object for one — jq's any
	// iterates an object's values too, so the pushed filter must still match it.
	seedDocs(t, store, []any{
		bson.M{"_id": "arr_hit", "items": bson.A{bson.M{"p": int32(6), "q": int32(1)}, bson.M{"p": int32(2)}}},
		bson.M{"_id": "arr_miss", "items": bson.A{bson.M{"p": int32(2)}, bson.M{"p": int32(3)}}},
		bson.M{"_id": "arr_split", "items": bson.A{bson.M{"p": int32(6)}, bson.M{"q": int32(1)}}},
		bson.M{"_id": "obj_hit", "items": bson.M{"x": bson.M{"p": int32(6), "q": int32(1)}}},
		bson.M{"_id": "obj_miss", "items": bson.M{"x": bson.M{"p": int32(2)}}},
		bson.M{"_id": "empty", "items": bson.A{}},
	})

	filters := []string{
		".[] | select(.items | any(.p == 6)) | ._id",
		".[] | select(.items | any(.p > 5)) | ._id",
		".[] | select(.items | any(.p > 5 and .q == 1)) | ._id", // same element must satisfy both
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed,
				"pushed $elemMatch must select the same documents as jq's any")
		})
	}
}

func TestCompileRegexEquivalentToClientSide(t *testing.T) {
	store := openIntegration(t, "regex_docs")
	// test() requires string input, so every doc's name is a string; the pushed
	// $regex must select exactly the same names as jq's own engine.
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "name": "Alpha"},
		bson.M{"_id": "2", "name": "alpha"},
		bson.M{"_id": "3", "name": "Beta"},
		bson.M{"_id": "4", "name": "A1"},
		bson.M{"_id": "5", "name": "xA"},
		bson.M{"_id": "6", "name": "a\nc"}, // newline: only . under dotall matches it
		bson.M{"_id": "7", "name": "x\vy"}, // vertical tab: in PCRE's \s, not RE2's
		bson.M{"_id": "8", "name": "x y"},  // space: \s in every engine
	})

	filters := []string{
		`.[] | select(.name | test("^A")) | ._id`,
		`.[] | select(.name | test("^a"; "i")) | ._id`,
		`.[] | select(.name | test("a")) | ._id`,
		`.[] | select(.name | test("\\d")) | ._id`,
		`.[] | select(.name | test("[AB]")) | ._id`,
		`.[] | select((.name | test("^A")) or .name == "xA") | ._id`,
		// jq's m flag is dotall, so "a.c" matches "a\nc"; the pushed $regex must use
		// PCRE's s option, not m, or it drops _id 6 and diverges (the narrowing bug).
		`.[] | select(.name | test("a.c"; "m")) | ._id`,
		// \s is a superset in PCRE (matches the vertical tab in _id 7 that RE2 does
		// not); the pushed pre-filter over-matches, and the client re-run corrects it
		// to jq's result — _id 8 only.
		`.[] | select(.name | test("x\\sy")) | ._id`,
	}
	eng := query.NewJQEngine(store)
	for _, f := range filters {
		t.Run(f, func(t *testing.T) {
			plain := runIDs(t, eng, f, query.RunOptions{})
			pushed := runIDs(t, eng, f, query.RunOptions{Compile: true})
			require.ElementsMatch(t, plain, pushed,
				"pushed $regex must select the same documents as jq's engine")
		})
	}
}

// runIDs runs a filter through the engine and returns the values it emits.
func runIDs(t *testing.T, eng *query.JQEngine, src string, opts query.RunOptions) []any {
	t.Helper()
	var got []any
	err := eng.Run(context.Background(), src, opts, func(v any) error {
		got = append(got, v)
		return nil
	})
	require.NoError(t, err)
	return got
}

func TestRawRunCommand(t *testing.T) {
	store := openIntegration(t, "raw_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "year": int32(2015)},
		bson.M{"_id": "2", "year": int32(2018)},
	})

	res, err := store.Query(context.Background(), []string{
		`{"count":"raw_docs","query":{"year":{"$gt":2016}}}`,
	})
	require.NoError(t, err)

	m := res.(map[string]any)
	require.EqualValues(t, 1, m["n"], "count command returns n=1")
	require.EqualValues(t, 1, m["ok"])
}

func TestRawRejectsMultipleArgs(t *testing.T) {
	store := openIntegration(t, "raw_docs")

	_, err := store.Query(context.Background(), []string{"{}", "{}"})

	require.ErrorContains(t, err, "one JSON command document")
}

func TestSplitCollection(t *testing.T) {
	tests := []struct {
		name      string
		uri       string
		wantClean string
		wantColl  string
	}{
		{"no query", "mongodb://h/db", "mongodb://h/db", ""},
		{"collection only", "mongodb://h/db?collection=orders", "mongodb://h/db", "orders"},
		{"collection among options", "mongodb://h/db?retryWrites=true&collection=orders", "mongodb://h/db?retryWrites=true", "orders"},
		{"multi-host", "mongodb://h1,h2/db?collection=orders", "mongodb://h1,h2/db", "orders"},
		{"empty collection value ignored", "mongodb://h/db?collection=", "mongodb://h/db?collection=", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, coll := SplitCollection(tt.uri)
			require.Equal(t, tt.wantClean, clean)
			require.Equal(t, tt.wantColl, coll)
			require.Equal(t, tt.wantColl, CollectionFromURI(tt.uri))
		})
	}
}

// TestOpenTakesCollectionFromURI proves the ?collection= default is adopted when
// the address carries no collection of its own.
func TestOpenTakesCollectionFromURI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	store, err := Open(ctx, uriWithCollection(t, "uri_default_docs"), "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.Equal(t, "uri_default_docs", store.collection, "the URI default is the keyspace")
	_, err = store.EstimateCount(ctx)
	require.NoError(t, err, "a collection-scoped call works without an address override")
}

// TestOpenAddressOverridesURICollection pins the precedence: an addressed
// collection wins over the URI default.
func TestOpenAddressOverridesURICollection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	store, err := Open(ctx, uriWithCollection(t, "uri_default_docs"), "addressed_docs", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.Equal(t, "addressed_docs", store.collection)
}

// TestOpenWithCanceledContextFails proves the ping that verifies the connection is
// bound by the caller's context rather than an unbounded one.
func TestOpenWithCanceledContextFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store, err := Open(ctx, testURI(), "canceled_open_docs", nil, numfmt.DecimalAuto)

	require.ErrorContains(t, err, "connect mongodb")
	require.ErrorIs(t, err, context.Canceled, "the ping error is wrapped, not flattened")
	require.Nil(t, store)
}

// TestOpenRejectsBadURI proves Open stops at the first bad part of the URI and
// returns that error, before it pings any server. An error from a library keeps
// its cause in the chain.
func TestOpenRejectsBadURI(t *testing.T) {
	tests := []struct {
		name      string
		uri       string
		wantErr   string
		wantCause bool
	}{
		{"no database", "mongodb://localhost:27017", "must name a database", false},
		{"unparsable uri", "mongodb://localhost:27017/%zz", "parse mongodb uri", true},
		{"bad client option", "mongodb://localhost:27017/iq_test?connectTimeoutMS=soon", "connect mongodb", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			t.Cleanup(cancel)

			store, err := Open(ctx, tt.uri, "docs", nil, numfmt.DecimalAuto)

			require.ErrorContains(t, err, tt.wantErr)
			if tt.wantCause {
				require.Error(t, errors.Unwrap(err), "the cause stays in the chain")
			}
			require.Nil(t, store)
		})
	}
}

// TestOperationsHonorCanceledContext proves every outbound call is bound by the
// caller's context: with an already-canceled one, none of them reaches the server.
func TestOperationsHonorCanceledContext(t *testing.T) {
	store := openIntegration(t, "canceled_ops_docs")
	seedDocs(t, store, []any{bson.M{"_id": "a", "n": int32(1)}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recs := []query.Record{{Key: "a", Value: map[string]any{"n": 1.0}}}

	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{"get", func(ctx context.Context) error { _, err := store.Get(ctx, []string{"a"}); return err }},
		{"scan batches", func(ctx context.Context) error {
			return store.ScanBatches(ctx, func(map[string]any) error { return nil })
		}},
		{"scan filtered", func(ctx context.Context) error {
			return store.ScanFiltered(ctx, predicate.Eq{Path: []string{"n"}, Value: 1.0}, func(map[string]any) error { return nil })
		}},
		{"typed scan", func(ctx context.Context) error {
			return store.TypedScan(ctx, func([]query.Record) error { return nil })
		}},
		{"estimate count", func(ctx context.Context) error { _, err := store.EstimateCount(ctx); return err }},
		{"query", func(ctx context.Context) error {
			_, err := store.Query(ctx, []string{`{"count":"canceled_ops_docs"}`})
			return err
		}},
		{"put upsert", func(ctx context.Context) error { _, err := store.Put(ctx, recs, query.Upsert); return err }},
		{"put insert only", func(ctx context.Context) error {
			_, err := store.Put(ctx, recs, query.InsertOnly)
			return err
		}},
		{"delete", func(ctx context.Context) error { _, err := store.Delete(ctx, []string{"a"}); return err }},
		{"clear", store.Clear},
		{"drop", store.Drop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(ctx)
			require.Error(t, err, "a canceled context must not be replaced by an unbounded one")
			require.ErrorIs(t, err, context.Canceled)
		})
	}

	// The canceled calls were refused before they reached the server, so the seeded
	// document is untouched.
	got, err := store.Get(context.Background(), []string{"a"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_id": "a", "n": 1}, got["a"])
}

// TestScanBatchesPropagatesCallbackError proves a callback error stops the scan and
// surfaces unchanged, rather than being swallowed into a successful scan.
func TestScanBatchesPropagatesCallbackError(t *testing.T) {
	store := openIntegration(t, "callback_error_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "n": int32(1)},
		bson.M{"_id": "2", "n": int32(2)},
		bson.M{"_id": "3", "n": int32(3)},
		bson.M{"_id": "4", "n": int32(4)},
	})
	store.pageSize = 2

	boom := errors.New("boom")
	pages := 0
	err := store.ScanBatches(context.Background(), func(map[string]any) error {
		pages++
		return boom
	})

	require.ErrorIs(t, err, boom, "the callback's error is returned as-is")
	require.Equal(t, 1, pages, "the scan stops at the failing page")
}

// TestScanBatchesStopsWhenContextCancelsMidScan proves the cursor's own paging is
// bound by the caller's context, not just the initial find: cancelling between
// pages fails the scan instead of draining the collection.
func TestScanBatchesStopsWhenContextCancelsMidScan(t *testing.T) {
	store := openIntegration(t, "midscan_cancel_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "n": int32(1)},
		bson.M{"_id": "2", "n": int32(2)},
		bson.M{"_id": "3", "n": int32(3)},
	})
	// One document per batch, so every page after the first needs a fresh round trip
	// that the canceled context must refuse.
	store.pageSize = 1

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pages := 0
	err := store.ScanBatches(ctx, func(map[string]any) error {
		pages++
		cancel()
		return nil
	})

	require.ErrorIs(t, err, context.Canceled, "a mid-scan cancellation fails the scan")
	require.Equal(t, 1, pages, "no page is handed over after the cancellation")
}

// TestCloseDisconnects proves Close actually tears the client down: a later call
// on the closed store fails rather than silently working.
func TestCloseDisconnects(t *testing.T) {
	store := openIntegration(t, "close_docs")

	require.NoError(t, store.Close())

	_, err := store.EstimateCount(context.Background())
	require.ErrorContains(t, err, "client is disconnected")
}

// TestRawAcceptsRelaxedExtendedJSON proves the command document is parsed in
// relaxed Extended JSON, the form a user types: a canonical-only parse would
// reject the relaxed $date spelling outright.
func TestRawAcceptsRelaxedExtendedJSON(t *testing.T) {
	store := openIntegration(t, "raw_relaxed_docs")
	seedDocs(t, store, []any{
		bson.M{"_id": "1", "at": bson.NewDateTimeFromTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))},
	})

	res, err := store.Query(context.Background(), []string{
		`{"count":"raw_relaxed_docs","query":{"at":{"$date":"2024-01-01T00:00:00Z"}}}`,
	})

	require.NoError(t, err)
	require.EqualValues(t, 1, res.(map[string]any)["n"], "the relaxed $date matched the stored date")
}

func TestRawRejectsInvalidJSON(t *testing.T) {
	store := openIntegration(t, "raw_docs")

	_, err := store.Query(context.Background(), []string{"{not json"})

	require.ErrorContains(t, err, "parse mongodb command")
	require.Error(t, errors.Unwrap(err), "the parser's own error stays wrapped, not flattened into text")
}

func TestRawRejectsUnknownCommand(t *testing.T) {
	store := openIntegration(t, "raw_docs")

	res, err := store.Query(context.Background(), []string{`{"noSuchCommand":1}`})

	require.ErrorContains(t, err, "mongodb command")
	require.Nil(t, res)
}

// cancelOnCommand cancels a context the first time the named command is traced,
// giving a test a deterministic cancellation point between two round trips of one
// call. It is an io.Writer because that is how the driver's command trace is wired.
type cancelOnCommand struct {
	name   string
	cancel context.CancelFunc

	mu   sync.Mutex
	seen bool
}

func (c *cancelOnCommand) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seen && bytes.Contains(p, []byte(c.name)) {
		c.seen = true
		c.cancel()
	}
	return len(p), nil
}

// TestGetStopsWhenContextCancelsMidFetch proves the by-_id fetch stays bound by the
// caller's context across every round trip: a find's first batch holds 101
// documents, so a larger result needs a getMore, and cancelling at that point must
// fail the fetch instead of draining the cursor under an unbounded context.
func TestGetStopsWhenContextCancelsMidFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store := openIntegrationTraced(t, "midfetch_cancel_docs", &cancelOnCommand{name: "getMore", cancel: cancel})

	const docs = 250
	seed := make([]any, docs)
	keys := make([]string, docs)
	for i := range seed {
		keys[i] = fmt.Sprintf("k%03d", i)
		seed[i] = bson.M{"_id": keys[i], "n": int32(i)}
	}
	seedDocs(t, store, seed)

	got, err := store.Get(ctx, keys)

	require.ErrorIs(t, err, context.Canceled, "the cursor error keeps the cancellation wrapped")
	require.ErrorContains(t, err, "mongodb cursor", "and is anchored at this driver")
	require.Nil(t, got, "no partial result is handed back as a success")
}

// TestEstimateCountFailureReturnsZero pins the count returned alongside an error:
// a failed estimate is no estimate, so the caller never reads a fabricated total.
func TestEstimateCountFailureReturnsZero(t *testing.T) {
	store := openIntegration(t, "estimate_error_docs")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	n, err := store.EstimateCount(ctx)

	require.Error(t, err)
	require.Zero(t, n, "a failed estimate returns no count")
}

// writerFunc adapts a function to io.Writer, so a test can observe how the command
// monitor calls into its trace writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestCommandMonitorSerializesWrites proves the monitor holds its lock for the whole
// write: two concurrent commands must never be inside the trace writer at once, or
// their lines would tear into each other.
func TestCommandMonitorSerializesWrites(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"find": "books"})
	require.NoError(t, err)

	var inside, overlaps atomic.Int32
	monitor := newCommandMonitor(writerFunc(func(p []byte) (int, error) {
		if inside.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(50 * time.Millisecond)
		inside.Add(-1)
		return len(p), nil
	}))

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			monitor.Started(context.Background(), &event.CommandStartedEvent{
				CommandName: "find",
				Command:     bson.Raw(raw),
			})
		})
	}
	close(start)
	wg.Wait()

	require.Zero(t, overlaps.Load(), "the writer is never entered concurrently")
}

// TestCommandMonitorSkipsHandshakeCommands proves the trace omits each handshake,
// auth and session command, and still writes a query command.
func TestCommandMonitorSkipsHandshakeCommands(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"x": 1})
	require.NoError(t, err)

	tests := []struct {
		command string
		traced  bool
	}{
		{"hello", false},
		{"ismaster", false},
		{"isMaster", false},
		{"saslStart", false},
		{"saslContinue", false},
		{"authenticate", false},
		{"getnonce", false},
		{"ping", false},
		{"endSessions", false},
		{"find", true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			var buf bytes.Buffer
			monitor := newCommandMonitor(&buf)

			monitor.Started(context.Background(), &event.CommandStartedEvent{
				CommandName: tt.command,
				Command:     bson.Raw(raw),
			})

			if tt.traced {
				require.Equal(t, "mongo> "+tt.command+" {\"x\": {\"$numberInt\":\"1\"}}\n", buf.String())
				return
			}
			require.Empty(t, buf.String())
		})
	}
}
