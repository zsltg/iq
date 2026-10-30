package neo4j

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

type noticeCall struct{ key, field string }

// noticeContext returns the marked context with a sink that appends to calls.
func noticeContext(calls *[]noticeCall) context.Context {
	return query.WithNonFiniteNotice(markedContext(), func(key, field string) {
		*calls = append(*calls, noticeCall{key, field})
	})
}

func TestNormalizeValueConvertsNonFinite(t *testing.T) {
	tests := []struct {
		name       string
		mode       numfmt.DecimalMode
		in         any
		want       any
		wantFields []string
	}{
		{"nan", numfmt.DecimalAuto, math.NaN(), "NaN", []string{"p"}},
		{"positive infinity", numfmt.DecimalAuto, math.Inf(1), "Infinity", []string{"p"}},
		{"negative infinity", numfmt.DecimalAuto, math.Inf(-1), "-Infinity", []string{"p"}},
		{"infinity in string mode", numfmt.DecimalString, math.Inf(1), "Infinity", []string{"p"}},
		{"finite in string mode", numfmt.DecimalString, 1.5, "1.5", nil},
		{"finite", numfmt.DecimalAuto, 1.5, 1.5, nil},
		{"list", numfmt.DecimalAuto, []any{1.5, math.NaN()}, []any{1.5, "NaN"}, []string{"p[1]"}},
		{
			"map",
			numfmt.DecimalAuto,
			map[string]any{"in": map[string]any{"up": math.Inf(1)}},
			map[string]any{"in": map[string]any{"up": "Infinity"}},
			[]string{"p.in.up"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{decimal: tt.mode}
			h := &numfmt.Hits{}
			h.Enter("p")

			got := s.normalizeValue(tt.in, h)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantFields, h.Fields)
		})
	}
}

func TestGetReportsNonFinite(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P"}, rows(
		getRow("a", map[string]any{"score": math.Inf(1), "ok": 1.5}),
	))
	var calls []noticeCall

	got, err := s.Get(noticeContext(&calls), []string{"a"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"score": "Infinity", "ok": 1.5}}, got)
	require.Equal(t, []noticeCall{{"a", "score"}}, calls)
}

func TestScanReportsNonFinite(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P"}, rows(
		scanRow("a", "e1", map[string]any{"v": 1.5}),
		scanRow("b", "e2", map[string]any{"v": math.NaN()}),
	))
	var calls []noticeCall
	var seen map[string]any

	err := s.ScanBatches(noticeContext(&calls), func(b map[string]any) error {
		seen = b
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"v": 1.5}, "b": map[string]any{"v": "NaN"}}, seen)
	require.Equal(t, []noticeCall{{"b", "v"}}, calls)
}

func TestQueryReportsNonFiniteWithRowKeys(t *testing.T) {
	s, _, _ := newFakeStore(target{}, rows(
		&neo4j.Record{Keys: []string{"a", "b"}, Values: []any{math.Inf(1), math.NaN()}},
		&neo4j.Record{Keys: []string{"a", "b"}, Values: []any{1.5, math.Inf(-1)}},
	))
	var calls []noticeCall

	got, err := s.Query(noticeContext(&calls), []string{"MATCH (n) RETURN n"})

	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"a": "Infinity", "b": "NaN"},
		map[string]any{"a": 1.5, "b": "-Infinity"},
	}, got)
	require.Equal(t, []noticeCall{{"row 1", "a"}, {"row 1", "b"}, {"row 2", "b"}}, calls)
}

func TestNormalizeValueReportsEntityPaths(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	node := neo4j.Node{ElementId: "n1", Props: map[string]any{"x": nan, "y": inf}}
	rel := neo4j.Relationship{ElementId: "r1", Props: map[string]any{"x": nan, "y": inf}}
	tests := []struct {
		name       string
		in         any
		wantFields []string
	}{
		{"node", node, []string{"p.x", "p.y"}},
		{"relationship", rel, []string{"p.x", "p.y"}},
		{"map", map[string]any{"x": nan, "y": inf}, []string{"p.x", "p.y"}},
		{
			"path",
			dbtype.Path{Nodes: []dbtype.Node{node, node}, Relationships: []dbtype.Relationship{rel}},
			[]string{
				"p.nodes[0].x", "p.nodes[0].y", "p.nodes[1].x", "p.nodes[1].y",
				"p.relationships[0].x", "p.relationships[0].y",
			},
		},
		{
			"path with two relationships",
			dbtype.Path{Nodes: []dbtype.Node{node}, Relationships: []dbtype.Relationship{rel, rel}},
			[]string{
				"p.nodes[0].x", "p.nodes[0].y",
				"p.relationships[0].x", "p.relationships[0].y", "p.relationships[1].x", "p.relationships[1].y",
			},
		},
		{
			"list with a value after a path",
			[]any{dbtype.Path{Relationships: []dbtype.Relationship{rel}}, nan},
			[]string{"p[0].relationships[0].x", "p[0].relationships[0].y", "p[1]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{}
			h := &numfmt.Hits{}
			h.Enter("p")

			s.normalizeValue(tt.in, h)

			require.ElementsMatch(t, tt.wantFields, h.Fields)
		})
	}
}

func TestNormalizeValuePlainTypes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"integer", int64(7), 7},
		{"bytes", []byte("hi"), "aGk="},
		{"time", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05Z"},
		{"date", dbtype.Date(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)), "2026-01-02"},
		{"point 2d", dbtype.Point2D{X: 1, Y: 2, SpatialRefId: 7203}, map[string]any{"x": 1.0, "y": 2.0, "srid": 7203}},
		{"point 3d", dbtype.Point3D{X: 1, Y: 2, Z: 3, SpatialRefId: 9157}, map[string]any{"x": 1.0, "y": 2.0, "z": 3.0, "srid": 9157}},
		{"string", "s", "s"},
		{"null", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, (&Store{}).normalizeValue(tt.in, nil))
		})
	}
}

func TestReadRowsAndColumnNormalizeValues(t *testing.T) {
	s, _, _ := newFakeStore(target{}, rows(
		&neo4j.Record{Keys: []string{"a", "b"}, Values: []any{int64(1), "x"}},
	), rows(
		&neo4j.Record{Keys: []string{"c"}, Values: []any{int64(2)}},
	))

	gotRows, err := s.readRows(markedContext(), "RETURN 1")
	require.NoError(t, err)
	gotCol, err := s.readColumn(markedContext(), "RETURN 2")
	require.NoError(t, err)

	require.Equal(t, []any{map[string]any{"a": 1, "b": "x"}}, gotRows)
	require.Equal(t, []any{2}, gotCol)
}
