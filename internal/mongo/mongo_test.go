package mongo

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/predicate"
)

// testURI returns the MongoDB URI for integration tests, defaulting to a local
// server and the iq_test database (kept off any real data).
func testURI() string {
	if uri := os.Getenv("IQ_MONGO_URL"); uri != "" {
		return uri
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

	store, err := Open(ctx, testURI(), collection)
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
