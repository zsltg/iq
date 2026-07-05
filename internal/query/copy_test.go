package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestNewTransformIdentity(t *testing.T) {
	// The zero options request no transformation, so no gojq is built.
	fn, err := query.NewTransform(query.TransformOptions{})
	require.NoError(t, err)
	require.Nil(t, fn)
}

func TestTransformKeying(t *testing.T) {
	in := query.Record{Key: "book:1", Type: "hash", Value: map[string]any{
		"title": "Dune", "year": 1965, "tags": []any{"sf", "classic"},
	}}
	tests := []struct {
		name    string
		opts    query.TransformOptions
		want    []query.Record
		wantErr string
	}{
		{
			name: "select keeps whole item and source key",
			opts: query.TransformOptions{Filter: "select(.year > 1900)"},
			want: []query.Record{{Key: "book:1", Type: "", Value: in.Value}},
		},
		{
			name: "select that rejects yields nothing",
			opts: query.TransformOptions{Filter: "select(.year > 2000)"},
			want: []query.Record{},
		},
		{
			name: "reshape 1:1 inherits source key, stamps type",
			opts: query.TransformOptions{Filter: "{t: .title}", Type: "json"},
			want: []query.Record{{Key: "book:1", Type: "json", Value: map[string]any{"t": "Dune"}}},
		},
		{
			name: "reshape with key expression",
			opts: query.TransformOptions{Filter: "{name: .title, y: .year}", Key: ".y", Type: "json"},
			want: []query.Record{{Key: "1965", Type: "json", Value: map[string]any{"name": "Dune", "y": 1965}}},
		},
		{
			name: "key prefix applied",
			opts: query.TransformOptions{Filter: ".title", KeyPrefix: "t:"},
			want: []query.Record{{Key: "t:book:1", Type: "", Value: "Dune"}},
		},
		{
			name:    "explosion without key fails fast",
			opts:    query.TransformOptions{Filter: ".tags[]"},
			wantErr: "multiple values",
		},
		{
			name: "explosion with key expression",
			opts: query.TransformOptions{Filter: ".tags[]", Key: "."},
			want: []query.Record{
				{Key: "sf", Type: "", Value: "sf"},
				{Key: "classic", Type: "", Value: "classic"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := query.NewTransform(tt.opts)
			require.NoError(t, err)
			got, err := fn(in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTransformKeyField(t *testing.T) {
	fn, err := query.NewTransform(query.TransformOptions{KeyField: "id"})
	require.NoError(t, err)
	got, err := fn(query.Record{Value: map[string]any{"id": "7", "title": "Dune"}})
	require.NoError(t, err)
	require.Equal(t, []query.Record{{Key: "7", Value: map[string]any{"id": "7", "title": "Dune"}}}, got)
}

func TestTransformBadFilter(t *testing.T) {
	_, err := query.NewTransform(query.TransformOptions{Filter: "this is not jq ("})
	require.Error(t, err)
}

// capturePutter records the batches and modes it is asked to write.
type capturePutter struct {
	batches [][]query.Record
	mode    query.WriteMode
	err     error
}

func (p *capturePutter) Put(_ context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if p.err != nil {
		return query.WriteStat{}, p.err
	}
	// Copy the slice: the Copier reuses its buffer across batches.
	b := append([]query.Record(nil), batch...)
	p.batches = append(p.batches, b)
	p.mode = mode
	return query.WriteStat{Written: len(batch)}, nil
}

// recordsSource turns a fixed slice into a RecordSource paged at size.
func recordsSource(recs []query.Record, size int) query.RecordSource {
	return func(_ context.Context, fn func([]query.Record) error) error {
		for i := 0; i < len(recs); i += size {
			end := i + size
			if end > len(recs) {
				end = len(recs)
			}
			if err := fn(recs[i:end]); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestCopierStreamsInPages(t *testing.T) {
	recs := []query.Record{
		{Key: "a", Value: 1},
		{Key: "b", Value: 2},
		{Key: "c", Value: 3},
		{Key: "d", Value: 4},
		{Key: "e", Value: 5},
	}
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, Mode: query.Upsert, PageSize: 2}
	stat, err := c.Copy(context.Background(), recordsSource(recs, 3), false)
	require.NoError(t, err)
	require.Equal(t, 5, stat.Written)
	// Page size 2 splits five records into batches of 2,2,1.
	require.Len(t, dst.batches, 3)
	require.Len(t, dst.batches[0], 2)
	require.Len(t, dst.batches[2], 1)
	require.Equal(t, query.Upsert, dst.mode)
}

func TestCopierDryRunWritesNothing(t *testing.T) {
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 10}
	stat, err := c.Copy(context.Background(), recordsSource([]query.Record{{Key: "a", Value: 1}}, 10), true)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Empty(t, dst.batches)
}

func TestCopierAppliesTransform(t *testing.T) {
	fn, err := query.NewTransform(query.TransformOptions{Filter: "select(.keep)"})
	require.NoError(t, err)
	recs := []query.Record{
		{Key: "a", Value: map[string]any{"keep": true}},
		{Key: "b", Value: map[string]any{"keep": false}},
	}
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 10, Transform: fn}
	stat, err := c.Copy(context.Background(), recordsSource(recs, 10), false)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, "a", dst.batches[0][0].Key)
}

func TestCopierPassesKeylessRecordToPutter(t *testing.T) {
	// The Copier does not reject a keyless record: the destination adapter decides
	// (Mongo mints an _id, Redis errors), so it reaches Put unchanged.
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 10}
	stat, err := c.Copy(context.Background(), recordsSource([]query.Record{{Value: 1}}, 10), false)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, "", dst.batches[0][0].Key)
}

func TestCopierDefaultPageSize(t *testing.T) {
	// A zero PageSize defaults to a large page, so three records arrive as one batch
	// — distinguishing the <= 0 default from an off-by-one that would flush per item.
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 0}
	recs := []query.Record{{Key: "a", Value: 1}, {Key: "b", Value: 2}, {Key: "c", Value: 3}}
	stat, err := c.Copy(context.Background(), recordsSource(recs, 3), false)
	require.NoError(t, err)
	require.Equal(t, 3, stat.Written)
	require.Len(t, dst.batches, 1)
}

func TestTransformKeyScalarTypes(t *testing.T) {
	tests := []struct {
		name    string
		keyExpr string
		value   any
		want    string
	}{
		{name: "int", keyExpr: ".n", value: map[string]any{"n": 7}, want: "7"},
		{name: "float", keyExpr: ".f", value: map[string]any{"f": 3.5}, want: "3.5"},
		{name: "bool", keyExpr: ".b", value: map[string]any{"b": true}, want: "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := query.NewTransform(query.TransformOptions{Filter: ".", Key: tt.keyExpr})
			require.NoError(t, err)
			got, err := fn(query.Record{Key: "src", Value: tt.value})
			require.NoError(t, err)
			require.Equal(t, tt.want, got[0].Key)
		})
	}
}

func TestTransformKeyErrors(t *testing.T) {
	tests := []struct {
		name    string
		opts    query.TransformOptions
		value   any
		wantErr string
	}{
		{
			name:    "key expression yields multiple values",
			opts:    query.TransformOptions{Filter: ".", Key: ".xs[]"},
			value:   map[string]any{"xs": []any{"a", "b"}},
			wantErr: "more than one value",
		},
		{
			name:    "key expression yields a non-scalar",
			opts:    query.TransformOptions{Filter: ".", Key: ".obj"},
			value:   map[string]any{"obj": map[string]any{"a": 1}},
			wantErr: "scalar",
		},
		{
			name:    "key-field absent",
			opts:    query.TransformOptions{KeyField: "id"},
			value:   map[string]any{"name": "x"},
			wantErr: "field absent",
		},
		{
			name:    "key-field on non-object",
			opts:    query.TransformOptions{KeyField: "id"},
			value:   "scalar",
			wantErr: "not an object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := query.NewTransform(tt.opts)
			require.NoError(t, err)
			_, err = fn(query.Record{Key: "src", Value: tt.value})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCopierPropagatesPutError(t *testing.T) {
	sentinel := errors.New("put boom")
	dst := &capturePutter{err: sentinel}
	c := query.Copier{Dst: dst, PageSize: 10}
	_, err := c.Copy(context.Background(), recordsSource([]query.Record{{Key: "a", Value: 1}}, 10), false)
	require.ErrorIs(t, err, sentinel)
}
