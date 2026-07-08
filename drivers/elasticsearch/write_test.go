package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestTallyBulk(t *testing.T) {
	t.Run("created is a write, updated is an overwrite", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"index": {Result: "created", Status: 201}},
			{"index": {Result: "updated", Status: 200}},
		}
		stat, err := tallyBulk(items)
		require.NoError(t, err)
		require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
	})

	t.Run("a 409 conflict is a skip", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"create": {Status: 409, Error: &bulkError{Type: "version_conflict_engine_exception", Reason: "exists"}}},
			{"create": {Result: "created", Status: 201}},
		}
		stat, err := tallyBulk(items)
		require.NoError(t, err)
		require.Equal(t, query.WriteStat{Written: 1, Skipped: 1}, stat)
	})

	t.Run("a non-conflict error fails the batch", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"index": {Status: 400, Error: &bulkError{Type: "mapper_parsing_exception", Reason: "bad field"}}},
		}
		_, err := tallyBulk(items)
		require.ErrorContains(t, err, "mapper_parsing_exception")
		require.ErrorContains(t, err, "bad field")
	})
}

func TestDocumentBody(t *testing.T) {
	t.Run("object value keeps its fields", func(t *testing.T) {
		doc, err := documentBody(query.Record{Key: "1", Value: map[string]any{"title": "Dune"}})
		require.NoError(t, err)
		require.Equal(t, map[string]any{"title": "Dune"}, doc)
	})

	t.Run("injected _id is stripped from the source", func(t *testing.T) {
		doc, err := documentBody(query.Record{Key: "1", Value: map[string]any{"_id": "1", "title": "Dune"}})
		require.NoError(t, err)
		require.Equal(t, map[string]any{"title": "Dune"}, doc)
	})

	t.Run("scalar value is rejected, not wrapped", func(t *testing.T) {
		_, err := documentBody(query.Record{Key: "k", Value: "bare-scalar"})
		require.ErrorContains(t, err, "is not a JSON object")
	})

	t.Run("keyless scalar is rejected too", func(t *testing.T) {
		_, err := documentBody(query.Record{Value: 42})
		require.ErrorContains(t, err, "is not a JSON object")
	})
}
