package dynamodb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsert(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	// Insert a new key.
	stat, err := st.Put(ctx, []query.Record{{
		Key:   "4",
		Type:  "item",
		Value: map[string]any{"title": "Clean Code", "year": 2008, "price": 35},
	}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)

	got, err := st.Get(ctx, []string{"4"})
	require.NoError(t, err)
	require.Equal(t, "Clean Code", got["4"].(map[string]any)["title"])
	require.Equal(t, 4, got["4"].(map[string]any)["id"]) // key attribute reconstructed from the record key

	// Overwrite the same key.
	_, err = st.Put(ctx, []query.Record{{
		Key:   "4",
		Value: map[string]any{"title": "Clean Code (2nd)", "year": 2008},
	}}, query.Upsert)
	require.NoError(t, err)
	got, err = st.Get(ctx, []string{"4"})
	require.NoError(t, err)
	require.Equal(t, "Clean Code (2nd)", got["4"].(map[string]any)["title"])
}

func TestPutInsertOnly(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	// Key 1 already exists: InsertOnly must skip it, not overwrite.
	stat, err := st.Put(ctx, []query.Record{{
		Key:   "1",
		Value: map[string]any{"title": "SHOULD NOT WIN", "year": 1},
	}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 0, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "The Go Programming Language", got["1"].(map[string]any)["title"])

	// A new key is written.
	stat, err = st.Put(ctx, []query.Record{{
		Key:   "5",
		Value: map[string]any{"title": "New", "year": 2020},
	}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 0, stat.Skipped)
}

func TestClear(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	require.NoError(t, st.Clear(ctx))

	remaining := 0
	err := st.ScanBatches(ctx, func(batch map[string]any) error {
		remaining += len(batch)
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, remaining)
}

func TestDrop(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	require.NoError(t, st.Drop(ctx))

	// The table is gone, so a scan now errors.
	err := st.ScanBatches(ctx, func(map[string]any) error { return nil })
	require.Error(t, err)
}

func TestTypedScan(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	var recs []query.Record
	err := st.TypedScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, recs, 3)
	for _, r := range recs {
		require.Equal(t, "item", r.Type)
		require.NotEmpty(t, r.Key)
	}
}
