package cassandra

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// canceledContext returns a context that is already canceled.
func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// traceRecorder is a trace writer that keeps every line and can cancel a context when
// a line holds a marker. The observer calls it after a statement ran, so a test can
// let one statement pass and fail the next one.
type traceRecorder struct {
	mu       sync.Mutex
	lines    []string
	marker   string
	onMarker context.CancelFunc
}

func (r *traceRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := string(p)
	r.lines = append(r.lines, line)
	if r.onMarker != nil && strings.Contains(line, r.marker) {
		r.onMarker()
	}
	return len(p), nil
}

func (r *traceRecorder) count(marker string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, marker) {
			n++
		}
	}
	return n
}

// openTraced opens a table-scoped store that traces every statement to rec.
func openTraced(t *testing.T, table string, rec *traceRecorder) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL(), table, rec, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCanceledContextStopsEveryOperation(t *testing.T) {
	// A canceled context must stop each operation and reach the caller with the cause.
	// An operation that drops the context runs to the end and reports success.
	books := seedTable(t, "cancel_books",
		"CREATE TABLE cancel_books (id int PRIMARY KEY, title text)",
		"INSERT INTO cancel_books (id, title) VALUES (1, 'Hobbit')")
	sales := seedTable(t, "cancel_sales",
		"CREATE TABLE cancel_sales (country text, id int, amount int, PRIMARY KEY (country, id))",
		"INSERT INTO cancel_sales (country, id, amount) VALUES ('US', 1, 100)")
	keyed := []query.Record{{Key: "1", Value: map[string]any{"title": "Dune"}}}
	keyless := []query.Record{{Value: map[string]any{"id": 2, "title": "Dune"}}}

	tests := []struct {
		name    string
		wantMsg string
		run     func(ctx context.Context) error
	}{
		{"get single key", "cassandra select", func(ctx context.Context) error {
			_, err := books.Get(ctx, []string{"1"})
			return err
		}},
		{"get composite key", "cassandra select", func(ctx context.Context) error {
			_, err := sales.Get(ctx, []string{`["US","1"]`})
			return err
		}},
		{"scan", "cassandra scan", func(ctx context.Context) error {
			return books.ScanBatches(ctx, func(map[string]any) error { return nil })
		}},
		{"filtered scan", "cassandra scan", func(ctx context.Context) error {
			pred := predicate.Eq{Path: []string{"title"}, Value: "Hobbit"}
			return books.ScanFiltered(ctx, pred, func(map[string]any) error { return nil })
		}},
		{"typed scan", "cassandra scan", func(ctx context.Context) error {
			return books.TypedScan(ctx, func([]query.Record) error { return nil })
		}},
		{"raw query", "cassandra:", func(ctx context.Context) error {
			_, err := books.Query(ctx, []string{"SELECT * FROM cancel_books"})
			return err
		}},
		{"upsert reads the existing keys", "cassandra select", func(ctx context.Context) error {
			_, err := books.Put(ctx, keyed, query.Upsert)
			return err
		}},
		{"upsert write", "cassandra insert", func(ctx context.Context) error {
			_, err := books.Put(ctx, keyless, query.Upsert)
			return err
		}},
		{"insert only write", "cassandra insert", func(ctx context.Context) error {
			_, err := books.Put(ctx, keyed, query.InsertOnly)
			return err
		}},
		{"delete reads the existing keys", "cassandra select", func(ctx context.Context) error {
			_, err := books.Delete(ctx, []string{"1"})
			return err
		}},
		{"clear", "cassandra truncate", books.Clear},
		{"drop", "cassandra drop", books.Drop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(canceledContext(t))
			require.ErrorContains(t, err, tt.wantMsg)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
	t.Run("the data is intact", func(t *testing.T) {
		got, err := books.Get(context.Background(), []string{"1"})
		require.NoError(t, err)
		require.Contains(t, got, "1", "no canceled operation changed the table")
	})
}

func TestDeleteStopsWhenTheContextEndsAfterTheRead(t *testing.T) {
	// The read of the existing keys runs, then the context ends, then the DELETE must
	// fail with the cause. A DELETE that drops the context removes the row.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	rec := &traceRecorder{marker: "SELECT * FROM", onMarker: cancel}
	seedTable(t, "cancel_delete",
		"CREATE TABLE cancel_delete (id int PRIMARY KEY, title text)",
		"INSERT INTO cancel_delete (id, title) VALUES (1, 'Hobbit')")
	st := openTraced(t, "cancel_delete", rec)

	stat, err := st.Delete(ctx, []string{"1"})
	require.Equal(t, query.DeleteStat{}, stat)
	require.ErrorContains(t, err, "cassandra delete")
	require.ErrorIs(t, err, context.Canceled)

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Contains(t, got, "1", "the row is still there")
}

func TestOpenAppliesTheConsistencyLevel(t *testing.T) {
	// The level in the URL must reach the session. A session without it runs every
	// statement at the default level.
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL()+"?consistency=one", "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.Equal(t, gocql.One, st.session.Query("SELECT now() FROM system.local").GetConsistency())
}

func TestCloseClosesTheSession(t *testing.T) {
	st := schemaStore(t)
	require.False(t, st.session.Closed())
	require.NoError(t, st.Close())
	require.True(t, st.session.Closed(), "Close releases the session")
}

func TestOpenReleasesTheSessionWhenTheTableIsMissing(t *testing.T) {
	// An Open that fails after it made a session must close that session. A session
	// that stays open keeps its goroutines and its connections for the life of the
	// process.
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	open := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		st, err := Open(ctx, testURL(), "no_such_table_zzz", nil, numfmt.DecimalAuto)
		if st != nil {
			_ = st.Close()
		}
		return err
	}
	// Warm up once, so the shared one-time goroutines of the driver are not counted.
	require.ErrorContains(t, open(), `table "no_such_table_zzz" not found`)
	time.Sleep(time.Second)
	before := runtime.NumGoroutine()
	for range 3 {
		require.ErrorContains(t, open(), `table "no_such_table_zzz" not found`)
	}
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+3 },
		15*time.Second, 100*time.Millisecond, "a failed Open leaves no session behind")
}

