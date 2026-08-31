package hbase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// errBackend is the failure a fake surfaces so a test can follow it back out through
// the driver's wrapping.
var errBackend = errors.New("region server unavailable")

// rowRecord is one upsert-shaped record, the smallest valid write.
func rowRecord(key string) query.Record {
	return query.Record{Key: key, Type: "row", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}
}

// TestBackendFailuresSurfaceWrapped drives every guarded RPC to failure and requires
// the driver to report it, wrapped with the operation that failed and still unwrapping
// to the backend's own error. A cleared guard would return success instead.
func TestBackendFailuresSurfaceWrapped(t *testing.T) {
	tests := []struct {
		name string
		fail func(fc *fakeClient, fa *fakeAdmin)
		run  func(st *Store) error
		want string
	}{
		{
			name: "get",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.getErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Get(context.Background(), []string{"1"})
				return err
			},
			want: "hbase get",
		},
		{
			name: "scan",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.scanErr = errBackend },
			run: func(st *Store) error {
				return st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
			},
			want: "hbase scan",
		},
		{
			name: "filtered scan",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.scanErr = errBackend },
			run: func(st *Store) error {
				return st.ScanFiltered(context.Background(), nil, func(map[string]any) error { return nil })
			},
			want: "hbase scan",
		},
		{
			name: "exec get",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.getErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Query(context.Background(), []string{"get", "books", "1"})
				return err
			},
			want: "hbase get",
		},
		{
			name: "exec scan",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.scanErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Query(context.Background(), []string{"scan", "books"})
				return err
			},
			want: "hbase scan",
		},
		{
			name: "exec count",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.scanErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Query(context.Background(), []string{"count", "books"})
				return err
			},
			want: "hbase count",
		},
		{
			name: "exec put",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.putErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Query(context.Background(), []string{"put", "books", "1", "cf:title", "Dune"})
				return err
			},
			want: "hbase put",
		},
		{
			name: "exec delete",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.delErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Query(context.Background(), []string{"delete", "books", "1"})
				return err
			},
			want: "hbase delete",
		},
		{
			name: "upsert write",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.putErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Put(context.Background(), []query.Record{rowRecord("1")}, query.Upsert)
				return err
			},
			want: "hbase put",
		},
		{
			name: "insert-only pre-read is skipped, so the check-and-put failure surfaces",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.getErr = errBackend },
			run: func(st *Store) error {
				// InsertOnly makes no pre-read, so a failing Get must not reach it.
				_, err := st.Put(context.Background(), []query.Record{rowRecord("1")}, query.InsertOnly)
				return err
			},
			want: "",
		},
		{
			name: "delete pre-read",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.getErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Delete(context.Background(), []string{"1"})
				return err
			},
			want: "hbase exists",
		},
		{
			name: "delete write",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.delErr = errBackend },
			run: func(st *Store) error {
				_, err := st.Delete(context.Background(), []string{"1"})
				return err
			},
			want: "hbase delete",
		},
		{
			name: "clear scan",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.scanErr = errBackend },
			run:  func(st *Store) error { return st.Clear(context.Background()) },
			want: "hbase clear scan",
		},
		{
			name: "clear delete",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fc.delErr = errBackend },
			run:  func(st *Store) error { return st.Clear(context.Background()) },
			want: "hbase clear delete",
		},
		{
			name: "drop disable",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fa.disableErr = errBackend },
			run:  func(st *Store) error { return st.Drop(context.Background()) },
			want: "hbase disable table",
		},
		{
			name: "drop delete",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fa.deleteErr = errBackend },
			run:  func(st *Store) error { return st.Drop(context.Background()) },
			want: "hbase delete table",
		},
		{
			name: "table listing",
			fail: func(fc *fakeClient, fa *fakeAdmin) { fa.listErr = errBackend },
			run: func(st *Store) error {
				_, err := st.InspectTables(context.Background())
				return err
			},
			want: "hbase list tables",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			fc.seed("1", cell("cf", "title", "Dune"))
			fa := &fakeAdmin{}
			tt.fail(fc, fa)
			st := newFakeStore(fc, fa, "books", typeMap{}, ctAuto)
			err := tt.run(st)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
			// The message anchors at our boundary and the cause is still reachable.
			require.ErrorIs(t, err, errBackend)
		})
	}
}

// TestUnencodableRowKeyStopsBeforeAnyRequest requires every key-taking path to reject
// a key that cannot be encoded for the declared row-key type, before it issues a call.
func TestUnencodableRowKeyStopsBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		run  func(st *Store) error
	}{
		{"get", func(st *Store) error {
			_, err := st.Get(context.Background(), []string{"not-a-number"})
			return err
		}},
		{"put", func(st *Store) error {
			_, err := st.Put(context.Background(), []query.Record{rowRecord("not-a-number")}, query.Upsert)
			return err
		}},
		{"insert-only put", func(st *Store) error {
			_, err := st.Put(context.Background(), []query.Record{rowRecord("not-a-number")}, query.InsertOnly)
			return err
		}},
		{"delete", func(st *Store) error {
			_, err := st.Delete(context.Background(), []string{"not-a-number"})
			return err
		}},
		{"exec get", func(st *Store) error {
			_, err := st.Query(context.Background(), []string{"get", "books", "not-a-number"})
			return err
		}},
		{"exec put", func(st *Store) error {
			_, err := st.Query(context.Background(), []string{"put", "books", "not-a-number", "cf:title", "Dune"})
			return err
		}},
		{"exec delete", func(st *Store) error {
			_, err := st.Query(context.Background(), []string{"delete", "books", "not-a-number"})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctLong)
			err := tt.run(st)
			require.ErrorContains(t, err, "is not an integer for an int/long column")
			// The bad key aborts before the driver touches the cluster.
			require.Empty(t, fc.gets)
			require.Empty(t, fc.puts)
			require.Empty(t, fc.dels)
			require.Empty(t, fc.cas)
		})
	}
}

func TestGuardCell(t *testing.T) {
	tests := []struct {
		name          string
		values        map[string]map[string][]byte
		wantFamily    string
		wantQualifier string
		wantOK        bool
	}{
		{
			name:   "no families",
			values: map[string]map[string][]byte{},
			wantOK: false,
		},
		{
			name:   "every family empty",
			values: map[string]map[string][]byte{"a": {}, "b": {}, "c": {}},
			wantOK: false,
		},
		{
			name: "the smallest family wins",
			values: map[string]map[string][]byte{
				"delta": {"z": nil}, "alpha": {"z": nil}, "charlie": {"z": nil},
				"bravo": {"z": nil}, "echo": {"z": nil}, "foxtrot": {"z": nil},
			},
			wantFamily: "alpha", wantQualifier: "z", wantOK: true,
		},
		{
			name: "the smallest qualifier of that family wins",
			values: map[string]map[string][]byte{
				"cf": {"zulu": nil, "alpha": nil, "mike": nil, "bravo": nil, "yankee": nil, "delta": nil},
			},
			wantFamily: "cf", wantQualifier: "alpha", wantOK: true,
		},
		{
			name: "an empty family is skipped, not chosen",
			values: map[string]map[string][]byte{
				"aaa": {}, "bbb": {"q": nil},
			},
			wantFamily: "bbb", wantQualifier: "q", wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Go randomises map iteration order, so repeat enough that landing on the
			// sorted answer by chance is not a plausible explanation for a pass.
			for range 30 {
				family, qualifier, ok := guardCell(tt.values)
				require.Equal(t, tt.wantOK, ok)
				require.Equal(t, tt.wantFamily, family)
				require.Equal(t, tt.wantQualifier, qualifier)
			}
		})
	}
}
