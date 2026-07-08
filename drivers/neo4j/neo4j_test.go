package neo4j

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		address  string
		wantDSN  string
		wantDB   string
		wantUser string
		wantPass string
		wantAuth bool
		wantLbl  string
		wantKey  string
		wantErr  string
	}{
		{
			name:     "full url with auth label key",
			url:      "neo4j://neo4j:secret@localhost:7687/?database=books&label=Person&key=id",
			wantDSN:  "neo4j://localhost:7687",
			wantDB:   "books",
			wantUser: "neo4j",
			wantPass: "secret",
			wantAuth: true,
			wantLbl:  "Person",
			wantKey:  "id",
		},
		{
			name:    "database defaults to neo4j",
			url:     "neo4j://localhost:7687/?label=Person",
			wantDSN: "neo4j://localhost:7687",
			wantDB:  "neo4j",
			wantLbl: "Person",
		},
		{
			name:    "dotted address overrides ?label=",
			url:     "neo4j://localhost:7687/?label=Person",
			address: "Book",
			wantDSN: "neo4j://localhost:7687",
			wantDB:  "neo4j",
			wantLbl: "Book",
		},
		{
			name:    "bolt+s scheme passes through",
			url:     "bolt+s://localhost:7687/",
			wantDSN: "bolt+s://localhost:7687",
			wantDB:  "neo4j",
		},
		{
			name:    "no label is allowed (raw/elementId use)",
			url:     "neo4j://localhost:7687/",
			wantDSN: "neo4j://localhost:7687",
			wantDB:  "neo4j",
		},
		{
			name:    "unknown scheme",
			url:     "http://localhost:7687/",
			wantErr: "must use neo4j:// or bolt://",
		},
		{
			name:    "missing host",
			url:     "neo4j:///?label=Person",
			wantErr: "must name a host",
		},
		{
			name:    "label with a marker char is rejected",
			url:     "neo4j://localhost:7687/?label=:KNOWS",
			wantErr: "must be a bare identifier",
		},
		{
			name:    "label with a leading digit is rejected",
			url:     "neo4j://localhost:7687/?label=1Person",
			wantErr: "must be a bare identifier",
		},
		{
			name:    "key with a marker char is rejected",
			url:     "neo4j://localhost:7687/?label=Person&key=a.b",
			wantErr: "must be a bare identifier",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, tt.address)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDSN, cc.dsn)
			require.Equal(t, tt.wantDB, cc.database)
			require.Equal(t, tt.wantAuth, cc.hasAuth)
			require.Equal(t, tt.wantUser, cc.user)
			require.Equal(t, tt.wantPass, cc.pass)
			require.Equal(t, tt.wantLbl, cc.target.name)
			require.Equal(t, tt.wantKey, cc.target.key)
		})
	}
}

func TestParseURLRelTarget(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		address  string
		wantKind targetKind
		wantName string
		wantErr  string
	}{
		{
			name:     "?rel= selects a relationship type",
			url:      "neo4j://localhost:7687/?rel=KNOWS",
			wantKind: relTarget,
			wantName: "KNOWS",
		},
		{
			name:     "colon-prefixed address selects a relationship type",
			url:      "neo4j://localhost:7687/?label=Person",
			address:  ":WROTE",
			wantKind: relTarget,
			wantName: "WROTE",
		},
		{
			name:     "plain address stays a node label",
			url:      "neo4j://localhost:7687/",
			address:  "Movie",
			wantKind: nodeTarget,
			wantName: "Movie",
		},
		{
			name:    "both ?label= and ?rel= is an error",
			url:     "neo4j://localhost:7687/?label=Person&rel=KNOWS",
			wantErr: "both ?label= and ?rel=",
		},
		{
			name:    "empty relationship type is an error",
			url:     "neo4j://localhost:7687/?label=Person",
			address: ":",
			wantErr: "relationship type is empty",
		},
		{
			name:    "relationship type must be an identifier",
			url:     "neo4j://localhost:7687/?rel=a-b",
			wantErr: "relationship type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, tt.address)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantKind, cc.target.kind)
			require.Equal(t, tt.wantName, cc.target.name)
		})
	}
}

