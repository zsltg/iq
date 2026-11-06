package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestPutRefusesAForeignForm proves that Put refuses a record in a native form
// that it does not own before it touches the backend. The store has no
// connection, so any other path fails with a different error.
func TestPutRefusesAForeignForm(t *testing.T) {
	batch := []query.Record{
		{Key: "plain", Value: map[string]any{"a": 1}},
		{Key: "typed", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"N": "1"}}},
	}
	stat, err := (&Store{}).Put(context.Background(), batch, query.Upsert)
	require.ErrorContains(t, err, `mongo: record "typed" has form "dynamodb-json"`)
	require.ErrorContains(t, err, `accepts only forms "json" and "mongo-ejson"`)
	require.Equal(t, query.WriteStat{}, stat)
}

// TestPutRefusesAMalformedNativeValue proves that a bad tag fails the whole
// batch before any write.
func TestPutRefusesAMalformedNativeValue(t *testing.T) {
	s := lazyStore(t)
	batch := []query.Record{
		{Key: "bad", Form: query.FormMongoEJSON, Value: map[string]any{"n": map[string]any{"$numberInt": "x"}}},
	}
	for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
		_, err := s.Put(context.Background(), batch, mode)
		require.ErrorContains(t, err, `key "bad"`)
	}
}
