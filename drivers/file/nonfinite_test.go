package file

import (
	"bytes"
	"context"
	"math"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
	"github.com/zsltg/rdb/encoder"
	"github.com/zsltg/rdb/model"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

type noticeCall struct{ key, field string }

// noticeCtx returns a context whose notice sink appends to calls.
func noticeCtx(calls *[]noticeCall) context.Context {
	return query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		*calls = append(*calls, noticeCall{key, field})
	})
}

// readAll runs src and returns every record it yields.
func readAll(t *testing.T, ctx context.Context, src query.RecordSource) []query.Record {
	t.Helper()
	var recs []query.Record
	err := src(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	})
	require.NoError(t, err)
	return recs
}

func TestBSONSourceConvertsNonFinite(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"_id": "k1", "price": math.Inf(1), "n": bson.A{1.5, math.NaN()}})
	require.NoError(t, err)
	var calls []noticeCall

	recs := readAll(t, noticeCtx(&calls), bsonSource(bytes.NewReader(raw), 10, numfmt.DecimalAuto))

	require.Len(t, recs, 1)
	require.Equal(t, map[string]any{"_id": "k1", "price": "Infinity", "n": []any{1.5, "NaN"}}, recs[0].Value)
	require.ElementsMatch(t, []noticeCall{{"k1", "price"}, {"k1", "n[1]"}}, calls)
}

func TestExtJSONSourceConvertsNonFinite(t *testing.T) {
	line := `{"_id":"k2","low":{"$numberDouble":"-Infinity"}}` + "\n"
	var calls []noticeCall

	recs := readAll(t, noticeCtx(&calls), extJSONSource(bytes.NewReader([]byte(line)), 10, numfmt.DecimalAuto))

	require.Len(t, recs, 1)
	require.Equal(t, map[string]any{"_id": "k2", "low": "-Infinity"}, recs[0].Value)
	require.Equal(t, []noticeCall{{"k2", "low"}}, calls)
}

func TestRDBSourceReportsNonFiniteScore(t *testing.T) {
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, 1, 0))
	require.NoError(t, enc.WriteZSetObject("z", []*model.ZSetEntry{
		{Member: "a", Score: 1},
		{Member: "b", Score: math.Inf(1)},
	}))
	require.NoError(t, enc.WriteEnd())
	var calls []noticeCall

	recs := readAll(t, noticeCtx(&calls), rdbSource(&buf, 10))

	require.Len(t, recs, 1)
	require.Equal(t, []any{
		map[string]any{"member": "a", "score": 1.0},
		map[string]any{"member": "b", "score": "inf"},
	}, recs[0].Value)
	require.Equal(t, []noticeCall{{"z", "[1].score"}}, calls)
}

func TestCassandraCSVRowConvertsNonFinite(t *testing.T) {
	layout := csvLayout{
		header:  []string{"id", "price", "rate"},
		keyCols: []string{"id"},
		types:   map[string]gocql.Type{"id": gocql.TypeText, "price": gocql.TypeDouble, "rate": gocql.TypeDouble},
	}
	var calls []noticeCall

	rec, err := recordForCSVRow(noticeCtx(&calls), layout, []string{"r1", "-Infinity", "NaN"}, numfmt.DecimalAuto)

	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": "r1", "price": "-Infinity", "rate": "NaN"}, rec.Value)
	require.ElementsMatch(t, []noticeCall{{rec.Key, "price"}, {rec.Key, "rate"}}, calls)
}
