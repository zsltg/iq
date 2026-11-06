package hbase

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// nativeRows reads every row of st in the raw form, sorted by key.
func nativeRows(ctx context.Context, t *testing.T, st *Store) []query.Record {
	t.Helper()
	var recs []query.Record
	require.NoError(t, st.NativeScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	}))
	sort.Slice(recs, func(i, j int) bool { return recs[i].Key < recs[j].Key })
	return recs
}

// TestIntegrationNativeCopyRoundTrip seeds a table with bytes that are not text,
// copies it into a second table in the raw form, and compares the two tables.
func TestIntegrationNativeCopyRoundTrip(t *testing.T) {
	url := integrationOrSkip(t)
	createTable(t, url, "iq_it_raw_src", "cf", "other")
	createTable(t, url, "iq_it_raw_dst", "cf", "other")
	src := openIntegration(t, url, "iq_it_raw_src", "?keytype=long&types=cf:name=long")
	dst := openIntegration(t, url, "iq_it_raw_dst", "")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seed := []query.Record{
		{Key: b64("\xff\x00key"), Form: query.FormHBaseRaw, Value: map[string]any{
			"cf":    map[string]any{b64("\xfe\x01"): b64("\x80\x81"), b64("name"): b64("Dune"), b64("empty"): ""},
			"other": map[string]any{b64("q"): b64("\x00")},
		}},
		{Key: b64("plain"), Form: query.FormHBaseRaw, Value: map[string]any{
			"cf": map[string]any{b64("name"): b64("Hyperion")},
		}},
	}
	stat, err := src.Put(ctx, seed, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)

	require.NoError(t, src.NativeScan(ctx, func(batch []query.Record) error {
		_, err := dst.Put(ctx, batch, query.Upsert)
		return err
	}))

	want := nativeRows(ctx, t, src)
	got := nativeRows(ctx, t, dst)
	require.Len(t, want, 2)
	require.Equal(t, want, got)
	for _, r := range got {
		require.Equal(t, query.FormHBaseRaw, r.Form)
	}
}
