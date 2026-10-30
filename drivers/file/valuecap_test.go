package file

import (
	"context"
	"io"
	"strings"
	"testing"

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
