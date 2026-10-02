package query_test

import (
	"context"
	"errors"
	"fmt"
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
		name      string
		opts      query.TransformOptions
		want      []query.Record
		wantErr   string
		wantErrIs error
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
			// The sentinel must survive the wrap: a caller distinguishes "this
			// record has no key" from any other write failure by errors.Is.
			wantErrIs: query.ErrNoKey,
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
				if tt.wantErrIs != nil {
					require.ErrorIs(t, err, tt.wantErrIs)
				}
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

// TestTransformBadFilter pins that a malformed expression is reported with the
// context of which expression failed. A bare gojq parse error says only what the
// syntax problem was, never which of the two expressions carried it, so the
// wrapper is the whole message from the caller's point of view.
func TestTransformBadFilter(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts query.TransformOptions
		want string
	}{
		{"item filter", query.TransformOptions{Filter: "this is not jq ("}, "parse item filter"},
		{"key expression", query.TransformOptions{Key: "this is not jq ("}, "parse --key"},
		// An undefined function parses cleanly and fails at compile instead, which
		// is a separate guard with its own wording; a parse-only check never sees it.
		{"item filter compile", query.TransformOptions{Filter: "nosuchfunc"}, "compile item filter"},
		{"key expression compile", query.TransformOptions{Key: "nosuchfunc"}, "compile --key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := query.NewTransform(tt.opts)

			require.ErrorContains(t, err, tt.want, "the failing expression must be named")
			// Naming the expression is done by wrapping, so gojq's own parse error
			// has to stay reachable underneath; formatting it in instead reads the
			// same but severs errors.Is/As for every caller.
			require.Error(t, errors.Unwrap(err), "the parse error must stay unwrappable")
		})
	}
}

// capturePutter records the batches and modes it is asked to write. stat, when
// set, is the per-batch outcome it reports instead of the default "everything
// written", so a test can drive the counters a Copier totals.
type capturePutter struct {
	batches  [][]query.Record
	mode     query.WriteMode
	err      error
	failFrom int // 1-based batch number err starts at; 0 means from the first
	stat     *query.WriteStat
	gotCtx   context.Context // the ctx Put was handed, so a dropped one is visible
	calls    int
}

func (p *capturePutter) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	p.gotCtx = ctx
	p.calls++
	if p.err != nil && p.calls >= max(p.failFrom, 1) {
		return query.WriteStat{}, p.err
	}
	// Copy the slice: the Copier reuses its buffer across batches.
	b := append([]query.Record(nil), batch...)
	p.batches = append(p.batches, b)
	p.mode = mode
	if p.stat != nil {
		return *p.stat, nil
	}
	return query.WriteStat{Written: len(batch)}, nil
}

// batchSizes reports the length of each batch the putter received, which is what
// distinguishes one page size from another.
func (p *capturePutter) batchSizes() []int {
	out := make([]int, 0, len(p.batches))
	for _, b := range p.batches {
		out = append(out, len(b))
	}
	return out
}

// numberedRecords returns n records keyed k0..k(n-1).
func numberedRecords(n int) []query.Record {
	out := make([]query.Record, 0, n)
	for i := range n {
		out = append(out, query.Record{Key: fmt.Sprintf("k%d", i), Value: i})
	}
	return out
}

