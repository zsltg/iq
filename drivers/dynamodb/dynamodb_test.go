package dynamodb

import (
	"bytes"
	"context"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// book builds a books-table item with an integer partition key id.
func book(id, year, price int, title string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id":    &types.AttributeValueMemberN{Value: strconv.Itoa(id)},
		"title": &types.AttributeValueMemberS{Value: title},
		"year":  &types.AttributeValueMemberN{Value: strconv.Itoa(year)},
		"price": &types.AttributeValueMemberN{Value: strconv.Itoa(price)},
	}
}

func seedBooks(t *testing.T) *Store {
	ks, attrs := hashKey("id", types.ScalarAttributeTypeN)
	return seedTable(
		t, "books", ks, attrs,
		book(1, 2015, 39, "The Go Programming Language"),
		book(2, 2017, 45, "Designing Data-Intensive Applications"),
		book(3, 2018, 20, "A Philosophy of Software Design"),
	)
}

func TestGetByKey(t *testing.T) {
	st := seedBooks(t)
	got, err := st.Get(context.Background(), []string{"1", "3", "999"})
	require.NoError(t, err)

	require.IsType(t, map[string]any{}, got["1"])
	require.Equal(t, "The Go Programming Language", got["1"].(map[string]any)["title"])
	require.Equal(t, 2018, got["3"].(map[string]any)["year"])
	require.NotContains(t, got, "999") // a missing key is absent from the map
}

func TestGetEmpty(t *testing.T) {
	st := seedBooks(t)
	got, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestScanBatches(t *testing.T) {
	st := seedBooks(t)
	all := map[string]any{}
	batches := 0
	err := st.ScanBatches(context.Background(), func(batch map[string]any) error {
		batches++
		maps.Copy(all, batch)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.ElementsMatch(t, []string{"1", "2", "3"}, keysOf(all))
	// Three items are far under the page size, so a connected store streams them as a
	// single page; a store opened without one would hand over a page per item.
	require.Equal(t, 1, batches)
}

func TestOpenRejectsAMissingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping dynamodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	// The key schema is read at connect time, so a table that does not exist fails
	// Open rather than surfacing later as an empty scan.
	_, err := Open(ctx, testURL(), "no-such-table", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "describe table")
	var notFound *types.ResourceNotFoundException
	require.ErrorAs(t, err, &notFound) // the service's own error stays in the chain
}

func TestOpenAppliesTheDecimalMode(t *testing.T) {
	ks, attrs := hashKey("id", types.ScalarAttributeTypeN)
	seedTable(t, "decimals", ks, attrs, map[string]types.AttributeValue{
		"id":     &types.AttributeValueMemberN{Value: "1"},
		"amount": &types.AttributeValueMemberN{Value: "19.99"},
	})
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		want any
	}{
		{"auto keeps the exact decimal string", numfmt.DecimalAuto, "19.99"},
		{"string mode keeps the exact decimal string", numfmt.DecimalString, "19.99"},
		{"number mode presents a float", numfmt.DecimalNumber, 19.99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			t.Cleanup(cancel)
			st, err := Open(ctx, testURL(), "decimals", nil, tt.mode)
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })

			got, err := st.Get(ctx, []string{"1"})
			require.NoError(t, err)
			require.Equal(t, tt.want, got["1"].(map[string]any)["amount"]) //nolint:testifylint // exact float+type intended: number mode must yield float64.
		})
	}
}

func TestScanFiltered(t *testing.T) {
	st := seedBooks(t)
	pred := predicate.Eq{Path: []string{"year"}, Value: 2017.0}
	got := map[string]any{}
	err := st.ScanFiltered(context.Background(), pred, func(batch map[string]any) error {
		maps.Copy(got, batch)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"2"}, keysOf(got))
}

func TestScanFilteredUnpushableFallsBackToFullScan(t *testing.T) {
	st := seedBooks(t)
	// A range predicate is not pushable, so ScanFiltered must fall back to a full
	// scan (the engine re-runs the jq client-side) rather than dropping rows.
	pred := predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}
	got := map[string]any{}
	err := st.ScanFiltered(context.Background(), pred, func(batch map[string]any) error {
		maps.Copy(got, batch)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestCompositeKey(t *testing.T) {
	ks := []types.KeySchemaElement{
		{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
		{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
	}
	attrs := []types.AttributeDefinition{
		{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
		{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeN},
	}
	item := map[string]types.AttributeValue{
		"pk":   &types.AttributeValueMemberS{Value: "user#1"},
		"sk":   &types.AttributeValueMemberN{Value: "7"},
		"note": &types.AttributeValueMemberS{Value: "hello"},
	}
	st := seedTable(t, "events", ks, attrs, item)

	got, err := st.Get(context.Background(), []string{`["user#1","7"]`})
	require.NoError(t, err)
	require.Equal(t, "hello", got[`["user#1","7"]`].(map[string]any)["note"])
}

func TestQueryPartiQL(t *testing.T) {
	st := seedBooks(t)
	res, err := st.Query(context.Background(), []string{`SELECT * FROM "books" WHERE id = 2`})
	require.NoError(t, err)
	rows, ok := res.([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	require.Equal(t, "Designing Data-Intensive Applications", rows[0].(map[string]any)["title"])
}

func TestEstimateCount(t *testing.T) {
	st := seedBooks(t)
	n, err := st.EstimateCount(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(0)) // ItemCount is a possibly-stale hint
}

func TestNoTableErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping dynamodb integration test in -short mode")
	}
	st := openIntegration(t, "") // raw-only store, no table selected
	_, err := st.Get(context.Background(), []string{"1"})
	require.ErrorIs(t, err, errNoTable)
	err = st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
	require.ErrorIs(t, err, errNoTable)
}

func TestInspect(t *testing.T) {
	st := seedBooks(t)

	tables, err := st.InspectTables(context.Background())
	require.NoError(t, err)
	names := tables.(map[string]any)["tables"].([]any)
	require.Contains(t, names, "books")

	desc, err := st.InspectTable(context.Background())
	require.NoError(t, err)
	m := desc.(map[string]any)
	require.Equal(t, "books", m["name"])
	require.NotEmpty(t, m["keySchema"])
}

func TestTraceLogsOperations(t *testing.T) {
	seedBooks(t) // create + seed the table
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	traced, err := Open(ctx, testURL(), "books", &buf, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = traced.Close() })

	_, err = traced.Get(ctx, []string{"1"})
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "DescribeTable books") // an op that carries a table name
	require.Contains(t, out, "BatchGetItem")        // an op that does not
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
