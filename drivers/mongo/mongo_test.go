package mongo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

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
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	store, err := Open(ctx, testURI(), collection, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
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
	})

	got, err := store.Get(context.Background(), []string{"b1", oid.Hex(), "missing"})
	require.NoError(t, err)

	require.Equal(t, map[string]any{
		"_id": "b1", "title": "Go", "year": 2015, "tags": []any{"go"},
	}, got["b1"])
	require.Equal(t, "Mongo", got[oid.Hex()].(map[string]any)["title"], "ObjectID key matched by hex")
	require.Nil(t, got["missing"], "absent _id reads as null")
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
	store := openIntegration(t, "empty_docs")

	got, err := store.Get(context.Background(), nil)

	require.NoError(t, err)
	require.Equal(t, map[string]any{}, got)
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
		for k, v := range batch {
			merged[k] = v
		}
		return nil
	})
	require.NoError(t, err)

	require.GreaterOrEqual(t, batches, 1)
	require.Len(t, merged, 3)
	require.Equal(t, map[string]any{"_id": "2", "n": 2}, merged["2"])
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
			for k, v := range batch {
				merged[k] = v
			}
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
	})

	filters := []string{
		`.[] | select(.name | test("^A")) | ._id`,
		`.[] | select(.name | test("^a"; "i")) | ._id`,
		`.[] | select(.name | test("a")) | ._id`,
		`.[] | select(.name | test("\\d")) | ._id`,
		`.[] | select(.name | test("[AB]")) | ._id`,
		`.[] | select((.name | test("^A")) or .name == "xA") | ._id`,
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
