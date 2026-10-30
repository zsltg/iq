package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestOpenPipeStreamsTheFirstRead(t *testing.T) {
	st, err := OpenPipe(strings.NewReader(pipeDump), FormatUnknown, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)
	require.Len(t, collect(t, st), 2)
}

// TestOpenPipeReadsOnce confirms that every kind of read fails with
// ErrPipeReadOnce after the first read took the stream.
func TestOpenPipeReadsOnce(t *testing.T) {
	ctx := context.Background()
	reads := map[string]func(*Store) error{
		"Get": func(st *Store) error {
			_, err := st.Get(ctx, []string{"a"})
			return err
		},
		"TypedScan": func(st *Store) error {
			return st.TypedScan(ctx, func([]query.Record) error { return nil })
		},
		"ScanBatches": func(st *Store) error {
			return st.ScanBatches(ctx, func(map[string]any) error { return nil })
		},
		"ScanFiltered": func(st *Store) error {
			return st.ScanFiltered(ctx, matchEverything, func(map[string]any) error { return nil })
		},
	}
	for first, read1 := range reads {
		for second, read2 := range reads {
			t.Run(first+" then "+second, func(t *testing.T) {
				st, err := OpenPipe(strings.NewReader(pipeDump), FormatJSONL, numfmt.DecimalAuto)
				require.NoError(t, err)
				require.NoError(t, read1(st))
				err = read2(st)
				require.ErrorIs(t, err, ErrPipeReadOnce)
				require.ErrorContains(t, err, "save it to a file")
			})
		}
	}
}

// TestOpenPipeDoesNotBufferTheInput confirms that the first page reaches the
// caller while most of the input is still unread.
func TestOpenPipeDoesNotBufferTheInput(t *testing.T) {
	in := &slowDump{total: 20 * pageSize}
	st, err := OpenPipe(in, FormatUnknown, numfmt.DecimalAuto)
	require.NoError(t, err)
	errStop := errors.New("stop after the first page")
	err = st.ScanBatches(context.Background(), func(map[string]any) error { return errStop })
	require.ErrorIs(t, err, errStop)
	require.Less(t, in.next, 2*pageSize, "the scan read far past the first page")
}

// TestOpenPipeDetectsFromThePeek confirms that the peek finds the format of each
// sniffable family, and that a forced format wins.
func TestOpenPipeDetectsFromThePeek(t *testing.T) {
	typed := `{"key":"a","type":"string","value":"x"}` + "\n"
	tests := []struct {
		name   string
		data   []byte
		forced Format
		want   Format
	}{
		{name: "typed JSONL", data: []byte(typed), want: FormatJSONL},
		{name: "typed array", data: []byte("[" + strings.TrimSpace(typed) + "]"), want: FormatJSONL},
		{name: "typed YAML", data: []byte("key: a\ntype: string\nvalue: x\n"), want: FormatYAML},
		{name: "mongoexport", data: []byte(`{"_id":{"$oid":"507f1f77bcf86cd799439011"},"n":1}` + "\n"), want: FormatMongoexport},
		{name: "BSON", data: mustBSON(t, bson.M{"a": "b"}), want: FormatBSON},
		{name: "RDB", data: buildRDB(t), want: FormatRDB},
		{name: "forced format wins", data: []byte(typed), forced: FormatMongoexport, want: FormatMongoexport},
		{name: "gzip needs a forced format", data: gzipBytes(t, []byte(typed)), forced: FormatJSONL, want: FormatJSONL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := OpenPipe(bytes.NewReader(tt.data), tt.forced, numfmt.DecimalAuto)
			require.NoError(t, err)
			require.Equal(t, tt.want, st.format)
		})
	}
}

// TestOpenPipeReadsGzip confirms that a gzipped dump on stdin decodes like a
// plain one.
func TestOpenPipeReadsGzip(t *testing.T) {
	st, err := OpenPipe(bytes.NewReader(gzipBytes(t, []byte(pipeDump))), FormatJSONL, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.Len(t, collect(t, st), 2)
}

// TestOpenPipeCarriesTheDecimalMode confirms that the piped (stdin) store
// keeps the decimal mode it was opened with. Dropping it silently decodes a
// piped dump under a different number policy than the same dump read from a path.
func TestOpenPipeCarriesTheDecimalMode(t *testing.T) {
	for _, dec := range []numfmt.DecimalMode{numfmt.DecimalAuto, numfmt.DecimalString} {
		t.Run(fmt.Sprintf("mode %d survives", dec), func(t *testing.T) {
			st, err := OpenPipe(strings.NewReader(`{"key":"a","type":"string","value":"x"}`), FormatJSONL, dec)
			require.NoError(t, err)
			require.Equal(t, dec, st.dec)
		})
	}
}

// failingReader returns err on every read.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// TestOpenPipeReportsAReadErrorOfTheHead pins that a read error during the peek,
// other than the end of the input, is an error that keeps its cause.
func TestOpenPipeReportsAReadErrorOfTheHead(t *testing.T) {
	boom := errors.New("boom")

	_, err := OpenPipe(failingReader{err: boom}, FormatUnknown, numfmt.DecimalAuto)

	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "read stdin head: boom")
}
