package cassandra

import (
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// columnMap rebuilds the name to value map from the two aligned slices that
// columnsFor returns.
func columnMap(t *testing.T, cols []string, vals []any) map[string]any {
	t.Helper()
	require.Len(t, vals, len(cols))
	out := make(map[string]any, len(cols))
	for i, c := range cols {
		out[c] = vals[i]
	}
	require.Len(t, out, len(cols), "a column name repeats")
	return out
}

func TestColumnsFor(t *testing.T) {
	single := tableMetaFor(
		[]*gocql.ColumnMetadata{column("id", gocql.TypeInt)},
		nil,
		[]*gocql.ColumnMetadata{column("title", gocql.TypeText), column("n", gocql.TypeInt)},
	)
	composite := tableMetaFor(
		[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
		[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
		[]*gocql.ColumnMetadata{column("city", gocql.TypeText)},
	)
	tests := []struct {
		name    string
		meta    *gocql.TableMetadata
		rec     query.Record
		want    map[string]any
		wantErr string
	}{
		{
			name: "key columns come from the key and the rest from the object",
			meta: single,
			rec:  query.Record{Key: "7", Value: map[string]any{"title": "x", "n": float64(3)}},
			want: map[string]any{"id": 7, "title": "x", "n": 3},
		},
		{
			name: "the key wins over a primary-key column in the object",
			meta: single,
			rec:  query.Record{Key: "7", Value: map[string]any{"id": float64(99), "title": "x"}},
			want: map[string]any{"id": 7, "title": "x"},
		},
		{
			name: "a key with an empty object gives only the key columns",
			meta: single,
			rec:  query.Record{Key: "7", Value: map[string]any{}},
			want: map[string]any{"id": 7},
		},
		{
			name: "a keyless record carries its primary key in the object",
			meta: single,
			rec:  query.Record{Value: map[string]any{"id": float64(5), "title": "x"}},
			want: map[string]any{"id": 5, "title": "x"},
		},
		{
			name: "a composite key fills both key columns",
			meta: composite,
			rec:  query.Record{Key: `["US","10001"]`, Value: map[string]any{"city": "NYC"}},
			want: map[string]any{"country": "US", "zip": 10001, "city": "NYC"},
		},
		{
			name:    "a keyless record without its primary key",
			meta:    single,
			rec:     query.Record{Value: map[string]any{"title": "x"}},
			wantErr: `cassandra: record missing primary-key column "id"`,
		},
		{
			name:    "a keyless composite record names the first missing column in schema order",
			meta:    composite,
			rec:     query.Record{Value: map[string]any{"city": "NYC"}},
			wantErr: `cassandra: record missing primary-key column "country"`,
		},
		{
			name:    "a keyless composite record missing only the clustering column",
			meta:    composite,
			rec:     query.Record{Value: map[string]any{"country": "US"}},
			wantErr: `cassandra: record missing primary-key column "zip"`,
		},
		{
			name:    "an unknown column",
			meta:    single,
			rec:     query.Record{Key: "7", Value: map[string]any{"nope": 1}},
			wantErr: `cassandra: unknown column "nope" in table "t"`,
		},
		{
			name:    "a value that does not fit its column",
			meta:    single,
			rec:     query.Record{Key: "7", Value: map[string]any{"n": "x"}},
			wantErr: "cassandra: x (string) is not an integer",
		},
		{
			name:    "a key that does not decode",
			meta:    single,
			rec:     query.Record{Key: "abc", Value: map[string]any{}},
			wantErr: `cassandra: key "abc" is not an integer: strconv.Atoi: parsing "abc": invalid syntax`,
		},
		{
			name:    "a string value",
			meta:    single,
			rec:     query.Record{Key: "7", Value: "x"},
			wantErr: "cassandra: record value must be a row object, got string",
		},
		{
			name:    "a nil value",
			meta:    single,
			rec:     query.Record{Key: "7"},
			wantErr: "cassandra: record value must be a row object, got <nil>",
		},
		{
			name:    "an array value",
			meta:    single,
			rec:     query.Record{Key: "7", Value: []any{}},
			wantErr: "cassandra: record value must be a row object, got []interface {}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{meta: tt.meta, keyspace: "ks", table: "t"}
			cols, vals, err := s.columnsFor(tt.rec)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, cols)
				require.Nil(t, vals)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, columnMap(t, cols, vals))
		})
	}
}