// recordsSource turns a fixed slice into a RecordSource paged at size.
func recordsSource(recs []query.Record, size int) query.RecordSource {
	return func(_ context.Context, fn func([]query.Record) error) error {
		for i := 0; i < len(recs); i += size {
			end := min(i+size, len(recs))
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
	require.Empty(t, dst.batches[0][0].Key)
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

// TestCopierTotalsEveryWriteStatField pins that a copy accumulates all three
// counters across pages, not just the one the default putter reports. Overwritten
// and Skipped are what make a non-atomic multi-key write honest, and a copy that
// summed only Written would report the same total as one that dropped them.
func TestCopierTotalsEveryWriteStatField(t *testing.T) {
	dst := &capturePutter{stat: &query.WriteStat{Written: 1, Overwritten: 2, Skipped: 3}}
	c := query.Copier{Dst: dst, PageSize: 1}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(2), 2), false)

	require.NoError(t, err)
	require.Equal(t, []int{1, 1}, dst.batchSizes(), "two pages, so two per-batch stats to fold")
	require.Equal(t, query.WriteStat{Written: 2, Overwritten: 4, Skipped: 6}, stat)
}

// TestCopierPageBoundaries pins the page arithmetic exactly: which page size a
// zero PageSize falls back to, and that a non-zero one is honoured as written.
// Batch counts alone cannot see an off-by-one in the default, so the sizes are
// compared element by element against a record count that straddles it.
func TestCopierPageBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		pageSize int
		records  int
		want     []int
	}{
		// 101 records straddle the default 100: a default of 99 would split 99/2 and
		// one of 101 would send a single batch.
		{name: "zero page size defaults to one hundred", pageSize: 0, records: 101, want: []int{100, 1}},
		// A page size of 1 must flush per record; treating "<= 1" as unset would
		// send one batch of 3 instead.
		{name: "page size one flushes per record", pageSize: 1, records: 3, want: []int{1, 1, 1}},
		// Records that divide evenly must not produce a trailing empty batch: the
		// final flush has nothing buffered and must not reach the store at all.
		{name: "exact multiple sends no empty trailing batch", pageSize: 2, records: 4, want: []int{2, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := &capturePutter{}
			c := query.Copier{Dst: dst, PageSize: tt.pageSize}

			stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(tt.records), tt.records), false)

			require.NoError(t, err)
			require.Equal(t, tt.records, stat.Written)
			require.Equal(t, tt.want, dst.batchSizes())
		})
	}
}

// TestCopierDryRunCountsEveryPage pins that a dry run's count is the real record
// count across several pages. The dry branch keeps its own buffer bookkeeping, so
// a reset that leaves a record behind, or a total that assigns instead of adding,
// reports a plausible but wrong number from a single-page run.
func TestCopierDryRunCountsEveryPage(t *testing.T) {
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 2}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(5), 5), true)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 5}, stat, "every record is counted once, across three pages")
	require.Empty(t, dst.batches, "a dry run writes nothing")
}

// TestCopierStopsOnMidCopyPutError pins that a page flushed inside the read loop
// propagates its error immediately, rather than the copy running on and only the
// final flush being checked.
func TestCopierStopsOnMidCopyPutError(t *testing.T) {
	sentinel := errors.New("put boom")
	dst := &capturePutter{err: sentinel}
	c := query.Copier{Dst: dst, PageSize: 1}

	_, err := c.Copy(context.Background(), recordsSource(numberedRecords(3), 3), false)

	require.ErrorIs(t, err, sentinel)
}

// TestCopierReportsWhatItWroteBeforeFailing pins that a copy interrupted partway
// still returns the stat for the pages that landed. Writes are not atomic across
// keys, so a caller shown a zero total after two of three pages were written has
// been told the destination is untouched when it is not.
func TestCopierReportsWhatItWroteBeforeFailing(t *testing.T) {
	sentinel := errors.New("put boom")
	dst := &capturePutter{err: sentinel, failFrom: 3}
	c := query.Copier{Dst: dst, PageSize: 1}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(3), 3), false)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, query.WriteStat{Written: 2}, stat, "the two pages that landed must be reported")
}

// TestCopierStopsOnTransformError pins that a transform failure stops the copy
// instead of the record being dropped and the walk continuing.
func TestCopierStopsOnTransformError(t *testing.T) {
	sentinel := errors.New("transform boom")
	dst := &capturePutter{}
	c := query.Copier{
		Dst:       dst,
		PageSize:  10,
		Transform: func(query.Record) ([]query.Record, error) { return nil, sentinel },
	}

	_, err := c.Copy(context.Background(), recordsSource(numberedRecords(2), 2), false)

	require.ErrorIs(t, err, sentinel)
	require.Empty(t, dst.batches, "nothing is written once the transform fails")
}

