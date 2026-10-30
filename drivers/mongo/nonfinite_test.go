package mongo

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestDocumentConvertsNonFinite(t *testing.T) {
	doc := bson.M{
		"_id":  "k1",
		"nan":  math.NaN(),
		"up":   math.Inf(1),
		"ok":   1.5,
		"deep": bson.M{"down": math.Inf(-1)},
		"list": bson.A{1.0, math.NaN()},
		"d":    bson.D{{Key: "x", Value: math.Inf(1)}},
	}
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})

	key, val := Document(ctx, doc, numfmt.DecimalAuto)

	require.Equal(t, "k1", key)
	require.Equal(t, map[string]any{
		"_id":  "k1",
		"nan":  "NaN",
		"up":   "Infinity",
		"ok":   1.5,
		"deep": map[string]any{"down": "-Infinity"},
		"list": []any{1.0, "NaN"},
		"d":    map[string]any{"x": "Infinity"},
	}, val)
	require.ElementsMatch(t, []call{
		{"k1", "nan"}, {"k1", "up"}, {"k1", "deep.down"}, {"k1", "list[1]"}, {"k1", "d.x"},
	}, got)
}

func TestDocumentWithoutNonFiniteIsQuiet(t *testing.T) {
	called := false
	ctx := query.WithNonFiniteNotice(context.Background(), func(string, string) { called = true })

	_, val := Document(ctx, bson.M{"_id": "k", "a": 1.5}, numfmt.DecimalAuto)

	require.Equal(t, map[string]any{"_id": "k", "a": 1.5}, val)
	require.False(t, called)
}