func TestTableMetaReportsAMissingKeyspace(t *testing.T) {
	st := schemaStore(t)
	meta, err := tableMeta(st.session, "no_such_keyspace_zzz", "t")
	require.Nil(t, meta)
	require.ErrorContains(t, err, "cassandra keyspace metadata")
	require.Error(t, errors.Unwrap(err), "the cause stays reachable through the wrap")
}

func TestGetOfOneKeyColumnUsesOneStatement(t *testing.T) {
	// A single-column key is read with one WHERE ... IN statement for all keys, not a
	// point query per key.
	rec := &traceRecorder{}
	seedTable(t, "one_statement",
		"CREATE TABLE one_statement (id int PRIMARY KEY, title text)",
		"INSERT INTO one_statement (id, title) VALUES (1, 'a')",
		"INSERT INTO one_statement (id, title) VALUES (2, 'b')")
	st := openTraced(t, "one_statement", rec)

	got, err := st.Get(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, 1, rec.count(`FROM "iq_test"."one_statement"`))
}

func TestGetOfNoKeysSendsNoStatement(t *testing.T) {
	rec := &traceRecorder{}
	seedTable(t, "no_statement", "CREATE TABLE no_statement (id int PRIMARY KEY, title text)")
	st := openTraced(t, "no_statement", rec)

	got, err := st.Get(context.Background(), []string{})
	require.NoError(t, err)
	require.Empty(t, got)
	require.Zero(t, rec.count(`FROM "iq_test"."no_statement"`))
}

func TestScanStopsAtTheCallersRefusal(t *testing.T) {
	st := seedTable(t, "refusal",
		"CREATE TABLE refusal (id int PRIMARY KEY, v text)",
		"INSERT INTO refusal (id, v) VALUES (1, 'a')",
		"INSERT INTO refusal (id, v) VALUES (2, 'b')",
		"INSERT INTO refusal (id, v) VALUES (3, 'c')")
	st.pageSize = 1

	want := context.DeadlineExceeded
	calls := 0
	err := st.ScanBatches(context.Background(), func(map[string]any) error { calls++; return want })
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, calls, "the scan stops at the first refusal")
}

func TestQueryReportsABadStatement(t *testing.T) {
	st := schemaStore(t)
	got, err := st.Query(context.Background(), []string{"SELEC nothing"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "cassandra:")
	require.Error(t, errors.Unwrap(err), "the cause stays reachable through the wrap")
}

func TestQueryReturnsEveryRowAsItsOwnMap(t *testing.T) {
	st := seedTable(t, "many_rows",
		"CREATE TABLE many_rows (id int PRIMARY KEY, title text, tags set<text>, amount decimal)",
		"INSERT INTO many_rows (id, title, tags, amount) VALUES (1, 'a', {'x'}, 1.5)",
		"INSERT INTO many_rows (id, title) VALUES (2, null)",
		"INSERT INTO many_rows (id, title, tags, amount) VALUES (3, 'c', {'y', 'z'}, 2.5)")

	res, err := st.Query(context.Background(), []string{"SELECT * FROM many_rows"})
	require.NoError(t, err)
	rows, ok := res.([]any)
	require.True(t, ok)
	require.ElementsMatch(t, []any{
		map[string]any{"id": 1, "title": "a", "tags": []any{"x"}, "amount": "1.5"},
		map[string]any{"id": 2, "title": "", "tags": []any{}, "amount": nil},
		map[string]any{"id": 3, "title": "c", "tags": []any{"y", "z"}, "amount": "2.5"},
	}, rows)
}

func TestPutKeyBeatsThePrimaryKeyInTheValue(t *testing.T) {
	// The record key is the source of the primary key. A primary-key field in the value
	// must not replace it.
	st := seedTable(t, "key_wins", "CREATE TABLE key_wins (id int PRIMARY KEY, title text)")

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "7", Value: map[string]any{"id": 99, "title": "KeyWins"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got, err := st.Get(context.Background(), []string{"7", "99"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"7": map[string]any{"id": 7, "title": "KeyWins"}}, got)
}

func TestPutNeedsEveryPrimaryKeyColumn(t *testing.T) {
	// A record with no key must carry all primary-key columns in its value.
	sales := seedTable(t, "needs_pk",
		"CREATE TABLE needs_pk (country text, id int, amount int, PRIMARY KEY (country, id))")
	tests := []struct {
		name  string
		value map[string]any
	}{
		{name: "none of them", value: map[string]any{"amount": 5}},
		{name: "the clustering column is missing", value: map[string]any{"country": "US", "amount": 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat, err := sales.Put(context.Background(), []query.Record{{Value: tt.value}}, query.Upsert)
			require.Equal(t, query.WriteStat{}, stat)
			require.ErrorContains(t, err, "record missing primary-key column")
		})
	}
}
