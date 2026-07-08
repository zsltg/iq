package dynamodb

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsert(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	// Insert a new key.
	stat, err := st.Put(ctx, []query.Record{{
		Key:   "4",
		Type:  "item",
		Value: map[string]any{"title": "Clean Code", "year": 2008, "price": 35},
	}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)

	got, err := st.Get(ctx, []string{"4"})
	require.NoError(t, err)
	require.Equal(t, "Clean Code", got["4"].(map[string]any)["title"])
	require.Equal(t, 4, got["4"].(map[string]any)["id"]) // key attribute reconstructed from the record key

	// Overwrite the same key: the upsert pre-read reports it as Overwritten, not Written.
	stat, err = st.Put(ctx, []query.Record{{
		Key:   "4",
		Value: map[string]any{"title": "Clean Code (2nd)", "year": 2008},
	}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Overwritten: 1}, stat)
	got, err = st.Get(ctx, []string{"4"})
	require.NoError(t, err)
	require.Equal(t, "Clean Code (2nd)", got["4"].(map[string]any)["title"])

	// A mixed batch splits the count: key 4 already exists (Overwritten), key 6 is new (Written).
	stat, err = st.Put(ctx, []query.Record{
		{Key: "4", Value: map[string]any{"title": "Clean Code (3rd)", "year": 2009}},
		{Key: "6", Value: map[string]any{"title": "SICP", "year": 1985}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
}

func TestPutInsertOnly(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	// Key 1 already exists: InsertOnly must skip it, not overwrite.
	stat, err := st.Put(ctx, []query.Record{{
		Key:   "1",
		Value: map[string]any{"title": "SHOULD NOT WIN", "year": 1},
	}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 0, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "The Go Programming Language", got["1"].(map[string]any)["title"])

	// A new key is written.
	stat, err = st.Put(ctx, []query.Record{{
		Key:   "5",
		Value: map[string]any{"title": "New", "year": 2020},
	}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 0, stat.Skipped)
}

func TestDeleteIntegration(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	// A mixed batch: keys 1 and 3 exist, key 999 never did. BatchWriteItem is silent on
	// absence, so the pre-read supplies the split: two Deleted, one Missing.
	stat, err := st.Delete(ctx, []string{"1", "999", "3"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)

	// The deleted keys are gone; the untouched key survives.
	got, err := st.Get(ctx, []string{"1", "2", "3"})
	require.NoError(t, err)
	require.Nil(t, got["1"])
	require.Nil(t, got["3"])
	require.Equal(t, "Designing Data-Intensive Applications", got["2"].(map[string]any)["title"])

	// Delete is idempotent: re-deleting an already-absent key is Missing, not an error.
	stat, err = st.Delete(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Missing: 1}, stat)
}

func TestDeleteCompositeKeyIntegration(t *testing.T) {
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
	ctx := context.Background()

	// A hash+sort key is addressed by its JSON-array spelling; the existing one deletes.
	stat, err := st.Delete(ctx, []string{`["user#1","7"]`})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 1}, stat)

	got, err := st.Get(ctx, []string{`["user#1","7"]`})
	require.NoError(t, err)
	require.Nil(t, got[`["user#1","7"]`])

	// An absent composite key is Missing, not Deleted.
	stat, err = st.Delete(ctx, []string{`["user#1","8"]`})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Missing: 1}, stat)
}

func TestClear(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	require.NoError(t, st.Clear(ctx))

	remaining := 0
	err := st.ScanBatches(ctx, func(batch map[string]any) error {
		remaining += len(batch)
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, remaining)
}

func TestDrop(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	require.NoError(t, st.Drop(ctx))

	// The table is gone, so a scan now errors.
	err := st.ScanBatches(ctx, func(map[string]any) error { return nil })
	require.Error(t, err)
}

func TestTypedScan(t *testing.T) {
	st := seedBooks(t)
	ctx := context.Background()

	var recs []query.Record
	err := st.TypedScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, recs, 3)
	for _, r := range recs {
		require.Equal(t, "item", r.Type)
		require.NotEmpty(t, r.Key)
	}
}