func TestRelationshipWritesDeferred(t *testing.T) {
	s := &Store{target: target{kind: relTarget, name: "KNOWS"}}
	_, err := s.Put(context.Background(), []query.Record{{Key: "1", Value: map[string]any{}}}, query.Upsert)
	require.ErrorIs(t, err, errRelWriteUnsupported)
	require.ErrorIs(t, s.Clear(context.Background()), errRelWriteUnsupported)
}

func TestTargetMatch(t *testing.T) {
	require.Equal(t, "MATCH (n)", target{}.match())
	require.Equal(t, "MATCH (n:`Person`)", target{name: "Person"}.match())
	require.Equal(t, "MATCH ()-[r:`KNOWS`]->()", target{kind: relTarget, name: "KNOWS"}.match())
	require.Equal(t, "n", target{name: "Person"}.variable())
	require.Equal(t, "r", target{kind: relTarget, name: "KNOWS"}.variable())
}

func TestNormalizeValue(t *testing.T) {
	ts := time.Date(2020, 5, 4, 3, 2, 1, 0, time.UTC)
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		in   any
		want any
	}{
		{name: "nil", in: nil, want: nil},
		{name: "bool", in: true, want: true},
		{name: "int64 to int", in: int64(42), want: 42},
		{name: "float auto stays float", mode: numfmt.DecimalAuto, in: 3.5, want: 3.5},
		{name: "float string mode", mode: numfmt.DecimalString, in: 3.5, want: "3.5"},
		{name: "string", in: "hi", want: "hi"},
		{name: "bytes to base64", in: []byte("hi"), want: "aGk="},
		{name: "big int preserved", in: big.NewInt(9), want: big.NewInt(9)},
		{name: "zoned datetime to rfc3339", in: ts, want: "2020-05-04T03:02:01Z"},
		{name: "duration to string", in: dbtype.Duration{Months: 1, Days: 2, Seconds: 3, Nanos: 0}, want: "P1M2DT3S"},
		{name: "point2d to object", in: dbtype.Point2D{X: 1, Y: 2, SpatialRefId: 7203}, want: map[string]any{"x": 1.0, "y": 2.0, "srid": 7203}},
		{name: "list recurses", in: []any{int64(1), "a"}, want: []any{1, "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{decimal: tt.mode}
			require.Equal(t, tt.want, s.normalizeValue(tt.in))
		})
	}
}

func TestNormalizeNode(t *testing.T) {
	s := &Store{}
	node := neo4j.Node{
		ElementId: "4:abc:1",
		Labels:    []string{"Person"},
		Props:     map[string]any{"name": "Ada", "age": int64(36)},
	}
	got := s.normalizeNode(node)
	require.Equal(t, "Ada", got["name"])
	require.Equal(t, 36, got["age"])
	require.Equal(t, "4:abc:1", got["_id"])
	require.Equal(t, []any{"Person"}, got["_labels"])
}

func TestNormalizeRelationship(t *testing.T) {
	s := &Store{}
	rel := neo4j.Relationship{
		ElementId:      "5:rel:1",
		StartElementId: "4:a:1",
		EndElementId:   "4:b:2",
		Type:           "KNOWS",
		Props:          map[string]any{"since": int64(2019)},
	}
	got := s.normalizeRelationship(rel)
	require.Equal(t, 2019, got["since"])
	require.Equal(t, "KNOWS", got["_type"])
	require.Equal(t, "4:a:1", got["_start"])
	require.Equal(t, "4:b:2", got["_end"])
	require.Equal(t, "5:rel:1", got["_id"])
}

