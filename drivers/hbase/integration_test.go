package hbase

import (
	"bytes"
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// row is a nested {family: {qualifier: value}} row value for a write record.
func row(cells map[string]map[string]any) any {
	out := map[string]any{}
	for family, quals := range cells {
		fam := map[string]any{}
		maps.Copy(fam, quals)
		out[family] = fam
	}
	return out
}

func TestIntegrationPutGetScan(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_books"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	batch := []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"title": "Dune", "author": "Herbert"}})},
		{Key: "2", Value: row(map[string]map[string]any{"cf": {"title": "Hyperion", "author": "Simmons"}})},
	}
	stat, err := st.Put(ctx, batch, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)

	got, err := st.Get(ctx, []string{"1", "missing"})
	require.NoError(t, err)
	require.Equal(t, "Dune", got["1"].(map[string]any)["cf"].(map[string]any)["title"])
	require.NotContains(t, got, "missing", "a key with no row is absent from the map")

	seen := map[string]any{}
	require.NoError(t, st.ScanBatches(ctx, func(b map[string]any) error {
		maps.Copy(seen, b)
		return nil
	}))
	require.Len(t, seen, 2)

	// Re-putting key 1 alongside a new key 3: the existence pre-read counts one
	// overwrite and one fresh write against a live region server.
	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"title": "Dune (rev)"}})},
		{Key: "3", Value: row(map[string]map[string]any{"cf": {"title": "Ringworld"}})},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
}

func TestIntegrationScanFiltered(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_filter"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"author": "Herbert"}})},
		{Key: "2", Value: row(map[string]map[string]any{"cf": {"author": "Simmons"}})},
	}, query.Upsert)
	require.NoError(t, err)

	seen := map[string]any{}
	require.NoError(t, st.ScanFiltered(ctx,
		predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
		func(b map[string]any) error {
			maps.Copy(seen, b)
			return nil
		}))
	// The server-side SingleColumnValueFilter returns only the matching row.
	require.Len(t, seen, 1)
	require.Contains(t, seen, "1")
}

func TestIntegrationQueryVerbs(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_verbs"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.Query(ctx, []string{"put", table, "42", "cf:title", "Sabriel"})
	require.NoError(t, err)

	got, err := st.Query(ctx, []string{"get", table, "42"})
	require.NoError(t, err)
	require.Equal(t, "Sabriel", got.(map[string]any)["cf"].(map[string]any)["title"])

	count, err := st.Query(ctx, []string{"count", table})
	require.NoError(t, err)
	require.Equal(t, 1, count)

	_, err = st.Query(ctx, []string{"delete", table, "42"})
	require.NoError(t, err)
	after, err := st.Query(ctx, []string{"count", table})
	require.NoError(t, err)
	require.Equal(t, 0, after)
}

func TestIntegrationClearAndDrop(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_clear"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"n": "a"}})},
		{Key: "2", Value: row(map[string]map[string]any{"cf": {"n": "b"}})},
	}, query.Upsert)
	require.NoError(t, err)

	require.NoError(t, st.Clear(ctx))
	seen := 0
	require.NoError(t, st.ScanBatches(ctx, func(b map[string]any) error { seen += len(b); return nil }))
	require.Equal(t, 0, seen)

	require.NoError(t, st.Drop(ctx))
	// After a drop, opening the table again fails its existence check.
	octx, ocancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer ocancel()
	_, err = Open(octx, url, table, nil, 0)
	require.Error(t, err)
}

func TestDeleteIntegration(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_delete"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"title": "Dune"}})},
		{Key: "2", Value: row(map[string]map[string]any{"cf": {"title": "Hyperion"}})},
		{Key: "3", Value: row(map[string]map[string]any{"cf": {"title": "Ringworld"}})},
	}, query.Upsert)
	require.NoError(t, err)

	// Delete a mix: 1 and 2 exist, "absent" never did. Del is silent on absence, so
	// the pre-read supplies the split; 3 is left untouched.
	stat, err := st.Delete(ctx, []string{"1", "absent", "2"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)

	got, err := st.Get(ctx, []string{"1", "2", "3"})
	require.NoError(t, err)
	require.Nil(t, got["1"])
	require.Nil(t, got["2"])
	require.Equal(t, "Ringworld", got["3"].(map[string]any)["cf"].(map[string]any)["title"])
}

func TestIntegrationDeclaredLongRoundTrip(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_typed"
	createTable(t, url, table, "cf")
	st := openIntegration(t, url, table, "?types=cf:pages=long")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: row(map[string]map[string]any{"cf": {"pages": 412}})},
	}, query.Upsert)
	require.NoError(t, err)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, 412, got["1"].(map[string]any)["cf"].(map[string]any)["pages"])
}

func TestIntegrationOpenConfiguresTheStoreFromTheURL(t *testing.T) {
	url := integrationOrSkip(t)
	const table = "iq_it_open"
	createTable(t, url, table, "cf")

	var trace bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Open(ctx, url+"?types=cf:pages=long&keytype=long", table, &trace, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Everything the URL decides has to reach the Store: the table it is scoped to,
	// the declared column and row-key encodings, the streaming page size, and the
	// trace writer the CLI's --verbose hands in.
	require.Equal(t, table, st.table)
	require.Equal(t, typeMap{cellKey("cf", "pages"): ctLong}, st.types)
	require.Equal(t, ctLong, st.rowkeyType)
	require.Equal(t, scanBatch, st.pageSize)

	_, err = st.Get(ctx, []string{"100"})
	require.NoError(t, err)
	require.Equal(t, "hbase> get "+table+" rows=1\n", trace.String())
}
