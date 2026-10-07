package neo4j

import (
	"errors"
	"strconv"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// getRow builds a Get result row: the key and the entity.
func getRow(k string, ent any) *neo4j.Record {
	return &neo4j.Record{Keys: []string{"k", "n"}, Values: []any{k, ent}}
}

// scanRow builds a scan result row: the key, the elementId and the entity.
func scanRow(k, eid string, ent any) *neo4j.Record {
	return &neo4j.Record{Keys: []string{"k", "eid", "n"}, Values: []any{k, eid, ent}}
}

// scanPageRows builds n scan rows with unique keys and elementIds from start.
func scanPageRows(start, n int) []*neo4j.Record {
	out := make([]*neo4j.Record, n)
	for i := range out {
		id := strconv.Itoa(start + i)
		out[i] = scanRow("k"+id, "e"+id, map[string]any{"v": int64(start + i)})
	}
	return out
}

func TestGetBuildsTheStatement(t *testing.T) {
	tests := []struct {
		name       string
		tgt        target
		wantCypher string
		wantParams map[string]any
	}{
		{
			"keyless node",
			target{name: "Person"},
			"MATCH (n:`Person`) WHERE elementId(n) IN $ids RETURN elementId(n) AS k, n",
			map[string]any{"ids": []string{"a", "b"}},
		},
		{
			"keyed node",
			target{name: "Person", key: "id"},
			"MATCH (n:`Person`) WHERE toString(n[$key]) IN $ids RETURN toString(n[$key]) AS k, n",
			map[string]any{"ids": []string{"a", "b"}, "key": "id"},
		},
		{
			"keyless relationship",
			target{kind: relTarget, name: "KNOWS"},
			"MATCH ()-[r:`KNOWS`]->() WHERE elementId(r) IN $ids RETURN elementId(r) AS k, r",
			map[string]any{"ids": []string{"a", "b"}},
		},
		{
			"keyed relationship",
			target{kind: relTarget, name: "KNOWS", key: "since"},
			"MATCH ()-[r:`KNOWS`]->() WHERE toString(r[$key]) IN $ids RETURN toString(r[$key]) AS k, r",
			map[string]any{"ids": []string{"a", "b"}, "key": "since"},
		},
		{
			"nameless node",
			target{},
			"MATCH (n) WHERE elementId(n) IN $ids RETURN elementId(n) AS k, n",
			map[string]any{"ids": []string{"a", "b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, drv, trace := newFakeStore(tt.tgt, rows())
			got, err := s.Get(markedContext(), []string{"a", "b"})
			require.NoError(t, err)
			require.Empty(t, got)
			require.Len(t, drv.session.runs, 1)
			require.Equal(t, tt.wantCypher, drv.session.runs[0].cypher)
			require.Equal(t, tt.wantParams, drv.session.runs[0].params)
			require.Equal(t, "cypher: "+tt.wantCypher+"\n", trace.String())
			require.Equal(t, neo4j.SessionConfig{DatabaseName: "db1", AccessMode: neo4j.AccessModeRead}, drv.configs[0])
		})
	}
}

func TestGetSkipsNilAndMissing(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P"}, rows(
		getRow("a", map[string]any{"v": int64(1)}),
		getRow("b", nil),
		&neo4j.Record{Keys: []string{"k", "n"}, Values: []any{42, map[string]any{"v": int64(2)}}},
	))
	got, err := s.Get(markedContext(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"v": 1}, "": map[string]any{"v": 2}}, got)
}

func TestGetDuplicateKey(t *testing.T) {
	dup := func() fakeReply {
		return rows(getRow("a", map[string]any{"v": int64(1)}), getRow("a", map[string]any{"v": int64(2)}))
	}

	t.Run("keyed", func(t *testing.T) {
		s, _, _ := newFakeStore(target{name: "P", key: "id"}, dup())
		got, err := s.Get(markedContext(), []string{"a"})
		require.EqualError(t, err, `neo4j: key "a" matches more than one node; P.id is not unique`)
		require.Nil(t, got)
	})
	t.Run("keyed relationship", func(t *testing.T) {
		s, _, _ := newFakeStore(target{kind: relTarget, name: "K", key: "id"}, dup())
		_, err := s.Get(markedContext(), []string{"a"})
		require.EqualError(t, err, `neo4j: key "a" matches more than one relationship; K.id is not unique`)
	})
	t.Run("keyless", func(t *testing.T) {
		s, _, _ := newFakeStore(target{name: "P"}, dup())
		got, err := s.Get(markedContext(), []string{"a"})
		require.NoError(t, err)
		require.Equal(t, map[string]any{"a": map[string]any{"v": 2}}, got, "the later row wins")
	})
}

