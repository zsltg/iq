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

func TestTallyBulkDelete(t *testing.T) {
	t.Run("deleted counts, not_found is missing", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"delete": {Result: "deleted", Status: 200}},
			{"delete": {Result: "not_found", Status: 404}},
			{"delete": {Result: "deleted", Status: 200}},
		}
		stat, err := tallyBulkDelete(items)
		require.NoError(t, err)
		require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)
	})

	t.Run("an errored item fails the batch", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"delete": {Status: 403, Error: &bulkError{Type: "cluster_block_exception", Reason: "read-only"}}},
		}
		_, err := tallyBulkDelete(items)
		require.ErrorContains(t, err, "cluster_block_exception")
		require.ErrorContains(t, err, "read-only")
	})

	t.Run("an unexpected result fails the batch", func(t *testing.T) {
		items := []map[string]bulkItem{
			{"delete": {Result: "noop", Status: 200}},
		}
		_, err := tallyBulkDelete(items)
		require.ErrorContains(t, err, "unexpected result")
		require.ErrorContains(t, err, "noop")
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