// TestCopierForwardsContext pins that the caller's context reaches both edges of
// a copy — the source it reads through and the putter it writes to — rather than
// either being handed a nil.
func TestCopierForwardsContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxMarker{}, "marker")
	var srcCtx context.Context
	src := func(c context.Context, fn func([]query.Record) error) error {
		srcCtx = c
		return fn(numberedRecords(1))
	}
	dst := &capturePutter{}

	_, err := (&query.Copier{Dst: dst, PageSize: 10}).Copy(ctx, src, false)

	require.NoError(t, err)
	require.NotNil(t, srcCtx, "the source must receive a non-nil context")
	require.Equal(t, "marker", srcCtx.Value(ctxMarker{}), "the caller's context must reach the source")
	require.NotNil(t, dst.gotCtx, "Put must receive a non-nil context")
	require.Equal(t, "marker", dst.gotCtx.Value(ctxMarker{}), "the caller's context must reach Put")
}

// TestTransformWithoutFilterKeepsSourceType pins that a transform that does not
// reshape the value leaves the record's native type alone. Only a reshaping
// filter may restamp it, so a copy that merely re-keys a Redis hash must not
// arrive at the destination as an untyped record.
func TestTransformWithoutFilterKeepsSourceType(t *testing.T) {
	fn, err := query.NewTransform(query.TransformOptions{KeyPrefix: "p:"})
	require.NoError(t, err)

	got, err := fn(query.Record{Key: "k", Type: "hash", Value: map[string]any{"f": "v"}})

	require.NoError(t, err)
	require.Equal(t, []query.Record{{Key: "p:k", Type: "hash", Value: map[string]any{"f": "v"}}}, got)
}

// TestCopierReportsWhatItWroteBeforeTheLastFlushFails fails only the final flush,
// after the first page landed. The copy returns the error and the stat of the
// page that landed.
func TestCopierReportsWhatItWroteBeforeTheLastFlushFails(t *testing.T) {
	sentinel := errors.New("put boom")
	dst := &capturePutter{err: sentinel, failFrom: 2}
	c := query.Copier{Dst: dst, PageSize: 2}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(3), 3), false)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, query.WriteStat{Written: 2}, stat, "the page that landed must be reported")
}

// TestTransformStopsOnAFilterRuntimeError pins that an item filter that fails on
// a value stops the transform. The record is not dropped in silence.
func TestTransformStopsOnAFilterRuntimeError(t *testing.T) {
	tr, err := query.NewTransform(query.TransformOptions{Filter: `error("boom")`})
	require.NoError(t, err)

	out, err := tr(query.Record{Key: "k", Value: 1})

	require.ErrorContains(t, err, "apply item filter")
	require.Error(t, errors.Unwrap(err), "the filter error must stay unwrappable")
	require.Nil(t, out)
}

// modesPutter records the write mode of every page it receives.
type modesPutter struct{ modes []query.WriteMode }

func (p *modesPutter) Put(_ context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	p.modes = append(p.modes, mode)
	return query.WriteStat{Written: len(batch)}, nil
}

// TestCopierPassesTheModeToPut sets InsertOnly, which is not the zero mode. A full
// page and the last page both reach Put with it, so a copy that drops the mode
// fails here, where an Upsert test cannot see it.
func TestCopierPassesTheModeToPut(t *testing.T) {
	dst := &modesPutter{}
	c := query.Copier{Dst: dst, Mode: query.InsertOnly, PageSize: 2}

	_, err := c.Copy(context.Background(), recordsSource(numberedRecords(3), 3), false)

	require.NoError(t, err)
	require.Equal(t, []query.WriteMode{query.InsertOnly, query.InsertOnly}, dst.modes)
}

// fanOut is a transform that emits n records for each source record.
func fanOut(n int) func(query.Record) ([]query.Record, error) {
	return func(r query.Record) ([]query.Record, error) {
		out := make([]query.Record, 0, n)
		for i := range n {
			out = append(out, query.Record{Key: fmt.Sprintf("%s.%d", r.Key, i), Value: r.Value})
		}
		return out, nil
	}
}

// TestCopierDryRunCountsAFannedOutTransform emits two records for each source record.
// A dry run counts both of them, and it sends none to the putter.
func TestCopierDryRunCountsAFannedOutTransform(t *testing.T) {
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 4, Transform: fanOut(2)}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(3), 3), true)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 6}, stat)
	require.Empty(t, dst.batches)
}