func TestGetErrors(t *testing.T) {
	cause := errors.New("boom")
	t.Run("run", func(t *testing.T) {
		s, _, _ := newFakeStore(target{name: "P"}, fakeReply{err: cause})
		got, err := s.Get(markedContext(), []string{"a"})
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j run:")
		require.Nil(t, got)
	})
	t.Run("stream", func(t *testing.T) {
		s, _, _ := newFakeStore(target{name: "P"}, failing(cause, getRow("a", map[string]any{})))
		got, err := s.Get(markedContext(), []string{"a"})
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j get:")
		require.Nil(t, got)
	})
}

func TestGetPassesTheContext(t *testing.T) {
	s, drv, _ := newFakeStore(target{name: "P"}, rows(getRow("a", map[string]any{})))
	_, err := s.Get(markedContext(), []string{"a"})
	require.NoError(t, err)
	require.True(t, isMarked(drv.ctxs[0]), "NewSession")
	require.True(t, isMarked(drv.session.runs[0].ctx), "Run")
	require.True(t, isMarked(drv.session.closeCtxs[0]), "Close")
	require.Equal(t, 1, drv.session.closed)
	for _, c := range drv.session.script[0].res.nextCtxs {
		require.True(t, isMarked(c), "Next")
	}
}

func TestScanReturnsErrNoLabelBeforeASession(t *testing.T) {
	s := &Store{} // no driver: a session would panic.
	err := s.ScanBatches(markedContext(), func(map[string]any) error { return nil })
	require.ErrorIs(t, err, errNoLabel)
}

func TestScanStatement(t *testing.T) {
	const tail = " RETURN elementId(n) AS k, elementId(n) AS eid, n ORDER BY eid LIMIT $limit"
	const keyExpr = "CASE WHEN n[$key] IS NULL THEN elementId(n) ELSE toString(n[$key]) END"
	tests := []struct {
		name       string
		tgt        target
		where      string
		filter     map[string]any
		wantCypher string
		wantParams map[string]any
	}{
		{
			"keyless",
			target{name: "P"},
			"", nil,
			"MATCH (n:`P`) WHERE elementId(n) > $after" + tail,
			map[string]any{"limit": 100, "after": ""},
		},
		{
			"keyed",
			target{name: "P", key: "id"},
			"", nil,
			"MATCH (n:`P`) WHERE elementId(n) > $after RETURN " + keyExpr + " AS k, elementId(n) AS eid, n ORDER BY eid LIMIT $limit",
			map[string]any{"limit": 100, "after": "", "key": "id"},
		},
		{
			"relationship",
			target{kind: relTarget, name: "K"},
			"", nil,
			"MATCH ()-[r:`K`]->() WHERE elementId(r) > $after RETURN elementId(r) AS k, elementId(r) AS eid, r ORDER BY eid LIMIT $limit",
			map[string]any{"limit": 100, "after": ""},
		},
		{
			"filtered",
			target{name: "P"},
			"n.`a` = $f0",
			map[string]any{"f0": 5},
			"MATCH (n:`P`) WHERE elementId(n) > $after AND (n.`a` = $f0)" + tail,
			map[string]any{"limit": 100, "after": "", "f0": 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, drv, trace := newFakeStore(tt.tgt, rows())
			require.NoError(t, s.pagedScan(markedContext(), tt.where, tt.filter, func(map[string]any) error { return nil }))
			require.Len(t, drv.session.runs, 1)
			require.Equal(t, tt.wantCypher, drv.session.runs[0].cypher)
			require.Equal(t, tt.wantParams, drv.session.runs[0].params)
			require.Equal(t, "cypher: "+tt.wantCypher+"\n", trace.String())
			require.Equal(t, neo4j.SessionConfig{DatabaseName: "db1", AccessMode: neo4j.AccessModeRead}, drv.configs[0])
		})
	}
}

