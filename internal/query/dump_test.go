package query_test

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestJSONLSourcePaging(t *testing.T) {
	lines := make([]string, 5)
	for i := range lines {
		lines[i] = fmt.Sprintf(`{"key":"k%d","type":"string","value":"v"}`, i)
	}
	tests := []struct {
		pageSize    int
		wantBatches int
	}{
		{pageSize: 2, wantBatches: 3}, // 2, 2, 1
		{pageSize: 5, wantBatches: 1},
		{pageSize: 0, wantBatches: 1}, // 0 defaults to a large page
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("page=%d", tt.pageSize), func(t *testing.T) {
			src := query.JSONLSource(strings.NewReader(strings.Join(lines, "\n")), tt.pageSize, false)
			batches := 0
			err := src(context.Background(), func([]query.Record) error {
				batches++
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, tt.wantBatches, batches)
		})
	}
}

func TestJSONLSourceDecodesFloat(t *testing.T) {
	got := drainSource(t, query.JSONLSource(strings.NewReader(`{"key":"k","type":"string","value":3.5}`), 10, false))
	require.Len(t, got, 1)
	require.Equal(t, 3.5, got[0].Value)
}

func TestJSONLSourceLineNumberInError(t *testing.T) {
	input := `{"key":"a","type":"string","value":"x"}` + "\n" + `not json`
	_, err := drainSourceErr(query.JSONLSource(strings.NewReader(input), 10, false))
	require.ErrorContains(t, err, "line 2")
}

func TestWriteJSONLRoundTrip(t *testing.T) {
	recs := []query.Record{
		{Key: "book:1", Type: "hash", Value: map[string]any{"title": "Dune"}},
		{Key: "book:2", Type: "string", Value: "hello"},
		{Key: "n", Type: "string", Value: 42},
	}
	var buf bytes.Buffer
	require.NoError(t, query.WriteJSONL(&buf, recs))
	// One JSON object per record, newline-terminated.
	require.Equal(t, 3, strings.Count(buf.String(), "\n"))

	got := drainSource(t, query.JSONLSource(&buf, 2, false))
	require.Equal(t, recs, got)
}

func TestJSONLSourceTyped(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []query.Record
		wantErr string
	}{
		{
			name:  "envelope",
			input: `{"key":"k1","type":"list","value":[1,2]}`,
			want:  []query.Record{{Key: "k1", Type: "list", Value: []any{1, 2}}},
		},
		{
			name:  "blank lines skipped",
			input: "\n  \n" + `{"key":"k1","type":"string","value":"v"}` + "\n",
			want:  []query.Record{{Key: "k1", Type: "string", Value: "v"}},
		},
		{
			name:    "bare value rejected in typed mode",
			input:   `{"title":"Dune"}`,
			wantErr: "no key",
		},
		{
			name:    "missing key rejected",
			input:   `{"type":"string","value":"v"}`,
			wantErr: "no key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := query.JSONLSource(strings.NewReader(tt.input), 10, false)
			got, err := drainSourceErr(src)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestJSONLSourcePlain(t *testing.T) {
	input := `{"id":"7","title":"Dune"}` + "\n" + `{"id":"8","title":"Emma"}`
	got := drainSource(t, query.JSONLSource(strings.NewReader(input), 10, true))
	require.Equal(t, []query.Record{
		{Value: map[string]any{"id": "7", "title": "Dune"}},
		{Value: map[string]any{"id": "8", "title": "Emma"}},
	}, got)
}

func TestJSONLSourcePreservesBigInt(t *testing.T) {
	big1 := "123456789012345678901234567890"
	input := `{"key":"k","type":"string","value":` + big1 + `}`
	got := drainSource(t, query.JSONLSource(strings.NewReader(input), 10, false))
	require.Len(t, got, 1)
	want, _ := new(big.Int).SetString(big1, 10)
	require.Equal(t, want, got[0].Value)
}

// drainSource drains a RecordSource, failing the test on error.
func drainSource(t *testing.T, src query.RecordSource) []query.Record {
	t.Helper()
	got, err := drainSourceErr(src)
	require.NoError(t, err)
	return got
}

// drainSourceErr drains a RecordSource, returning the accumulated records and error.
func drainSourceErr(src query.RecordSource) ([]query.Record, error) {
	var got []query.Record
	err := src(context.Background(), func(batch []query.Record) error {
		got = append(got, batch...)
		return nil
	})
	return got, err
}
