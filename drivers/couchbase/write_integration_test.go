package couchbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsert(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)

	batch := []query.Record{
		{Key: "1", Value: map[string]any{"n": 1}},
		{Key: "2", Value: map[string]any{"n": 2}},
	}
	stat, err := st.Put(ctx, batch, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2}, stat)

	// Re-upserting the same keys (plus a new one) counts overwrites honestly.
	batch2 := []query.Record{
		{Key: "1", Value: map[string]any{"n": 10}},
		{Key: "3", Value: map[string]any{"n": 3}},
	}
	stat, err = st.Put(ctx, batch2, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)

	// Read-your-writes: RequestPlus consistency sees the updated value.
	out, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"n": 10}, out["1"])
}

func TestPutInsertOnly(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"n": 1}}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"n": 99}},
		{Key: "2", Value: map[string]any{"n": 2}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Skipped: 1}, stat)
}

func TestPutKeylessMintsID(t *testing.T) {
	st := seedCollection(t, nil)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{{Value: map[string]any{"n": 1}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got := map[string]any{}
	require.NoError(t, st.ScanBatches(ctx, func(batch map[string]any) error {
		for k, v := range batch {
			got[k] = v
		}
		return nil
	}))
	require.Len(t, got, 1)
	for k := range got {
		require.NotEmpty(t, k) // a UUID was minted
	}
}

func TestPutNonObjectRejected(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)

	_, err := st.Put(ctx, []query.Record{{Key: "x", Value: "scalar"}}, query.Upsert)
	require.ErrorContains(t, err, "not a JSON object")

	_, err = st.Put(ctx, []query.Record{{Key: "y", Value: []any{1, 2}}}, query.InsertOnly)
	require.ErrorContains(t, err, "not a JSON object")
}

func TestClear(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)

	require.NoError(t, st.Clear(ctx))

	got := 0
	require.NoError(t, st.ScanBatches(ctx, func(batch map[string]any) error {
		got += len(batch)
		return nil
	}))
	require.Zero(t, got)
}

func TestDrop(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	require.NoError(t, st.Drop(ctx))
}

func TestDropDefaultRejected(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), "", nil, numfmt.DecimalAuto) // _default._default
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Drop(ctx)
	require.ErrorContains(t, err, "default collection cannot be dropped")
}

func TestTypedScan(t *testing.T) {
	fixture := books()
	st := seedCollection(t, fixture)
	ctx := skipShort(t)

	got := map[string]any{}
	require.NoError(t, st.TypedScan(ctx, func(batch []query.Record) error {
		for _, r := range batch {
			require.Equal(t, "document", r.Type)
			got[r.Key] = r.Value
		}
		return nil
	}))
	require.Len(t, got, len(fixture))
	require.Equal(t, fixture["1"], got["1"])
}
