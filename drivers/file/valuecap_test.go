package file

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

const testCap = 1 << 10

// padTo returns a filler string so that the whole value has exactly total bytes
// once the fixed part of the value is added.
func padTo(total, fixed int) string { return strings.Repeat("x", total-fixed) }

func drain(src query.RecordSource) (int, error) {
	count := 0
	err := src(context.Background(), func(b []query.Record) error { count += len(b); return nil })
	return count, err
}

type capCase struct {
	name   string
	format string
	// build returns the dump body whose last value has exactly total bytes.
	build func(total int) string
	// source runs the reader under test on r with the test cap.
	source func(t *testing.T, r io.Reader) query.RecordSource
}

func capCases() []capCase {
	mongoDoc := func(total int) string {
		const fixed = len(`{"_id":1,"s":""}`)
		return `{"_id":1,"s":"` + padTo(total, fixed) + `"}`
	}
	apocNode := func(total int) string {
		const fixed = len(`{"type":"node","id":"1","labels":["P"],"properties":{"s":""}}`)
		return `{"type":"node","id":"1","labels":["P"],"properties":{"s":"` + padTo(total, fixed) + `"}}`
	}
	mongo := func(t *testing.T, r io.Reader) query.RecordSource {
		return extJSONSourceCap(capped{r, testCap}, pageSize, numfmt.DecimalAuto)
	}
	apoc := func(t *testing.T, r io.Reader) query.RecordSource {
		src, err := neo4jSourceCap(capped{r, testCap}, pageSize, numfmt.DecimalAuto, Hints{Label: "P"})
		require.NoError(t, err)
		return src
	}
	return []capCase{
		{"mongoexport lines", "mongoexport json", mongoDoc, mongo},
		{"mongoexport array", "mongoexport json", func(n int) string { return "[" + mongoDoc(100) + "," + mongoDoc(n) + "]" }, mongo},
		{"apoc lines", "neo4j json", apocNode, apoc},
		{"apoc array", "neo4j json", func(n int) string { return "[" + apocNode(100) + "," + apocNode(n) + "]" }, apoc},
		{
			"cqlsh csv", "cassandra csv", func(n int) string { return "id,s\n1," + padTo(n, len("1,")) + "\n" },
			func(t *testing.T, r io.Reader) query.RecordSource {
				src, err := cassandraCSVSourceCap(capped{r, testCap}, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"})
				require.NoError(t, err)
				return src
			},
		},
		{"cqlsh csv multiline field", "cassandra csv", func(n int) string {
			return "id,s\n1,\"" + strings.Repeat("x\n", (n-len(`1,""`))/2) + "\"\n"
		}, func(t *testing.T, r io.Reader) query.RecordSource {
			src, err := cassandraCSVSourceCap(capped{r, testCap}, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"})
			require.NoError(t, err)
			return src
		}},
	}
}

// TestDumpValueCap pins the size limit of one value in the mongoexport, APOC,
// and cqlsh CSV readers: a value at the limit decodes, a value past it fails.
func TestDumpValueCap(t *testing.T) {
	for _, tc := range capCases() {
		t.Run(tc.name+" under the cap decodes", func(t *testing.T) {
			count, err := drain(tc.source(t, strings.NewReader(tc.build(testCap-8))))
			require.NoError(t, err)
			require.Equal(t, 1+boolToInt(strings.Contains(tc.name, "array") && !strings.Contains(tc.name, "csv")), count)
		})
		t.Run(tc.name+" over the cap fails", func(t *testing.T) {
			_, err := drain(tc.source(t, strings.NewReader(tc.build(testCap+64))))
			require.ErrorIs(t, err, errValueTooLarge)
			require.ErrorContains(t, err, tc.format+" record ")
			require.ErrorContains(t, err, "value is over the size limit (1024 bytes)")
			require.NotContains(t, err.Error(), "xxxx")
		})
		t.Run(tc.name+" huge value stops near the cap", func(t *testing.T) {
			cr := &countingReader{r: strings.NewReader(tc.build(64 << 20))}
			_, err := drain(tc.source(t, cr))
			require.ErrorIs(t, err, errValueTooLarge)
			require.LessOrEqual(t, cr.n, 2*testCap+4096, "read far past the cap")
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestDumpValueCapIsPerValue shows that many small values pass although the
// whole file is far over the cap.
func TestDumpValueCapIsPerValue(t *testing.T) {
	line := `{"_id":1,"s":"` + strings.Repeat("y", 200) + `"}` + "\n"
	body := strings.Repeat(line, 50)
	require.Greater(t, len(body), testCap*5)
	count, err := drain(extJSONSourceCap(capped{strings.NewReader(body), testCap}, pageSize, numfmt.DecimalAuto))
	require.NoError(t, err)
	require.Equal(t, 50, count)
}

// TestDumpValueCapRecordNumber pins the position in the error.
func TestDumpValueCapRecordNumber(t *testing.T) {
	body := `{"_id":1}` + "\n" + `{"_id":2}` + "\n" + `{"_id":3,"s":"` + strings.Repeat("x", 2*testCap) + `"}`
	_, err := drain(extJSONSourceCap(capped{strings.NewReader(body), testCap}, pageSize, numfmt.DecimalAuto))
	require.EqualError(t, err, "mongoexport json record 3: value is over the size limit (1024 bytes)")
}

// TestDumpValueCapIsTheJSONLLimit makes sure that the readers and the typed
// JSONL reader share one limit.
func TestDumpValueCapIsTheJSONLLimit(t *testing.T) {
	require.Equal(t, 16<<20, query.MaxRecordBytes)
}

// TestDumpValueRealCap runs one reader at the real 16 MiB limit. A 64 MiB
// document must fail before the reader takes much more than 16 MiB.
func TestDumpValueRealCap(t *testing.T) {
	body := `{"_id":1,"s":"` + strings.Repeat("x", 64<<20) + `"}`
	cr := &countingReader{r: strings.NewReader(body)}
	_, err := drain(extJSONSource(cr, pageSize, numfmt.DecimalAuto))
	require.ErrorIs(t, err, errValueTooLarge)
	require.LessOrEqual(t, cr.n, query.MaxRecordBytes+4096)
}

// TestDumpValueCapBoundary pins the exact limit. The first value of a dump may
// have exactly the limit in bytes. A value in an array has a comma before it,
// and the comma counts, so it has one byte less. One more byte fails.
func TestDumpValueCapBoundary(t *testing.T) {
	tests := []struct {
		name string
		// atCap is the build size of the largest value that decodes.
		atCap int
	}{
		{"mongoexport lines", testCap},
		{"mongoexport array", testCap - 1},
		{"apoc lines", testCap},
		{"apoc array", testCap - 1},
		{"cqlsh csv", testCap - 1}, // build adds a newline, which ends the record.
	}
	cases := map[string]capCase{}
	for _, tc := range capCases() {
		cases[tc.name] = tc
	}
	for _, tt := range tests {
		tc := cases[tt.name]
		t.Run(tt.name+" at the cap decodes", func(t *testing.T) {
			_, err := drain(tc.source(t, strings.NewReader(tc.build(tt.atCap))))
			require.NoError(t, err)
		})
		t.Run(tt.name+" one byte over fails", func(t *testing.T) {
			_, err := drain(tc.source(t, strings.NewReader(tc.build(tt.atCap+1))))
			require.ErrorIs(t, err, errValueTooLarge)
		})
	}
}

// TestDumpValueCapSeparator pins the cap error that json.Decoder.More hides in
// an array. Blanks after a value are not part of the next value, but they count
// toward its size, so a gap over the limit fails the read at record 2.
func TestDumpValueCapSeparator(t *testing.T) {
	gap := strings.Repeat(" ", 2*testCap)
	tests := []struct {
		name   string
		body   string
		source func(t *testing.T, r io.Reader) query.RecordSource
		want   string
	}{
		{
			"mongoexport array", `[{"_id":1}` + gap + `,{"_id":2}]`, capCases()[1].source,
			"mongoexport json record 2: value is over the size limit (1024 bytes)",
		},
		{
			"apoc array", `[{"type":"node","id":"1","labels":["P"],"properties":{}}` + gap + `]`, capCases()[3].source,
			"neo4j json record 2: value is over the size limit (1024 bytes)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := drain(tt.source(t, strings.NewReader(tt.body)))
			require.EqualError(t, err, tt.want)
		})
	}
}

// TestDumpValueCapIsPerCSVRecord shows that many small CSV records pass
// although the whole file is far over the cap.
func TestDumpValueCapIsPerCSVRecord(t *testing.T) {
	body := "id,s\n" + strings.Repeat("1,"+strings.Repeat("y", 200)+"\n", 50)
	require.Greater(t, len(body), testCap*5)
	src, err := cassandraCSVSourceCap(capped{strings.NewReader(body), testCap}, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"})
	require.NoError(t, err)
	count, err := drain(src)
	require.NoError(t, err)
	require.Equal(t, 50, count)
}

// TestCassandraCSVCapRecordNumber pins the CSV record number in the error. The
// header is record 1, and a dump with a ?columns= hint has no header record.
func TestCassandraCSVCapRecordNumber(t *testing.T) {
	big := strings.Repeat("x", 2*testCap)
	tests := []struct {
		name  string
		body  string
		hints Hints
		want  string
	}{
		{"header", "id," + big + "\n1,a\n", Hints{Keys: "id"}, "cassandra csv record 1"},
		{"first row", "id,s\n1," + big + "\n", Hints{Keys: "id"}, "cassandra csv record 2"},
		{"third row", "id,s\n1,a\n2," + big + "\n", Hints{Keys: "id"}, "cassandra csv record 3"},
		{"first row with columns hint", "1," + big + "\n", Hints{Keys: "id", Columns: "id,s"}, "cassandra csv record 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := cassandraCSVSourceCap(capped{strings.NewReader(tt.body), testCap}, pageSize, numfmt.DecimalAuto, tt.hints)
			require.NoError(t, err)
			_, err = drain(src)
			require.EqualError(t, err, tt.want+": value is over the size limit (1024 bytes)")
		})
	}
}

// TestCassandraCSVReadErrors pins the errors for a header or a row that the CSV
// reader cannot parse, and for a source that fails to read.
func TestCassandraCSVReadErrors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		src  io.Reader
		want string
	}{
		{"header parse", strings.NewReader("id,\"a\"b\n"), "read cassandra csv header: "},
		{"row parse", strings.NewReader("id,s\n1,\"a\"b\n"), "read cassandra csv row: "},
		{"header read", iotest.ErrReader(boom), "read cassandra csv header: boom"},
		{"row read", io.MultiReader(strings.NewReader("id,s\n"), iotest.ErrReader(boom)), "read cassandra csv row: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := cassandraCSVSourceCap(capped{tt.src, testCap}, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"})
			require.NoError(t, err)
			_, err = drain(src)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestDumpJSONReadErrors pins the errors of the JSON readers for a source that
// fails to read, for a dump with no value, and for a limit that stops the
// opening bracket of an array.
func TestDumpJSONReadErrors(t *testing.T) {
	boom := errors.New("boom")
	readers := []struct {
		format string
		first  string // one valid line of the dump.
		source func(src capped) query.RecordSource
	}{
		{"mongoexport json", `{"_id":1}`, func(src capped) query.RecordSource {
			return extJSONSourceCap(src, pageSize, numfmt.DecimalAuto)
		}},
		{"neo4j json", `{"type":"node","id":"1","labels":["P"],"properties":{}}`, func(src capped) query.RecordSource {
			s, err := neo4jSourceCap(src, pageSize, numfmt.DecimalAuto, Hints{Label: "P"})
			require.NoError(t, err)
			return s
		}},
	}
	for _, rd := range readers {
		t.Run(rd.format+" read fails at the start", func(t *testing.T) {
			_, err := drain(rd.source(capped{iotest.ErrReader(boom), testCap}))
			require.EqualError(t, err, "read "+rd.format+": boom")
		})
		t.Run(rd.format+" read fails after a value", func(t *testing.T) {
			body := io.MultiReader(strings.NewReader(rd.first+"\n"), iotest.ErrReader(boom))
			_, err := drain(rd.source(capped{body, testCap}))
			require.EqualError(t, err, "decode "+rd.format+": boom")
		})
		t.Run(rd.format+" blank dump has no record", func(t *testing.T) {
			count, err := drain(rd.source(capped{strings.NewReader(" \n"), testCap}))
			require.NoError(t, err)
			require.Zero(t, count)
		})
		t.Run(rd.format+" limit stops the opening bracket", func(t *testing.T) {
			_, err := drain(rd.source(capped{strings.NewReader(`[{"_id":1}]`), 0}))
			require.ErrorIs(t, err, errValueTooLarge)
			require.EqualError(t, err, map[string]string{
				"mongoexport json": "read mongoexport array: value is over the size limit",
				"neo4j json":       "read neo4j json array: value is over the size limit",
			}[rd.format])
		})
	}
}

// TestCapReaderBoundary pins the byte count that capReader lets through. It
// passes exactly the limit and then fails, however the caller sizes its reads.
func TestCapReaderBoundary(t *testing.T) {
	tests := []struct {
		name     string
		limit    int64
		readSize int
		wantRead int
	}{
		{"one byte reads end at the limit", 3, 1, 3},
		{"a read larger than the room is cut to the room", 4, 10, 4},
		{"a read of the room is not cut", 4, 4, 4},
		{"limit of zero reads nothing", 0, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &capReader{src: strings.NewReader("abcdefgh"), limit: tt.limit, consumed: func() int64 { return 0 }}
			total := 0
			var err error
			for err == nil {
				var n int
				n, err = cr.Read(make([]byte, tt.readSize))
				total += n
			}
			require.ErrorIs(t, err, errValueTooLarge)
			require.Equal(t, tt.wantRead, total)
		})
	}
}
