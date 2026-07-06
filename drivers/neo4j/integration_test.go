package neo4j

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// integrationOrSkip skips a test under -short (no container) and returns a bounded
// context for the round-trips.
func integrationOrSkip(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping neo4j integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// openStore opens a label/key-scoped store for a test, closing it on cleanup.
func openStore(t *testing.T, ctx context.Context, label, key string) *Store {
	t.Helper()
	u := testURL() + "?label=" + label
	if key != "" {
		u += "&key=" + key
	}
	st, err := Open(ctx, u, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// exec runs a setup/teardown Cypher statement through the raw path.
func exec(t *testing.T, ctx context.Context, st *Store, cypher string) {
	t.Helper()
	_, err := st.Query(ctx, []string{cypher})
	require.NoError(t, err)
}

func TestIntegrationGetScanCountFilter(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "CREATE CONSTRAINT itBook_id IF NOT EXISTS FOR (b:ItBook) REQUIRE b.id IS UNIQUE")
	exec(t, ctx, admin, "MATCH (b:ItBook) DETACH DELETE b")
	exec(t, ctx, admin, `CREATE (:ItBook {id:'1', title:'Go', year:2015}),
		(:ItBook {id:'2', title:'DDIA', year:2017}),
		(:ItBook {id:'3', title:'APoSD', year:2018})`)

	st := openStore(t, ctx, "ItBook", "id")

	t.Run("get by key", func(t *testing.T) {
		got, err := st.Get(ctx, []string{"1", "3", "missing"})
		require.NoError(t, err)
		require.NotNil(t, got["1"])
		assert.Equal(t, "Go", got["1"].(map[string]any)["title"])
		assert.Equal(t, 2018, got["3"].(map[string]any)["year"])
		assert.Nil(t, got["missing"])
	})

	t.Run("scan all", func(t *testing.T) {
		seen := map[string]any{}
		require.NoError(t, st.ScanBatches(ctx, func(p map[string]any) error {
			for k, v := range p {
				seen[k] = v
			}
			return nil
		}))
		assert.Len(t, seen, 3)
		assert.Contains(t, seen, "2")
	})

	t.Run("estimate count", func(t *testing.T) {
		c, err := st.EstimateCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(3), c)
	})

	t.Run("filtered scan pushes equality", func(t *testing.T) {
		seen := map[string]any{}
		require.NoError(t, st.ScanFiltered(ctx, predicate.Eq{Path: []string{"title"}, Value: "DDIA"}, func(p map[string]any) error {
			for k, v := range p {
				seen[k] = v
			}
			return nil
		}))
		assert.Contains(t, seen, "2")
		assert.NotContains(t, seen, "1")
	})

	t.Run("filtered scan falls back for a range", func(t *testing.T) {
		seen := map[string]any{}
		require.NoError(t, st.ScanFiltered(ctx, predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2016.0}, func(p map[string]any) error {
			for k, v := range p {
				seen[k] = v
			}
			return nil
		}))
		// The range does not push, so the full label is scanned (correct superset).
		assert.Len(t, seen, 3)
	})
}

func TestIntegrationScanPaging(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "CREATE CONSTRAINT itPage_id IF NOT EXISTS FOR (p:ItPage) REQUIRE p.id IS UNIQUE")
	exec(t, ctx, admin, "MATCH (p:ItPage) DETACH DELETE p")
	// Exactly two scanBatch (100) pages: this pins the paging arithmetic (skip += n,
	// n < scanBatch) and, because the total is an exact multiple, the final zero-row
	// query must NOT emit an empty page — so the page count is exactly 2, not 3.
	exec(t, ctx, admin, "UNWIND range(1, 200) AS i CREATE (:ItPage {id: toString(i)})")

	st := openStore(t, ctx, "ItPage", "id")

	seen := map[string]any{}
	pages := 0
	require.NoError(t, st.ScanBatches(ctx, func(p map[string]any) error {
		pages++
		for k, v := range p {
			seen[k] = v
		}
		return nil
	}))
	assert.Len(t, seen, 200)
	assert.Equal(t, 2, pages, "200 nodes should stream in exactly two pages of 100, no trailing empty page")

	c, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(200), c)
}