func TestScanPagesByKeyset(t *testing.T) {
	t.Run("full page continues", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 100)...), rows(scanPageRows(100, 3)...))
		var sizes []int
		require.NoError(t, s.ScanBatches(markedContext(), func(b map[string]any) error {
			sizes = append(sizes, len(b))
			return nil
		}))
		require.Equal(t, []int{100, 3}, sizes)
		require.Len(t, drv.session.runs, 2)
		require.Equal(t, "", drv.session.runs[0].params["after"])
		require.Equal(t, "e99", drv.session.runs[1].params["after"])
	})
	t.Run("short page stops", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 99)...))
		require.NoError(t, s.ScanBatches(markedContext(), func(map[string]any) error { return nil }))
		require.Len(t, drv.session.runs, 1)
	})
	t.Run("empty page after a full one", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 100)...), rows())
		calls := 0
		require.NoError(t, s.ScanBatches(markedContext(), func(map[string]any) error { calls++; return nil }))
		require.Equal(t, 1, calls)
		require.Len(t, drv.session.runs, 2)
	})
}

func TestScanCursorSkipsNilEntities(t *testing.T) {
	page := scanPageRows(0, 99)
	page = append(page, scanRow("kx", "ex", nil))
	s, drv, _ := newFakeStore(target{name: "P"}, rows(page...), rows())
	var got []map[string]any
	require.NoError(t, s.ScanBatches(markedContext(), func(b map[string]any) error { got = append(got, b); return nil }))
	require.Len(t, got, 1)
	require.Len(t, got[0], 99)
	require.NotContains(t, got[0], "kx")
	require.Len(t, drv.session.runs, 2, "the nil row counts toward the page size")
	require.Equal(t, "e98", drv.session.runs[1].params["after"])
}

func TestScanAllNilPageMakesNoCallback(t *testing.T) {
	nils := make([]*neo4j.Record, 100)
	for i := range nils {
		nils[i] = scanRow("k", "e"+strconv.Itoa(i), nil)
	}
	s, drv, _ := newFakeStore(target{name: "P"}, rows(nils...), rows())
	calls := 0
	require.NoError(t, s.ScanBatches(markedContext(), func(map[string]any) error { calls++; return nil }))
	require.Zero(t, calls)
	require.Len(t, drv.session.runs, 2)
	require.Equal(t, "", drv.session.runs[1].params["after"], "no entity was seen, so the cursor stays")
}

func TestScanKeepsDuplicateKeysApart(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P", key: "id"}, rows(
		scanRow("a", "e1", map[string]any{"v": int64(1)}),
		scanRow("a", "e2", map[string]any{"v": int64(2)}),
	))
	var got map[string]any
	require.NoError(t, s.ScanBatches(markedContext(), func(b map[string]any) error { got = b; return nil }))
	require.Equal(t, map[string]any{"a": map[string]any{"v": 1}, "e2": map[string]any{"v": 2}}, got)
}

func TestScanStopsOnCallbackError(t *testing.T) {
	stop := errors.New("stop")
	s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 100)...), rows())
	err := s.ScanBatches(markedContext(), func(map[string]any) error { return stop })
	require.Equal(t, stop, err)
	require.Len(t, drv.session.runs, 1)
	require.Equal(t, 1, drv.session.closed)
}

func TestScanErrors(t *testing.T) {
	cause := errors.New("boom")
	noop := func(map[string]any) error { return nil }
	t.Run("run on page one", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, fakeReply{err: cause})
		err := s.ScanBatches(markedContext(), noop)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j run:")
		require.Equal(t, 1, drv.session.closed)
	})
	t.Run("run on page two", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 100)...), fakeReply{err: cause})
		calls := 0
		err := s.ScanBatches(markedContext(), func(map[string]any) error { calls++; return nil })
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j run:")
		require.Equal(t, 1, calls)
		require.Equal(t, 1, drv.session.closed)
	})
	t.Run("stream", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{name: "P"}, failing(cause, scanPageRows(0, 3)...))
		calls := 0
		err := s.ScanBatches(markedContext(), func(map[string]any) error { calls++; return nil })
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j scan:")
		require.Zero(t, calls, "a failed page is not handed to the callback")
		require.Equal(t, 1, drv.session.closed)
	})
}

