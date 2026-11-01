package query_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// countPutter counts Put calls.
type countPutter struct{ puts int }

func (p *countPutter) Put(_ context.Context, b []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	p.puts++
	return query.WriteStat{Written: len(b)}, nil
}

// nativePutter is a countPutter that owns the DynamoDB form.
type nativePutter struct{ countPutter }

func (*nativePutter) WritesForm() query.Form { return query.FormDynamoJSON }

func nativeSource(forms ...query.Form) query.RecordSource {
	return func(_ context.Context, fn func([]query.Record) error) error {
		batch := make([]query.Record, len(forms))
		for i, f := range forms {
			batch[i] = query.Record{Key: "k", Form: f, Value: map[string]any{}}
		}
		return fn(batch)
	}
}

func TestWriterForm(t *testing.T) {
	require.Equal(t, query.FormJSON, query.WriterForm(&countPutter{}))
	require.Equal(t, query.FormDynamoJSON, query.WriterForm(&nativePutter{}))
}

func TestCopierChecksTheFormBeforeAnyPut(t *testing.T) {
	tests := []struct {
		name     string
		dst      interface{ query.Putter }
		forms    []query.Form
		dry      bool
		wantErr  bool
		wantPuts int
	}{
		{"real run refuses", &countPutter{}, []query.Form{query.FormDynamoJSON}, false, true, 0},
		{"dry run refuses", &countPutter{}, []query.Form{query.FormDynamoJSON}, true, true, 0},
		{"mixed batch refuses", &countPutter{}, []query.Form{query.FormJSON, query.FormDynamoJSON}, false, true, 0},
		{"owner accepts", &nativePutter{}, []query.Form{query.FormDynamoJSON}, false, false, 1},
		{"json passes", &countPutter{}, []query.Form{query.FormJSON}, false, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := query.Copier{Dst: tt.dst}
			_, err := c.Copy(context.Background(), nativeSource(tt.forms...), tt.dry)
			if tt.wantErr {
				require.ErrorContains(t, err, `destination: record "k" has form "dynamodb-json"`)
			} else {
				require.NoError(t, err)
			}
			switch p := tt.dst.(type) {
			case *countPutter:
				require.Equal(t, tt.wantPuts, p.puts)
			case *nativePutter:
				require.Equal(t, tt.wantPuts, p.puts)
			}
		})
	}
}

func TestCopierAnyFormSkipsTheCheck(t *testing.T) {
	p := &countPutter{}
	c := query.Copier{Dst: p, AnyForm: true}
	_, err := c.Copy(context.Background(), nativeSource(query.FormMongoEJSON), false)
	require.NoError(t, err)
	require.Equal(t, 1, p.puts)
}
