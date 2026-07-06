package file

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestCassandraCSVParity(t *testing.T) {
	// A cqlsh COPY dump with a header, single uuid primary key, mixed column types.
	csv := "id,name,age,active,ts\n" +
		"11111111-1111-1111-1111-111111111111,Alice,30,true,2021-01-02T03:04:05Z\n" +
		"22222222-2222-2222-2222-222222222222,Bob,,false,\n"
	u := writeDump(t, "books.csv", []byte(csv),
		"format=cassandra-csv&keys=id&types=id=uuid,age=int,active=boolean,ts=timestamp")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatCassandraCSV, st.format)

	recs := collect(t, st)
	require.Equal(t, query.Record{
		Key:  "11111111-1111-1111-1111-111111111111",
		Type: "row",
		Value: map[string]any{
			"id": "11111111-1111-1111-1111-111111111111", "name": "Alice",
			"age": 30, "active": true, "ts": "2021-01-02T03:04:05Z",
		},
	}, recs["11111111-1111-1111-1111-111111111111"])
	// An empty field is CQL null; the row still decodes.
	require.Equal(t, map[string]any{
		"id": "22222222-2222-2222-2222-222222222222", "name": "Bob",
		"age": nil, "active": false, "ts": nil,
	}, recs["22222222-2222-2222-2222-222222222222"].Value)
}

func TestCassandraCSVCompositeKey(t *testing.T) {
	csv := "part,clust,v\np1,5,hello\np1,10,world\n"
	u := writeDump(t, "c.csv", []byte(csv), "format=cassandra-csv&keys=part,clust&types=clust=int")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	// A composite key is a JSON array of the key columns in schema order.
	require.Equal(t, query.Record{Key: `["p1","5"]`, Type: "row", Value: map[string]any{
		"part": "p1", "clust": 5, "v": "hello",
	}}, recs[`["p1","5"]`])
	require.Equal(t, map[string]any{"part": "p1", "clust": 10, "v": "world"}, recs[`["p1","10"]`].Value)
}

func TestCassandraCSVHeaderless(t *testing.T) {
	// A dump exported without HEADER=TRUE; ?columns= names the columns in order.
	csv := "x1,7\nx2,8\n"
	u := writeDump(t, "h.csv", []byte(csv), "format=cassandra-csv&keys=id&columns=id,age&types=age=int")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	require.Equal(t, map[string]any{"id": "x1", "age": 7}, recs["x1"].Value)
	require.Equal(t, map[string]any{"id": "x2", "age": 8}, recs["x2"].Value)
}

func TestCassandraCSVRequiresKeys(t *testing.T) {
	u := writeDump(t, "c.csv", []byte("id\na\n"), "format=cassandra-csv")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "key schema")
}

func TestCassandraCSVBadType(t *testing.T) {
	u := writeDump(t, "c.csv", []byte("id,n\na,x\n"), "format=cassandra-csv&keys=id&types=n=int")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "not an integer")
}

func TestCassandraCSVFieldCountMismatch(t *testing.T) {
	// A ragged row (fewer fields than the header) is a clear error, not a silent
	// short row; recordForCSVRow validates each row's width against the header.
	u := writeDump(t, "c.csv", []byte("id,name,age\na,b\n"), "format=cassandra-csv&keys=id")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "field(s), header has")
}

func TestPagingCassandra(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("id\n")
	for i := 0; i < bigCount; i++ {
		fmt.Fprintf(&buf, "k%d\n", i)
	}
	u := writeDump(t, "big.csv", buf.Bytes(), "format=cassandra-csv&keys=id")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

func TestPagingExactMultipleCassandra(t *testing.T) {
	// Exactly one page of rows: the trailing partial-page flush must not fire, so a
	// perfect multiple yields a single page with no empty tail.
	var buf bytes.Buffer
	buf.WriteString("id\n")
	for i := 0; i < pageSize; i++ {
		fmt.Fprintf(&buf, "k%d\n", i)
	}
	u := writeDump(t, "exact.csv", buf.Bytes(), "format=cassandra-csv&keys=id")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize}, pageSizes(t, st))
}

func TestCassandraFormatString(t *testing.T) {
	require.Equal(t, "cassandra-csv", FormatCassandraCSV.String())
	f, err := ParseFormat("cql")
	require.NoError(t, err)
	require.Equal(t, FormatCassandraCSV, f)
}