func TestScanPassesTheContext(t *testing.T) {
	s, drv, _ := newFakeStore(target{name: "P"}, rows(scanPageRows(0, 100)...), rows(scanPageRows(100, 1)...))
	require.NoError(t, s.ScanBatches(markedContext(), func(map[string]any) error { return nil }))
	require.True(t, isMarked(drv.ctxs[0]), "NewSession")
	require.Len(t, drv.session.runs, 2)
	for _, r := range drv.session.runs {
		require.True(t, isMarked(r.ctx), "Run")
	}
	for _, rep := range drv.session.script {
		for _, c := range rep.res.nextCtxs {
			require.True(t, isMarked(c), "Next")
		}
	}
	require.True(t, isMarked(drv.session.closeCtxs[0]), "Close")
	require.Equal(t, 1, drv.session.closed)
}

func TestQueryRunsInAWriteSession(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCypher string
		wantParams map[string]any
	}{
		{"statement only", []string{"  RETURN 1  "}, "RETURN 1", nil},
		{"with parameters", []string{"RETURN $a", `{"a":1}`}, "RETURN $a", map[string]any{"a": float64(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, drv, trace := newFakeStore(target{}, rows())
			_, err := s.Query(markedContext(), tt.args)
			require.NoError(t, err)
			require.Equal(t, neo4j.SessionConfig{DatabaseName: "db1", AccessMode: neo4j.AccessModeWrite}, drv.configs[0])
			require.Equal(t, tt.wantCypher, drv.session.runs[0].cypher)
			require.Equal(t, tt.wantParams, drv.session.runs[0].params)
			require.Equal(t, "cypher: "+tt.wantCypher+"\n", trace.String())
		})
	}
}

func TestQueryRows(t *testing.T) {
	s, _, _ := newFakeStore(target{}, rows(
		&neo4j.Record{Keys: []string{"a", "b"}, Values: []any{int64(7), "x"}},
		&neo4j.Record{Keys: []string{"a", "b"}, Values: []any{nil, []byte("hi")}},
	))
	got, err := s.Query(markedContext(), []string{"RETURN 1"})
	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"a": 7, "b": "x"},
		map[string]any{"a": nil, "b": "aGk="},
	}, got)

	empty, _, _ := newFakeStore(target{}, rows())
	got, err = empty.Query(markedContext(), []string{"RETURN 1"})
	require.NoError(t, err)
	require.Equal(t, []any{}, got, "an empty result is an empty list, not nil")
}

func TestQueryArgumentErrors(t *testing.T) {
	const count = "neo4j raw expects a Cypher statement and an optional JSON parameters object"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, count},
		{"three", []string{"a", "{}", "c"}, count},
		{"blank", []string{" \t "}, "neo4j raw expects a non-empty Cypher statement"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{} // no driver: the arguments fail first.
			_, err := s.Query(markedContext(), tt.args)
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestQueryErrors(t *testing.T) {
	cause := errors.New("boom")
	t.Run("run", func(t *testing.T) {
		s, _, _ := newFakeStore(target{}, fakeReply{err: cause})
		got, err := s.Query(markedContext(), []string{"RETURN 1"})
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j run:")
		require.Nil(t, got)
	})
	t.Run("stream", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{}, failing(cause, &neo4j.Record{Keys: []string{"a"}, Values: []any{"x"}}))
		got, err := s.Query(markedContext(), []string{"RETURN 1"})
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "neo4j query:")
		require.Nil(t, got)
		require.Equal(t, 1, drv.session.closed)
	})
	t.Run("bad parameters open no session", func(t *testing.T) {
		s, drv, _ := newFakeStore(target{}, rows())
		_, err := s.Query(markedContext(), []string{"RETURN 1", "{"})
		require.ErrorContains(t, err, "parse neo4j query parameters:")
		require.Empty(t, drv.configs)
	})
}

func TestQueryPassesTheContext(t *testing.T) {
	s, drv, _ := newFakeStore(target{}, rows(&neo4j.Record{Keys: []string{"a"}, Values: []any{"x"}}))
	_, err := s.Query(markedContext(), []string{"RETURN 1"})
	require.NoError(t, err)
	require.True(t, isMarked(drv.ctxs[0]), "NewSession")
	require.True(t, isMarked(drv.session.runs[0].ctx), "Run")
	require.True(t, isMarked(drv.session.closeCtxs[0]), "Close")
	for _, c := range drv.session.script[0].res.nextCtxs {
		require.True(t, isMarked(c), "Next")
	}
	require.Equal(t, 1, drv.session.closed)
}