func TestIntegrationGetByElementID(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "MATCH (n:ItEid) DETACH DELETE n")
	exec(t, ctx, admin, "CREATE (:ItEid {name:'solo'})")

	st := openStore(t, ctx, "ItEid", "") // no key -> elementId
	var eid string
	require.NoError(t, st.ScanBatches(ctx, func(p map[string]any) error {
		for k := range p {
			eid = k
		}
		return nil
	}))
	require.NotEmpty(t, eid)

	got, err := st.Get(ctx, []string{eid})
	require.NoError(t, err)
	require.NotNil(t, got[eid])
	assert.Equal(t, "solo", got[eid].(map[string]any)["name"])
}

func TestIntegrationWriteRoundTrip(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "CREATE CONSTRAINT itAuthor_id IF NOT EXISTS FOR (a:ItAuthor) REQUIRE a.id IS UNIQUE")
	exec(t, ctx, admin, "MATCH (a:ItAuthor) DETACH DELETE a")

	st := openStore(t, ctx, "ItAuthor", "id")

	stat, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"id": "1", "name": "Ada"}},
		{Key: "2", Value: map[string]any{"id": "2", "name": "Grace"}},
	}, query.Upsert)
	require.NoError(t, err)
	assert.Equal(t, 2, stat.Written)
	assert.Equal(t, 0, stat.Overwritten)

	// A mixed Upsert batch: one existing key (1) and one new (3). created=1, so
	// Written=1 and Overwritten = len-created = 1 — a mutant using len+created (=3)
	// would not match.
	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"id": "1", "name": "Ada L."}},
		{Key: "3", Value: map[string]any{"id": "3", "name": "Edsger"}},
	}, query.Upsert)
	require.NoError(t, err)
	assert.Equal(t, 1, stat.Written)
	assert.Equal(t, 1, stat.Overwritten)

	// The Upsert overwrote node 1's name.
	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	assert.Equal(t, "Ada L.", got["1"].(map[string]any)["name"])

	// TypedScan reads them back tagged "node".
	var recs []query.Record
	require.NoError(t, st.TypedScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	}))
	assert.Len(t, recs, 3)
	for _, r := range recs {
		assert.Equal(t, "node", r.Type)
	}

	// A mixed InsertOnly batch: one existing (1) and one new (4). created=1, so
	// Written=1 and Skipped = len-created = 1, and the existing node is NOT changed.
	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"id": "1", "name": "should-not-apply"}},
		{Key: "4", Value: map[string]any{"id": "4", "name": "Barbara"}},
	}, query.InsertOnly)
	require.NoError(t, err)
	assert.Equal(t, 1, stat.Written)
	assert.Equal(t, 1, stat.Skipped)
	got, err = st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	assert.Equal(t, "Ada L.", got["1"].(map[string]any)["name"], "InsertOnly must not overwrite an existing node")

	// A raw statement error is surfaced, not swallowed.
	_, err = st.Query(ctx, []string{"THIS IS NOT CYPHER @@@"})
	require.Error(t, err)

	require.NoError(t, st.Clear(ctx))
	c, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), c)
}

func TestIntegrationClearPagesLargeLabel(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "MATCH (n:ItBig) DETACH DELETE n")
	// More than scanBatch (100) nodes so Clear must loop over more than one delete
	// batch; a mutant that stops after the first batch would leave nodes behind.
	exec(t, ctx, admin, "UNWIND range(1, 150) AS i CREATE (:ItBig {n: i})")

	st := openStore(t, ctx, "ItBig", "")
	require.NoError(t, st.Clear(ctx))
	c, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), c)
}

