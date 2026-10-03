package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

func TestParseURLKeepsTheHostWithUserinfo(t *testing.T) {
	// The userinfo must be cut off the authority, or the user name and the password
	// become part of the host name the driver dials.
	cc, err := parseURL("cassandra://alice:s3cret@n1,n2:9043/shop", "")
	require.NoError(t, err)
	require.Equal(t, []string{"n1:9042", "n2:9043"}, cc.hosts)
}

func TestParseURLRejectsBadURLs(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "keyspace path has a second segment", rawURL: "cassandra://h/shop/orders", wantErr: "must name a keyspace"},
		{name: "query does not unescape", rawURL: "cassandra://h/shop?table=%zz", wantErr: "parse cassandra url query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.rawURL, "")
			require.Equal(t, connConfig{}, cc, "a bad URL gives no config")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseURLKeepsTheQueryCauseInTheChain(t *testing.T) {
	_, err := parseURL("cassandra://h/shop?table=%zz", "")
	var escapeErr url.EscapeError
	require.ErrorAs(t, err, &escapeErr, "the cause stays reachable through the wrap")
}

func TestParseConsistencyUnknownKeepsQuorum(t *testing.T) {
	// An unknown level is an error. The level that comes back with it is the default,
	// so a caller that ignores the error still runs at QUORUM and not at ANY.
	got, err := parseConsistency("nope")
	require.Error(t, err)
	require.Equal(t, gocql.Quorum, got)
}

func TestParseColumnTypesEmptyHintGivesAMap(t *testing.T) {
	// An empty hint gives an empty map, not a nil map, so a caller can add to it.
	got, err := ParseColumnTypes("  ")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestColumnTypesIndexesEveryColumn(t *testing.T) {
	meta := tableMetaFor(
		[]*gocql.ColumnMetadata{column("id", gocql.TypeInt)},
		nil,
		[]*gocql.ColumnMetadata{column("title", gocql.TypeText)},
	)
	got := columnTypes(meta)
	require.Len(t, got, 2)
	require.Equal(t, gocql.TypeInt, got["id"].Type())
	require.Equal(t, gocql.TypeText, got["title"].Type())
}

func TestToCQLRefusesWhatItCannotPush(t *testing.T) {
	// Each predicate here would push a wrong or a wider WHERE if one of the guards in
	// the translation were gone. None of them may push.
	eq := func(v string, path ...string) predicate.Eq { return predicate.Eq{Path: path, Value: v} }
	tests := []struct {
		name string
		pred predicate.Node
	}{
		{name: "equality on a nested path", pred: eq("x", "a", "b")},
		{name: "or of a single branch", pred: predicate.Or{eq("x", "a")}},
		{name: "or with a nested branch last", pred: predicate.Or{eq("x", "a"), eq("y", "a", "b")}},
		{name: "or with a nested branch first", pred: predicate.Or{eq("x", "a", "b"), eq("y", "a")}},
		{name: "or with a branch that is not an equality", pred: predicate.Or{predicate.Exists{Path: []string{"a"}}, eq("y", "a")}},
		{name: "or over two columns", pred: predicate.Or{eq("x", "a"), eq("y", "b")}},
		{name: "and of nothing that pushes", pred: predicate.And{predicate.Exists{Path: []string{"a"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args, ok := toCQL(nil, tt.pred)
			require.False(t, ok)
			require.Empty(t, where)
			require.Empty(t, args)
		})
	}
}

func TestToCQLAndSkipsAConjunctThatDoesNotPush(t *testing.T) {
	// A conjunct that does not push is skipped. The ones after it still push.
	pred := predicate.And{
		predicate.Exists{Path: []string{"a"}},
		predicate.Eq{Path: []string{"b"}, Value: "x"},
	}
	where, args, ok := toCQL(nil, pred)
	require.True(t, ok)
	require.Equal(t, `"b" = ?`, where)
	require.Equal(t, []any{"x"}, args)
}

func TestExplainPlanMaterializesAScanThatCannotStream(t *testing.T) {
	plan := ExplainPlan(selector.KeySet{Scan: true}, nil, false)
	require.Equal(t, []string{
		"SELECT *: full-table scan (every row read, filtered client-side)",
		"whole result materialized in memory",
	}, plan.Ops)
}

func TestGetOfNoKeysNeedsNoSession(t *testing.T) {
	// An empty key list answers at once with an empty map. It must not reach the
	// session, so a store with no session shows whether a query went out.
	meta := tableMetaFor([]*gocql.ColumnMetadata{column("id", gocql.TypeInt)}, nil, nil)
	st := &Store{meta: meta}
	got, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestQueryRejectsAnEmptyStatement(t *testing.T) {
	// An empty statement is refused before any session call, so a store with no
	// session shows whether a query went out.
	st := &Store{}
	got, err := st.Query(context.Background(), nil)
	require.Nil(t, got)
	require.ErrorContains(t, err, "empty statement")
}

func TestOpenReportsAnUnreachableCluster(t *testing.T) {
	// A cluster that refuses the connection must fail the open with the cause behind
	// the wrap. An open that goes on hands back a store with no session.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, "cassandra://127.0.0.1:1/ks", "", nil, numfmt.DecimalAuto)
	require.Nil(t, st)
	require.ErrorContains(t, err, "connect cassandra")
	require.Error(t, errors.Unwrap(err), "the cause stays reachable through the wrap")
}

func TestOpenReportsABadURL(t *testing.T) {
	st, err := Open(context.Background(), "redis://h/ks", "", nil, numfmt.DecimalAuto)
	require.Nil(t, st)
	require.ErrorContains(t, err, "must start with cassandra://")
}

func TestDecodeKeyEdgeCases(t *testing.T) {
	t.Run("a table with no primary key", func(t *testing.T) {
		meta := tableMetaFor(nil, nil, nil)
		got, err := decodeKey(meta, `["a"]`)
		require.Nil(t, got)
		require.ErrorContains(t, err, "has no primary key")
	})
	t.Run("a composite key whose part does not parse", func(t *testing.T) {
		meta := tableMetaFor(
			[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
			[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
			nil,
		)
		got, err := decodeKey(meta, `["US","abc"]`)
		require.Nil(t, got)
		require.ErrorContains(t, err, "not an integer")
	})
	t.Run("a composite key that is not JSON keeps the cause", func(t *testing.T) {
		meta := tableMetaFor(
			[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
			[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
			nil,
		)
		_, err := decodeKey(meta, "US,10001")
		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax, "the cause stays reachable through the wrap")
	})
}

func TestBindKeyValueKeepsTheParseCause(t *testing.T) {
	// A key part that does not parse is an error with the parser's own error behind
	// the wrap, so a caller can tell a range fault from a syntax fault.
	tests := []struct {
		name    string
		typ     gocql.Type
		in      string
		wantMsg string
	}{
		{name: "integer", typ: gocql.TypeInt, in: "x", wantMsg: "is not an integer"},
		{name: "bigint", typ: gocql.TypeBigInt, in: "x", wantMsg: "is not a bigint"},
		{name: "float", typ: gocql.TypeFloat, in: "x", wantMsg: "is not a float"},
		{name: "double", typ: gocql.TypeDouble, in: "x", wantMsg: "is not a double"},
		{name: "boolean", typ: gocql.TypeBoolean, in: "x", wantMsg: "is not a boolean"},
		{name: "uuid", typ: gocql.TypeUUID, in: "x", wantMsg: "is not a uuid"},
		{name: "timestamp", typ: gocql.TypeTimestamp, in: "x", wantMsg: "is not a timestamp"},
		{name: "blob", typ: gocql.TypeBlob, in: "!", wantMsg: "is not base64 blob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindKeyValue(tt.typ, tt.in)
			require.Nil(t, got)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Error(t, errors.Unwrap(err), "the cause stays reachable through the wrap")
		})
	}
}

func TestBindKeyValueNumberWidths(t *testing.T) {
	t.Run("bigint takes the full int64 range", func(t *testing.T) {
		got, err := bindKeyValue(gocql.TypeBigInt, strconv.FormatInt(1<<62+(1<<62-1), 10))
		require.NoError(t, err)
		require.Equal(t, int64(1<<62+(1<<62-1)), got)
	})
	t.Run("bigint refuses a value past int64", func(t *testing.T) {
		_, err := bindKeyValue(gocql.TypeBigInt, "9223372036854775808")
		require.ErrorContains(t, err, "is not a bigint")
	})
	t.Run("float refuses a value past float32", func(t *testing.T) {
		_, err := bindKeyValue(gocql.TypeFloat, "1e39")
		require.ErrorContains(t, err, "is not a float")
	})
	t.Run("double takes a value past float32", func(t *testing.T) {
		got, err := bindKeyValue(gocql.TypeDouble, "1e39")
		require.NoError(t, err)
		require.InDelta(t, 1e39, got, 1e25)
	})
}

func TestBindValueReportsAWrongKindOfValue(t *testing.T) {
	// A string-encoded type needs a string. The message must name the wrong value and
	// type, not the parse fault of an empty string.
	for _, typ := range []gocql.Type{gocql.TypeUUID, gocql.TypeTimestamp, gocql.TypeBlob, gocql.TypeInet} {
		t.Run(strconv.Itoa(int(typ)), func(t *testing.T) {
			got, err := bindValue(nativeType(typ), 5)
			require.Nil(t, got)
			require.ErrorContains(t, err, "value 5 (int) is not a")
		})
	}
}

func TestBindValueReportsABadElement(t *testing.T) {
	list := gocql.NewNativeType(4, gocql.TypeCustom, "list<int>")
	m := gocql.NewNativeType(4, gocql.TypeCustom, "map<text,int>")
	t.Run("list", func(t *testing.T) {
		got, err := bindValue(list, []any{1, "x"})
		require.Nil(t, got)
		require.ErrorContains(t, err, "is not an integer")
	})
	t.Run("map", func(t *testing.T) {
		got, err := bindValue(m, map[string]any{"k": "x"})
		require.Nil(t, got)
		require.ErrorContains(t, err, "is not an integer")
	})
}

func TestToIntGivesZeroWithTheError(t *testing.T) {
	got, err := toInt(1.5)
	require.Zero(t, got)
	require.ErrorContains(t, err, "is not a whole number")
}

func TestToIntGivesZeroForAWrongKindOfValue(t *testing.T) {
	got, err := toInt("x")
	require.Zero(t, got)
	require.ErrorContains(t, err, "is not an integer")
}

// gateWriter blocks its first Write until release closes. Every later Write
// signals entered2 and returns.
type gateWriter struct {
	first    atomic.Bool
	entered1 chan struct{}
	entered2 chan struct{}
	release  chan struct{}
}

func (w *gateWriter) Write(p []byte) (int, error) {
	if w.first.CompareAndSwap(false, true) {
		close(w.entered1)
		<-w.release
		return len(p), nil
	}
	select {
	case w.entered2 <- struct{}{}:
	default:
	}
	return len(p), nil
}

func TestQueryObserverSerializesItsWrites(t *testing.T) {
	// The lock must cover the write: while one write is in progress, a second one
	// must wait for it.
	w := &gateWriter{
		entered1: make(chan struct{}),
		entered2: make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	o := newQueryObserver(w)
	q := gocql.ObservedQuery{Statement: "SELECT 1"}
	var wg sync.WaitGroup
	wg.Go(func() { o.ObserveQuery(context.Background(), q) })
	<-w.entered1
	wg.Go(func() { o.ObserveQuery(context.Background(), q) })

	select {
	case <-w.entered2:
		close(w.release)
		wg.Wait()
		t.Fatal("a second write started while the first was in progress")
	case <-time.After(200 * time.Millisecond):
	}
	close(w.release)
	wg.Wait()
}