// TestCopierPagesAFannedOutTransform emits three records for one source record. The
// page limit counts the records that the transform emits, so the page boundary falls
// inside the output of one source record.
func TestCopierPagesAFannedOutTransform(t *testing.T) {
	dst := &capturePutter{}
	c := query.Copier{Dst: dst, PageSize: 2, Transform: fanOut(3)}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(1), 1), false)

	require.NoError(t, err)
	require.Equal(t, 3, stat.Written)
	require.Equal(t, []int{2, 1}, dst.batchSizes())
}

// statErrPutter reports a non-zero stat together with an error, as a store does when
// part of a page landed before it failed.
type statErrPutter struct {
	stat query.WriteStat
	err  error
}

func (p statErrPutter) Put(context.Context, []query.Record, query.WriteMode) (query.WriteStat, error) {
	return p.stat, p.err
}

// TestCopierAddsTheStatOfAFailedPut fails a page after part of it landed. The copy
// returns the error, and its total holds the stat that the failed Put reported.
func TestCopierAddsTheStatOfAFailedPut(t *testing.T) {
	sentinel := errors.New("put boom")
	dst := statErrPutter{stat: query.WriteStat{Written: 1, Overwritten: 2, Skipped: 3}, err: sentinel}
	c := query.Copier{Dst: dst, PageSize: 2}

	stat, err := c.Copy(context.Background(), recordsSource(numberedRecords(2), 2), false)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, dst.stat, stat)
}

// TestTransformKeyPriority sets the key expression and the key field together, and
// checks which one names the key. The key expression wins over the key field, the key
// field wins over the source key, and the prefix goes on every derived key. A key
// field names a key for each of many outputs, which the source key cannot do.
func TestTransformKeyPriority(t *testing.T) {
	in := query.Record{Key: "src", Type: "hash", Value: map[string]any{"id": "7", "alt": "x"}}
	tests := []struct {
		name string
		opts query.TransformOptions
		want []string
	}{
		{name: "key expression over key field", opts: query.TransformOptions{Key: ".alt", KeyField: "id"}, want: []string{"x"}},
		{name: "key field over source key", opts: query.TransformOptions{KeyField: "id"}, want: []string{"7"}},
		{name: "prefix on a key expression", opts: query.TransformOptions{Key: ".alt", KeyPrefix: "p:"}, want: []string{"p:x"}},
		{name: "prefix on a key field", opts: query.TransformOptions{KeyField: "id", KeyPrefix: "p:"}, want: []string{"p:7"}},
		{name: "key field names each of many outputs", opts: query.TransformOptions{Filter: ".,.", KeyField: "id"}, want: []string{"7", "7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := query.NewTransform(tt.opts)
			require.NoError(t, err)
			got, err := fn(in)
			require.NoError(t, err)
			keys := make([]string, 0, len(got))
			for _, r := range got {
				keys = append(keys, r.Key)
			}
			require.Equal(t, tt.want, keys)
		})
	}
}

// TestTransformEmptyKeyIsNoKey derives an empty key by each route. Each gives the
// ErrNoKey sentinel itself, and the prefix does not turn an empty key into a key.
func TestTransformEmptyKeyIsNoKey(t *testing.T) {
	tests := []struct {
		name  string
		opts  query.TransformOptions
		value any
	}{
		{name: "key expression", opts: query.TransformOptions{Key: `""`, KeyPrefix: "p:"}, value: map[string]any{}},
		{name: "key field", opts: query.TransformOptions{KeyField: "id", KeyPrefix: "p:"}, value: map[string]any{"id": ""}},
		{name: "source key", opts: query.TransformOptions{KeyPrefix: "p:"}, value: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := query.NewTransform(tt.opts)
			require.NoError(t, err)
			_, err = fn(query.Record{Key: "", Value: tt.value})
			require.ErrorIs(t, err, query.ErrNoKey)
			require.EqualError(t, err, query.ErrNoKey.Error())
		})
	}
}
