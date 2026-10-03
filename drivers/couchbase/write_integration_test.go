package couchbase

import (
	"bytes"
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/couchbase/gocb/v2"
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

	// Read the document back: the count alone cannot tell an inserted document from an
	// inserted null.
	out, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"n": 1}, out["1"])

	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"n": 99}},
		{Key: "2", Value: map[string]any{"n": 2}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Skipped: 1}, stat)
}

// TestPutEncodeError pins that a document the SDK cannot encode fails the batch in both
// write modes. The SDK reports the failure on the single operation, and the batch call
// itself succeeds, so a Put that reads only the batch result counts the document as
// written.
func TestPutEncodeError(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)

	tests := []struct {
		name string
		mode query.WriteMode
		want string
	}{
		{name: "upsert", mode: query.Upsert, want: "couchbase upsert"},
		{name: "insert only", mode: query.InsertOnly, want: "couchbase insert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := map[string]any{"c": make(chan int)} // JSON cannot encode a channel
			_, err := st.Put(ctx, []query.Record{{Key: "bad", Value: bad}}, tt.mode)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestPutKeylessMintsID(t *testing.T) {
	st := seedCollection(t, nil)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{{Value: map[string]any{"n": 1}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got := map[string]any{}
	require.NoError(t, st.ScanBatches(ctx, func(batch map[string]any) error {
		maps.Copy(got, batch)
		return nil
	}))
	require.Len(t, got, 1)
	for k := range got {
		require.NotEmpty(t, k) // a UUID was minted
	}
}

// TestPutEmptyBatch pins the short-circuit: nothing to write means no round trip. The
// bulk write traces its document count, so an empty trace is the proof.
func TestPutEmptyBatch(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)
	var buf bytes.Buffer
	st.trace = &buf

	stat, err := st.Put(ctx, nil, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{}, stat)
	require.Empty(t, buf.String(), "an empty batch must not reach the cluster")
}

// TestPutKeyValidation pins that Put validates the key it was given before any write. The
// read path has the same guard, and only that one was covered. An empty key never reaches
// the guard, because a record without a key gets a minted UUID, so the case is a key over
// the length limit.
func TestPutKeyValidation(t *testing.T) {
	st := seedCollectionKV(t, nil)
	ctx := skipShort(t)

	long := strings.Repeat("k", maxKeyBytes+1)
	_, err := st.Put(ctx, []query.Record{{Key: long, Value: map[string]any{"n": 1}}}, query.Upsert)
	require.ErrorContains(t, err, "couchbase document key exceeds")

	_, err = st.Put(ctx, []query.Record{{Key: long, Value: map[string]any{"n": 1}}}, query.InsertOnly)
	require.ErrorContains(t, err, "couchbase document key exceeds")
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

// TestWritePathContextCancelled pins that the context each write op is handed reaches the
// call it makes: a context cancelled before the op starts must surface the cancellation
// instead of a completed write. The collection exists, so cancellation is the only reason
// these ops can fail. It mirrors the read-path checks over ScanBatches and ScanFiltered.
// Drop runs last, because it removes the collection the other cases need.
func TestWritePathContextCancelled(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel before the first op issues its request

	tests := []struct {
		name string
		op   func() error
	}{
		{name: "clear", op: func() error { return st.Clear(cctx) }},
		{name: "typed scan", op: func() error { return st.TypedScan(cctx, func([]query.Record) error { return nil }) }},
		{name: "drop", op: func() error { return st.Drop(cctx) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.op(), gocb.ErrRequestCanceled,
				"a cancelled context must surface as an error, not a completed op")
		})
	}
}

// TestDropNonDefaultScope pins the reach of the default-collection guard: it protects the
// default collection of the default scope only. A collection named _default in another
// scope is an ordinary collection, so Drop must try it and report what the server says.
func TestDropNonDefaultScope(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), "iq_no_such_scope._default", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Drop(ctx)
	require.ErrorContains(t, err, "couchbase drop")
	require.NotContains(t, err.Error(), "default collection cannot be dropped",
		"the guard must test the scope as well as the collection")
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
