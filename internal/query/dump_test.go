package query_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	require.Equal(t, 3.5, got[0].Value) //nolint:testifylint // exact float+type intended: JSONL decodes 3.5 to float64.
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

func TestJSONSourceArray(t *testing.T) {
	src := query.JSONSource(strings.NewReader(`[{"key":"a","type":"string","value":"x"},{"key":"b","type":"hash","value":{"f":"v"}}]`), 10, false)
	recs := drainSource(t, src)
	require.Len(t, recs, 2)
	require.Equal(t, query.Record{Key: "a", Type: "string", Value: "x"}, recs[0])
	require.Equal(t, "hash", recs[1].Type)
	require.Equal(t, map[string]any{"f": "v"}, recs[1].Value)
}

func TestJSONSourceConcatenated(t *testing.T) {
	src := query.JSONSource(strings.NewReader("{\"key\":\"a\",\"type\":\"string\",\"value\":1}\n{\"key\":\"b\",\"type\":\"string\",\"value\":2}\n"), 10, false)
	recs := drainSource(t, src)
	require.Len(t, recs, 2)
	require.Equal(t, 1, recs[0].Value)
	require.Equal(t, "b", recs[1].Key)
}

func TestJSONSourcePlainArray(t *testing.T) {
	src := query.JSONSource(strings.NewReader("[10, 20, 30]"), 10, true)
	recs := drainSource(t, src)
	require.Len(t, recs, 3)
	require.Equal(t, 20, recs[1].Value)
	require.Empty(t, recs[1].Key)
}

func TestYAMLSourceRoundTrip(t *testing.T) {
	y := "key: a\ntype: string\nvalue: hi\n---\nkey: b\ntype: hash\nvalue:\n  f: v\n"
	src := query.YAMLSource(strings.NewReader(y), 10, false)
	recs := drainSource(t, src)
	require.Len(t, recs, 2)
	require.Equal(t, query.Record{Key: "a", Type: "string", Value: "hi"}, recs[0])
	require.Equal(t, map[string]any{"f": "v"}, recs[1].Value)
}

// errReader fails after handing out its payload, so a source's read error is
// reachable without a filesystem.
type errReader struct {
	data string
	err  error
	done bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	n := copy(p, r.data)
	return n, nil
}

// TestWriteJSONLReportsEncodeFailure pins that a value the encoder cannot render
// stops the dump with the record named, rather than a truncated file that looks
// complete.
func TestWriteJSONLReportsEncodeFailure(t *testing.T) {
	var buf bytes.Buffer
	err := query.WriteJSONL(&buf, []query.Record{{Key: "k", Value: make(chan int)}})
	require.Error(t, err)
	require.ErrorContains(t, err, `encode dump record "k"`)
	var ute *json.UnsupportedTypeError
	require.ErrorAs(t, err, &ute, "the encoder's cause must stay reachable")
}