func TestRecordProps(t *testing.T) {
	t.Run("strips reserved and derives key from value", func(t *testing.T) {
		rec := query.Record{Key: "ignored", Value: map[string]any{
			"id": "7", "name": "Ada", "_id": "4:x:1", "_labels": []any{"Person"},
		}}
		props, keyVal, err := recordProps(rec, "id")
		require.NoError(t, err)
		require.Equal(t, "7", keyVal)
		require.Equal(t, map[string]any{"id": "7", "name": "Ada"}, props)
	})
	t.Run("falls back to record key when property absent", func(t *testing.T) {
		rec := query.Record{Key: "42", Value: map[string]any{"name": "Ada"}}
		props, keyVal, err := recordProps(rec, "id")
		require.NoError(t, err)
		require.Equal(t, "42", keyVal)
		require.Equal(t, "42", props["id"])
	})
	t.Run("non-object value errors", func(t *testing.T) {
		_, _, err := recordProps(query.Record{Key: "1", Value: "scalar"}, "id")
		require.ErrorContains(t, err, "must be a JSON object")
	})
}

func TestQueryArgValidation(t *testing.T) {
	s := &Store{} // never reaches the driver: every case errors before opening a session.
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no args", args: nil, want: "a Cypher statement"},
		{name: "too many args", args: []string{"a", "b", "c"}, want: "a Cypher statement"},
		{name: "empty statement", args: []string{"   "}, want: "non-empty Cypher"},
		{name: "bad params json", args: []string{"RETURN 1", "{not json"}, want: "parse neo4j query parameters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Query(context.Background(), tt.args)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestFormatRaw(t *testing.T) {
	s := &Store{}
	out := s.FormatRaw([]any{map[string]any{"n": 6}}, false)
	require.Contains(t, out, "\"n\"")
	require.Contains(t, out, "6")

	// A value encoding/json cannot marshal falls back to a plain rendering rather
	// than returning an error string the user cannot read.
	fallback := s.FormatRaw(make(chan int), false)
	require.NotEmpty(t, fallback)
}

func TestExplainWriteModes(t *testing.T) {
	up := ExplainWrite(query.Upsert)
	require.Len(t, up.Ops, 1)
	require.Contains(t, up.Ops[0], "replace-upsert")

	ins := ExplainWrite(query.InsertOnly)
	require.Len(t, ins.Ops, 1)
	require.Contains(t, ins.Ops[0], "skip existing")
	require.NotEqual(t, up.Ops[0], ins.Ops[0])
}

func TestExplainClear(t *testing.T) {
	plan := ExplainClear()
	require.Len(t, plan.Ops, 1)
	require.Contains(t, plan.Ops[0], "DETACH DELETE")
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	require.NotEmpty(t, plan.Ops)
	var mentionsDetachDelete bool
	for _, op := range plan.Ops {
		if strings.Contains(op, "DETACH DELETE") {
			mentionsDetachDelete = true
		}
	}
	require.True(t, mentionsDetachDelete, "delete plan should mention DETACH DELETE")
}

func TestExplainPlanPushesPredicate(t *testing.T) {
	plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Eq{Path: []string{"name"}, Value: "Ada"}, false)
	require.NotNil(t, plan.Filter)
	require.Equal(t, "n[$f0] = $f1", plan.Filter["where"])
	require.Contains(t, plan.Ops[0], "server-side pre-filter")
}

func TestExplainPlanMaterializesWhenUnbounded(t *testing.T) {
	plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true)
	require.Contains(t, plan.Ops[1], "materialized")
}

func TestUniquenessConstraint(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"UNIQUENESS", true},
		{"NODE_KEY", true},
		{"NODE_PROPERTY_EXISTENCE", false},
		{"RELATIONSHIP_PROPERTY_EXISTENCE", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			require.Equal(t, tt.want, uniquenessConstraint(tt.typ))
		})
	}
}

func TestExplainDropUnsupported(t *testing.T) {
	_, ok := ExplainDrop()
	require.False(t, ok)
}

func TestExplainPlanBoundedVsScan(t *testing.T) {
	bounded := ExplainPlan(selector.KeySet{Scan: false, Keys: []string{"1", "2"}}, nil, false)
	require.Len(t, bounded.Ops, 1)
	require.Contains(t, bounded.Ops[0], "fetch 2 requested key")

	scan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
	require.Contains(t, scan.Ops[0], "full-label scan")
	require.Nil(t, scan.Filter)
}
