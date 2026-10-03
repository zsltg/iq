package cassandra

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestOpenSetsTheClusterConfig(t *testing.T) {
	// Open must hand the credentials and the deadline of the caller to the driver. The
	// driver keeps them in a cluster config that the session does not expose, so the
	// test reads the exported fields of that config by reflection.
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	rawURL := strings.Replace(testURL(), "cassandra://", "cassandra://alice:s3cret@", 1)
	st, err := Open(ctx, rawURL, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	cfg := reflect.ValueOf(st.session).Elem().FieldByName("cfg")
	require.True(t, cfg.IsValid(), "the driver session keeps a cluster config")
	require.False(t, cfg.FieldByName("Authenticator").IsNil(), "a URL with a user name sets the authenticator")
	for _, name := range []string{"ConnectTimeout", "Timeout"} {
		d := time.Duration(cfg.FieldByName(name).Int())
		require.Greater(t, d, 30*time.Second, "%s follows the deadline of the caller", name)
		require.LessOrEqual(t, d, 45*time.Second, "%s follows the deadline of the caller", name)
	}
}

func TestOpenWithoutUserLeavesTheAuthenticatorEmpty(t *testing.T) {
	st := schemaStore(t)
	cfg := reflect.ValueOf(st.session).Elem().FieldByName("cfg")
	require.True(t, cfg.FieldByName("Authenticator").IsNil())
}

func TestGetRejectsAKeyThatDoesNotDecode(t *testing.T) {
	books := seedTable(t, "bad_key_books", "CREATE TABLE bad_key_books (id int PRIMARY KEY, title text)")
	sales := seedTable(t, "bad_key_sales",
		"CREATE TABLE bad_key_sales (country text, id int, amount int, PRIMARY KEY (country, id))")

	t.Run("single key column", func(t *testing.T) {
		got, err := books.Get(context.Background(), []string{"abc"})
		require.Nil(t, got)
		require.ErrorContains(t, err, "is not an integer")
	})
	t.Run("composite key", func(t *testing.T) {
		got, err := sales.Get(context.Background(), []string{`["US","abc"]`})
		require.Nil(t, got)
		require.ErrorContains(t, err, "is not an integer")
	})
}

func TestPutRejectsARecordThatDoesNotFitTheTable(t *testing.T) {
	st := seedTable(t, "bad_record", "CREATE TABLE bad_record (id int PRIMARY KEY, title text)")
	tests := []struct {
		name    string
		mode    query.WriteMode
		record  query.Record
		wantErr string
	}{
		{"value is not an object", query.Upsert, query.Record{Key: "1", Value: "scalar"}, "must be a row object"},
		{"value is not an object on insert only", query.InsertOnly, query.Record{Key: "1", Value: "scalar"}, "must be a row object"},
		{"key does not decode", query.Upsert, query.Record{Key: "abc", Value: map[string]any{"title": "x"}}, "is not an integer"},
		{"key does not decode on insert only", query.InsertOnly, query.Record{Key: "abc", Value: map[string]any{"title": "x"}}, "is not an integer"},
		{"column value has the wrong type", query.Upsert, query.Record{Key: "1", Value: map[string]any{"title": 5}}, "is not a"},
		{"column is unknown", query.Upsert, query.Record{Key: "1", Value: map[string]any{"nope": "x"}}, "unknown column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat, err := st.Put(context.Background(), []query.Record{tt.record}, tt.mode)
			require.Equal(t, query.WriteStat{}, stat)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