// TestJSONLSourcePageBoundaries pins the page arithmetic exactly. Counting
// batches over five lines cannot see an off-by-one in the default page size, so
// the sizes are compared against a line count that straddles it.
func TestJSONLSourcePageBoundaries(t *testing.T) {
	line := func(i int) string { return fmt.Sprintf(`{"key":"k%d","type":"string","value":"v"}`, i) }
	lines := func(n int) string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, line(i))
		}
		return strings.Join(out, "\n")
	}
	tests := []struct {
		name     string
		pageSize int
		count    int
		want     []int
	}{
		// 101 lines straddle the default 100: a default of 99 would split 99/2 and
		// one of 101 would deliver a single page.
		{name: "zero page size defaults to one hundred", pageSize: 0, count: 101, want: []int{100, 1}},
		// A page size of 1 must deliver per line; treating "<= 1" as unset would
		// hand over one page of three.
		{name: "page size one delivers per line", pageSize: 1, count: 3, want: []int{1, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			err := query.JSONLSource(strings.NewReader(lines(tt.count)), tt.pageSize, false)(
				context.Background(), func(b []query.Record) error {
					got = append(got, len(b))
					return nil
				},
			)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestJSONLSourceLineCap pins the scanner's line budget, which is a deliberate
// choice on both sides: it is raised well above bufio's 64 KiB default so a large
// document reloads, and it is still a cap, so a runaway line is refused rather
// than read into memory unbounded.
func TestJSONLSourceLineCap(t *testing.T) {
	record := func(payload int) string {
		return `{"key":"k","type":"string","value":"` + strings.Repeat("a", payload) + `"}`
	}
	t.Run("a line above bufio's default still decodes", func(t *testing.T) {
		got := drainSource(t, query.JSONLSource(strings.NewReader(record(70<<10)), 10, false))
		require.Len(t, got, 1)
		require.Equal(t, "k", got[0].Key)
	})
	t.Run("a line just under the cap decodes", func(t *testing.T) {
		got := drainSource(t, query.JSONLSource(strings.NewReader(record(15<<20+512<<10)), 10, false))
		require.Len(t, got, 1)
	})
	t.Run("a line over the cap is refused", func(t *testing.T) {
		_, err := drainSourceErr(query.JSONLSource(strings.NewReader(record(16<<20+512<<10)), 10, false))
		require.ErrorContains(t, err, "read jsonl")
		require.ErrorContains(t, err, "token too long")
	})
}

// TestJSONLSourceStopsOnContextCancellation pins that a cancelled context ends
// the walk instead of the whole file being decoded regardless.
func TestJSONLSourceStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := query.JSONLSource(strings.NewReader(`{"key":"k","type":"string","value":"v"}`), 10, false)(
		ctx, func([]query.Record) error {
			t.Fatal("no page may be delivered after cancellation")
			return nil
		},
	)
	require.ErrorIs(t, err, context.Canceled)
}

// TestJSONLSourcePropagatesPageError pins that a consumer's refusal stops the
// walk at that page rather than the rest of the file being read anyway.
func TestJSONLSourcePropagatesPageError(t *testing.T) {
	sentinel := errors.New("page boom")
	input := `{"key":"a","type":"string","value":1}` + "\n" + `{"key":"b","type":"string","value":2}`
	pages := 0
	err := query.JSONLSource(strings.NewReader(input), 1, false)(context.Background(), func([]query.Record) error {
		pages++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, pages, "the walk stops at the refusing page")
}

// TestJSONLSourcePropagatesReadError pins that a reader failure surfaces as an
// error, not as a short dump that looks like the end of the file.
func TestJSONLSourcePropagatesReadError(t *testing.T) {
	sentinel := errors.New("read boom")
	r := &errReader{data: `{"key":"a","type":"string","value":1}` + "\n", err: sentinel}
	_, err := drainSourceErr(query.JSONLSource(r, 10, false))
	require.ErrorContains(t, err, "read jsonl")
	require.ErrorIs(t, err, sentinel)
}

// TestJSONLSourceMalformedLineNamesTheEnvelope pins which failure a malformed
// typed line reports. Both the decode failure and the missing-key check produce
// an error mentioning the line, so a substring assertion on the line number alone
// cannot tell a dropped decode guard from a working one.
func TestJSONLSourceMalformedLineNamesTheEnvelope(t *testing.T) {
	input := `{"key":"a","type":"string","value":"x"}` + "\n" + `not json`
	_, err := drainSourceErr(query.JSONLSource(strings.NewReader(input), 10, false))
	require.ErrorContains(t, err, "line 2")
	require.ErrorContains(t, err, "expected a {key,type,value} record")
	require.ErrorContains(t, err, "--key-field")
	// The decoder's own complaint has to stay reachable under both wraps.
	var syn *json.SyntaxError
	require.ErrorAs(t, err, &syn, "the decode error must stay unwrappable")
}

// TestJSONLSourcePlainRejectsMalformedValue pins that plain mode reports a
// malformed value rather than passing a nil record on as if the line were empty.
func TestJSONLSourcePlainRejectsMalformedValue(t *testing.T) {
	_, err := drainSourceErr(query.JSONLSource(strings.NewReader(`{"a":`), 10, true))
	require.ErrorContains(t, err, "line 1")
	require.ErrorContains(t, err, "decode json")
	// The decoder's own complaint has to stay reachable under both wraps.
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "the decode error must stay unwrappable")
}

// TestConvertNumbersWalksEveryEntry pins that number conversion reaches every
// element of a composite, not just the first: a walk that stopped early would
// leave the rest as json.Number, which no consumer of the normalized shape
// expects.
func TestConvertNumbersWalksEveryEntry(t *testing.T) {
	t.Run("every key of an object", func(t *testing.T) {
		got := drainSource(t, query.JSONLSource(strings.NewReader(`{"a":1,"b":2,"c":3,"d":4,"e":5}`), 10, true))
		require.Len(t, got, 1)
		require.Equal(t, map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}, got[0].Value)
	})
	t.Run("every element of an array", func(t *testing.T) {
		got := drainSource(t, query.JSONLSource(strings.NewReader(`[1,2,3,4,5]`), 10, true))
		require.Len(t, got, 1)
		require.Equal(t, []any{1, 2, 3, 4, 5}, got[0].Value)
	})
}

// TestJSONSourceRejectsTrailingGarbage pins that a stream of concatenated values
// is decoded to its end and refuses what it cannot read, rather than stopping
// quietly at the first byte that does not begin a value.
func TestJSONSourceRejectsTrailingGarbage(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		// ']' is where the decoder's own "is there more?" probe says no, so a walk
		// that trusted that probe outside array mode would stop here and report
		// success on a truncated read.
		{name: "stray closing bracket", input: `{"key":"a","type":"string","value":1}]`},
		{name: "unparsable trailer", input: `{"key":"a","type":"string","value":1} @@@`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := drainSourceErr(query.JSONSource(strings.NewReader(tt.input), 10, false))
			require.ErrorContains(t, err, "decode json record")
			// Naming the step is done by wrapping, so the decoder's own error has
			// to stay reachable underneath.
			var syn *json.SyntaxError
			require.ErrorAs(t, err, &syn, "the decode error must stay unwrappable")
		})
	}
}

// TestJSONSourceReadsASingleByteDocument pins that the array probe peeks exactly
// one byte: a whole document can be one byte long, and peeking further would read
// past the end and report an empty stream.
func TestJSONSourceReadsASingleByteDocument(t *testing.T) {
	got := drainSource(t, query.JSONSource(strings.NewReader("1"), 10, true))
	require.Equal(t, []query.Record{{Value: 1}}, got)
}
