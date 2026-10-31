package couchdb

import (
	"context"
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
		{
			name:   "a plain object is copied field for field",
			record: query.Record{Key: "1", Value: map[string]any{"title": "Go", "year": 2015}},
			want:   map[string]any{"title": "Go", "year": 2015},
		},
		{
			name:   "an embedded _id is stripped so identity comes from the key",
			record: query.Record{Key: "1", Value: map[string]any{"_id": "foreign", "title": "Go"}},
			want:   map[string]any{"title": "Go"},
		},
		{
			name:   "an embedded _rev is stripped so the upsert sets it",
			record: query.Record{Key: "1", Value: map[string]any{"_rev": "9-bad", "title": "Go"}},
			want:   map[string]any{"title": "Go"},
		},
		{
			name:   "an empty-string field name is data, not identity",
			record: query.Record{Key: "1", Value: map[string]any{"": "blank", "title": "Go"}},
			want:   map[string]any{"": "blank", "title": "Go"},
		},
		{
			name:   "an object of only identity fields yields an empty document",
			record: query.Record{Key: "1", Value: map[string]any{"_id": "foreign", "_rev": "9-bad"}},
			want:   map[string]any{},
		},
		{
			name:    "a scalar value is rejected rather than wrapped",
			record:  query.Record{Key: "k", Value: "bare-scalar"},
			wantErr: "is not a JSON object",
		},
		{
			name:    "a key that is not valid UTF-8 is rejected",
			record:  query.Record{Key: "a\xff", Value: map[string]any{"t": "x"}},
			wantErr: `couchdb: record key "a\xff" is not valid UTF-8`,
		},
		{
			name:    "a string value that is not valid UTF-8 is rejected",
			record:  query.Record{Key: "1", Value: map[string]any{"t": map[string]any{"u": "x\xfe"}}},
			wantErr: `couchdb: key "1": field "t.u" holds a string that is not valid UTF-8`,
		},
		{
			name:    "a field name that is not valid UTF-8 is rejected",
			record:  query.Record{Key: "1", Value: map[string]any{"t\xfe": "x"}},
			wantErr: `couchdb: key "1": the field name "t\xfe" is not valid UTF-8`,
		},
		{
			name:   "an invalid _id value is dropped with the _id",
			record: query.Record{Key: "1", Value: map[string]any{"_id": "\xff", "title": "Go"}},
			want:   map[string]any{"title": "Go"},
		},
		{
			name:    "a _deleted true member is refused so the write cannot delete",
			record:  query.Record{Key: "k", Value: map[string]any{"_deleted": true, "t": "x"}},
			wantErr: `couchdb: key "k": the value holds _deleted, so CouchDB would delete the document or drop the member; use iq data delete to delete it`,
		},
		{
			name:    "a _deleted false member is refused because CouchDB would drop it",
			record:  query.Record{Key: "k", Value: map[string]any{"_deleted": false}},
			wantErr: `couchdb: key "k": the value holds _deleted`,
		},
		{
			name:    "a _deleted member of any other type is refused too",
			record:  query.Record{Key: "k", Value: map[string]any{"_deleted": "yes"}},
			wantErr: `couchdb: key "k": the value holds _deleted`,
		},
		{
			name:    "an array value is rejected too",
			record:  query.Record{Key: "k", Value: []any{1, 2}},
			wantErr: "is not a JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := documentBody(tt.record)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDocumentBodySkipsIdentityWithoutEndingTheWalk(t *testing.T) {
	// The identity fields are skipped, not a stop condition: every data field
	// survives whatever order Go's randomized map iteration visits the object in.
	// A run repeats the copy so an ordering that happens to place both identity
	// fields last cannot make a walk-ending mutation look correct.
	const runs = 32
	want := map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}
	for range runs {
		got, err := documentBody(query.Record{Key: "1", Value: map[string]any{
			"_id": "foreign", "_rev": "9-bad", "a": 1, "b": 2, "c": 3, "d": 4, "e": 5,
		}})
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestCurrentRevsForKeysEmptyShortCircuits(t *testing.T) {
	// No keys means no _all_docs round trip at all, so the store needs no client.
	// The short-circuit still hands back an empty map, never a nil one.
	st := &Store{}
	revs, err := st.currentRevsForKeys(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, revs)
	require.Empty(t, revs)
}
