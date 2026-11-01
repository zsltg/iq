package query_test

import (
	"context"
	"errors"
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

// eventPutter logs the Put calls and the BeforeWrite calls in order.
type eventPutter struct{ events *[]string }

func (p eventPutter) Put(_ context.Context, b []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	*p.events = append(*p.events, "put")
	return query.WriteStat{Written: len(b)}, nil
}

func TestCopierBeforeWrite(t *testing.T) {
	twoPages := func(_ context.Context, fn func([]query.Record) error) error {
		for _, k := range []string{"a", "b"} {
			if err := fn([]query.Record{{Key: k, Value: map[string]any{}}}); err != nil {
				return err
			}
		}
		return nil
	}
	empty := func(context.Context, func([]query.Record) error) error { return nil }
	tests := []struct {
		name    string
		src     query.RecordSource
		dry     bool
		wantErr bool
		want    []string
	}{
		{"once before the first put", twoPages, false, false, []string{"before", "put", "put"}},
		{"at the end of an empty source", empty, false, false, []string{"before"}},
		{"never in a dry run", twoPages, true, false, nil},
		{"never in a dry run of an empty source", empty, true, false, nil},
		{"not when the first page is refused", nativeSource(query.FormDynamoJSON), false, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			c := query.Copier{
				Dst: eventPutter{events: &events}, PageSize: 1,
				BeforeWrite: func(context.Context) error { events = append(events, "before"); return nil },
			}
			_, err := c.Copy(context.Background(), tt.src, tt.dry)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, events)
		})
	}
}

func TestCopierBeforeWriteErrorStopsTheCopy(t *testing.T) {
	var events []string
	boom := errors.New("boom")
	c := query.Copier{
		Dst:         eventPutter{events: &events},
		BeforeWrite: func(context.Context) error { return boom },
	}
	_, err := c.Copy(context.Background(), nativeSource(query.FormJSON), false)
	require.ErrorIs(t, err, boom)
	require.Empty(t, events, "no Put runs after the hook fails")
}

type hookCtxKey struct{}

func TestCopierBeforeWriteGetsTheCallerContext(t *testing.T) {
	onePage := nativeSource(query.FormJSON)
	empty := func(context.Context, func([]query.Record) error) error { return nil }
	tests := []struct {
		name string
		src  query.RecordSource
	}{
		{"first page", onePage},
		{"empty source", empty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), hookCtxKey{}, "marker")
			var got any
			c := query.Copier{
				Dst: &countPutter{},
				BeforeWrite: func(hctx context.Context) error {
					got = hctx.Value(hookCtxKey{})
					return nil
				},
			}
			_, err := c.Copy(ctx, tt.src, false)
			require.NoError(t, err)
			require.Equal(t, "marker", got)
		})
	}
}

func TestCopierBeforeWriteErrorOnAnEmptySource(t *testing.T) {
	boom := errors.New("boom")
	c := query.Copier{
		Dst:         &countPutter{},
		BeforeWrite: func(context.Context) error { return boom },
	}
	empty := func(context.Context, func([]query.Record) error) error { return nil }
	_, err := c.Copy(context.Background(), empty, false)
	require.ErrorIs(t, err, boom)
}