// openRelStore opens a relationship-type source (?rel=TYPE) for a test.
func openRelStore(t *testing.T, ctx context.Context, relType, key string) *Store {
	t.Helper()
	u := testURL() + "?rel=" + relType
	if key != "" {
		u += "&key=" + key
	}
	st, err := Open(ctx, u, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestIntegrationRelationships(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "MATCH (n:ItRelNode) DETACH DELETE n")
	exec(t, ctx, admin, `CREATE (a:ItRelNode {id:'a'}), (b:ItRelNode {id:'b'}), (c:ItRelNode {id:'c'}),
		(a)-[:IT_KNOWS {since: 2019}]->(b),
		(a)-[:IT_KNOWS {since: 2021}]->(c)`)

	st := openRelStore(t, ctx, "IT_KNOWS", "")

	t.Run("scan yields the relationship envelope", func(t *testing.T) {
		seen := map[string]any{}
		require.NoError(t, st.ScanBatches(ctx, func(p map[string]any) error {
			for k, v := range p {
				seen[k] = v
			}
			return nil
		}))
		require.Len(t, seen, 2)
		for _, v := range seen {
			rel := v.(map[string]any)
			assert.Equal(t, "IT_KNOWS", rel["_type"])
			assert.NotEmpty(t, rel["_start"])
			assert.NotEmpty(t, rel["_end"])
			assert.Contains(t, rel, "since")
		}
	})

	t.Run("count", func(t *testing.T) {
		c, err := st.EstimateCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), c)
	})

	t.Run("filtered scan pushes a property equality on r", func(t *testing.T) {
		seen := 0
		require.NoError(t, st.ScanFiltered(ctx, predicate.Eq{Path: []string{"since"}, Value: 2019.0}, func(p map[string]any) error {
			seen += len(p)
			return nil
		}))
		assert.Equal(t, 1, seen)
	})

	t.Run("get by elementId and typed scan", func(t *testing.T) {
		var eid string
		var recs []query.Record
		require.NoError(t, st.TypedScan(ctx, func(batch []query.Record) error {
			recs = append(recs, batch...)
			return nil
		}))
		require.Len(t, recs, 2)
		for _, r := range recs {
			assert.Equal(t, "relationship", r.Type)
			eid = r.Key
		}
		got, err := st.Get(ctx, []string{eid})
		require.NoError(t, err)
		assert.Equal(t, "IT_KNOWS", got[eid].(map[string]any)["_type"])
	})

	t.Run("colon-marker address resolves the same relationship type", func(t *testing.T) {
		marked, err := Open(ctx, testURL(), ":IT_KNOWS", nil, numfmt.DecimalAuto)
		require.NoError(t, err)
		t.Cleanup(func() { _ = marked.Close() })
		c, err := marked.EstimateCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), c)
	})
}

func TestIntegrationInspect(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "CREATE CONSTRAINT itIns_id IF NOT EXISTS FOR (p:ItInspect) REQUIRE p.id IS UNIQUE")
	exec(t, ctx, admin, "MATCH (p:ItInspect) DETACH DELETE p")
	exec(t, ctx, admin, "CREATE (a:ItInspect {id:'1'})-[:IT_LINKS]->(b:ItInspect {id:'2'})")

	st := openStore(t, ctx, "ItInspect", "id")

	server, err := st.InspectServer(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, server)

	dbs, err := st.InspectDatabases(ctx)
	require.NoError(t, err)
	assert.Contains(t, dbs, "neo4j")

	labels, err := st.InspectLabels(ctx)
	require.NoError(t, err)
	assert.Contains(t, labels, "ItInspect")

	reltypes, err := st.InspectRelationshipTypes(ctx)
	require.NoError(t, err)
	assert.Contains(t, reltypes, "IT_LINKS")

	constraints, err := st.InspectConstraints(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, constraints)
}

func TestIntegrationKeyIntegrity(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")

	t.Run("write refused without a uniqueness constraint", func(t *testing.T) {
		exec(t, ctx, admin, "MATCH (n:ItNoConstraint) DETACH DELETE n")
		st := openStore(t, ctx, "ItNoConstraint", "id")
		_, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"id": "1"}}}, query.Upsert)
		require.ErrorContains(t, err, "no uniqueness constraint")
	})

	t.Run("ambiguous bounded lookup errors", func(t *testing.T) {
		exec(t, ctx, admin, "MATCH (n:ItDup) DETACH DELETE n")
		exec(t, ctx, admin, "CREATE (:ItDup {id:'dup', v:1}), (:ItDup {id:'dup', v:2})")
		st := openStore(t, ctx, "ItDup", "id")
		_, err := st.Get(ctx, []string{"dup"})
		require.ErrorContains(t, err, "matches more than one node")
	})

	t.Run("keyless node falls back to elementId on scan", func(t *testing.T) {
		exec(t, ctx, admin, "MATCH (n:ItKeyless) DETACH DELETE n")
		exec(t, ctx, admin, "CREATE (:ItKeyless {id:'has-key'}), (:ItKeyless {name:'no-key'})")
		st := openStore(t, ctx, "ItKeyless", "id")
		seen := map[string]any{}
		require.NoError(t, st.ScanBatches(ctx, func(p map[string]any) error {
			for k, v := range p {
				seen[k] = v
			}
			return nil
		}))
		require.Len(t, seen, 2)
		assert.Contains(t, seen, "has-key")
		// The keyless node is keyed by its elementId, which contains ':'.
		var fellBack bool
		for k := range seen {
			if k != "has-key" {
				fellBack = strings.Contains(k, ":")
			}
		}
		assert.True(t, fellBack, "keyless node should be keyed by its elementId")
	})
}
