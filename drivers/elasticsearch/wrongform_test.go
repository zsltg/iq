package elasticsearch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestPutRefusesANativeForm proves that Put refuses a record in a native form
// before it touches the backend. The store has no connection, so any other path
// fails with a different error.
func TestPutRefusesANativeForm(t *testing.T) {
	tests := []struct {
		name string
		form query.Form
	}{
		{"dynamodb json", query.FormDynamoJSON},
		{"mongo ejson", query.FormMongoEJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := []query.Record{
				{Key: "plain", Value: map[string]any{"a": 1}},
				{Key: "typed", Form: tt.form, Value: map[string]any{"a": map[string]any{"N": "1"}}},
			}
			stat, err := (&Store{}).Put(context.Background(), batch, query.Upsert)
			require.ErrorContains(t, err, "elasticsearch: record \"typed\" has form \""+tt.form.String()+"\"")
			require.ErrorContains(t, err, "accepts only form \"json\"")
			require.Equal(t, query.WriteStat{}, stat)
		})
	}
}
