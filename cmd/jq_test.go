package cmd

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// countingReader counts the Read calls on its reader, so a test can prove that a
// command left stdin unread.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// runWithStdin runs the root command with the given stdin text and returns the
// combined output and the number of reads on stdin.
func runWithStdin(t *testing.T, stdin string, args ...string) (string, int, error) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })
	root, _ := newRootCmd()
	in := &countingReader{r: strings.NewReader(stdin)}
	root.SetIn(in)
	out, err := runCmd(t, root, args...)
	return out, in.reads, err
}

// TestQueryReadsPipedStdin checks a query with no source reads a piped dump and
// that --explain and --verbose describe that read, while a plain run does not.
func TestQueryReadsPipedStdin(t *testing.T) {
	configEnv(t)
	dump := `{"key":"a","value":{"n":1}}` + "\n"
	tests := []struct {
		name     string
		flags    []string
		wantPlan bool
		wantData bool
	}{
		{name: "plain run", wantPlan: false, wantData: true},
		{name: "explain stops before reading", flags: []string{"--explain"}, wantPlan: true, wantData: false},
		{name: "verbose plans and runs", flags: []string{"--verbose"}, wantPlan: true, wantData: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.flags...), ".[]")
			out, reads, err := runWithStdin(t, dump, args...)
			require.NoError(t, err)
			require.Equal(t, tt.wantPlan, strings.Contains(out, "decode stdin dump"), out)
			require.Equal(t, tt.wantData, strings.Contains(out, `"n"`), out)
			require.Equal(t, tt.wantData, reads > 0, "stdin is read only when the query runs")
		})
	}
}

// TestQueryWithoutASourceOnATerminal checks a query with no source and no piped
// input reports the missing source instead of waiting on stdin.
func TestQueryWithoutASourceOnATerminal(t *testing.T) {
	configEnv(t)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = orig })

	root, _ := newRootCmd()
	_, err := runCmd(t, root, ".[]")
	require.ErrorIs(t, err, errNoSource)
}

// TestQueryLogsTracesAndHints runs a query against a fake backend and checks the
// log record, the wire trace switch and the scan-refusal hint.
func TestQueryLogsTracesAndHints(t *testing.T) {
	t.Run("a run logs its start", func(t *testing.T) {
		useCombDriver(t)
		root, _ := newRootCmd()
		out, err := runCmd(t, root, "--src", "a", "--log.file", "stderr", "--log.level", "debug", "--log.format", "json", ".[]")
		require.NoError(t, err)
		require.Contains(t, out, `"msg":"query start"`)
	})

	t.Run("verbose turns the trace on", func(t *testing.T) {
		opened := useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", "--verbose", ".[]")
		require.NoError(t, err)
		require.NotEmpty(t, *opened)
		require.NotNil(t, (*opened)[0].trace)
	})

	t.Run("no verbose keeps the trace off", func(t *testing.T) {
		opened := useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", ".[]")
		require.NoError(t, err)
		require.NotEmpty(t, *opened)
		require.Nil(t, (*opened)[0].trace)
	})

	t.Run("a refused scan names the way out and keeps the cause", func(t *testing.T) {
		useCombDriver(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "a", ".")
		require.ErrorIs(t, err, query.ErrScanNotAllowed)
		require.ErrorContains(t, err, "re-run with --unbounded")
	})
}

// TestQueryWithAnUnknownSourceDoesNotReadStdin checks a named source that does not
// exist is an error even when input is piped.
func TestQueryWithAnUnknownSourceDoesNotReadStdin(t *testing.T) {
	configEnv(t)
	_, reads, err := runWithStdin(t, `{"key":"a","value":{"n":1}}`+"\n", "--src", "nosuch", ".[]")
	require.ErrorContains(t, err, "nosuch")
	require.Zero(t, reads, "stdin stays unread")
}

// lazyDump is a typed JSONL stream that makes its records on demand. It counts
// the bytes that the reader took, so a test can see how much of the input a run
// had read at a given moment.
type lazyDump struct {
	next, total int
	pending     []byte
	taken       int
}

func (l *lazyDump) Read(p []byte) (int, error) {
	for len(l.pending) == 0 {
		if l.next == l.total {
			return 0, io.EOF
		}
		l.pending = fmt.Appendf(nil, `{"key":"k%d","type":"document","value":{"n":%d}}`+"\n", l.next, l.next)
		l.next++
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	l.taken += n
	return n, nil
}

// firstWrite records how many bytes of the input were read when the first output
// arrived.
type firstWrite struct {
	in    *lazyDump
	taken int
	seen  bool
}

func (w *firstWrite) Write(p []byte) (int, error) {
	if !w.seen {
		w.seen = true
		w.taken = w.in.taken
	}
	return len(p), nil
}

// TestPipedStdinStreams checks that a query over piped stdin emits its first
// result before it has read the whole input.
func TestPipedStdinStreams(t *testing.T) {
	configEnv(t)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })

	const records = 20000
	in := &lazyDump{total: records}
	out := &firstWrite{in: in}
	root, _ := newRootCmd()
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{".[]"})
	require.NoError(t, root.Execute())

	require.True(t, out.seen)
	require.Equal(t, records, in.next, "the run reads all the input in the end")
	require.Less(t, out.taken, in.taken/10, "the first result must arrive before most of the input is read")
}

// TestPipedStdinFormats checks the format detection of piped stdin: an empty
// pipe is an error, and --from-format reads a gzipped dump that detection cannot
// sniff.
func TestPipedStdinFormats(t *testing.T) {
	configEnv(t)
	dump := `{"key":"a","type":"document","value":{"n":1}}` + "\n"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, err := zw.Write([]byte(dump))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	t.Run("an empty pipe is an error", func(t *testing.T) {
		_, _, err := runWithStdin(t, "", ".[]")
		require.ErrorContains(t, err, "dump is empty")
	})
	t.Run("a gzipped dump needs --from-format", func(t *testing.T) {
		out, _, err := runWithStdin(t, gz.String(), "--from-format", "jsonl", ".[]")
		require.NoError(t, err)
		require.Contains(t, out, `"n"`)
	})
}
