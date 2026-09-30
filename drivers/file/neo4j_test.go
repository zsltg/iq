package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// apocNodes is a small APOC JSON Lines export: two Person nodes, one Book node, and
// a KNOWS relationship between the two people. It exercises label filtering (Book is
// excluded from a Person scan), the node/relationship split, and reserved-key
// overwrite (the first node carries a real "_id" property that the envelope clobbers).
const apocNodes = `{"type":"node","id":"0","labels":["Person"],"properties":{"name":"Ada","age":36,"score":1.5,"_id":"clobbered"}}
{"type":"node","id":"1","labels":["Person","User"],"properties":{"name":"Grace"}}
{"type":"node","id":"2","labels":["Book"],"properties":{"title":"Notes"}}
{"id":"5","type":"relationship","label":"KNOWS","properties":{"since":2019},"start":{"id":"0","labels":["Person"]},"end":{"id":"1","labels":["Person"]}}
`

func TestNeo4jNodeScanEnvelope(t *testing.T) {
	u := writeDump(t, "graph.json", []byte(apocNodes), "format=neo4j&label=Person")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatNeo4jJSON, st.format)

	recs := collect(t, st)
	// Only the two Person nodes; the Book node and the relationship are other keyspaces.
	require.Len(t, recs, 2)
	// Integers narrow to int, floats stay float, and the reserved _id overwrites the
	// same-named property with the export id.
	require.Equal(t, query.Record{Key: "0", Type: "node", Value: map[string]any{
		"name": "Ada", "age": 36, "score": 1.5, "_id": "0", "_labels": []any{"Person"},
	}}, recs["0"])
	// A node with several labels is still reached by any one of them.
	require.Equal(t, query.Record{Key: "1", Type: "node", Value: map[string]any{
		"name": "Grace", "_id": "1", "_labels": []any{"Person", "User"},
	}}, recs["1"])
}

func TestNeo4jRelationshipScan(t *testing.T) {
	u := writeDump(t, "graph.json", []byte(apocNodes), "format=neo4j&rel=KNOWS")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	require.Len(t, recs, 1)
	require.Equal(t, query.Record{Key: "5", Type: "relationship", Value: map[string]any{
		"since": 2019, "_id": "5", "_type": "KNOWS", "_start": "0", "_end": "1",
	}}, recs["5"])
}

func TestNeo4jKeyOverrideAndFallback(t *testing.T) {
	// One node carries the key property, the other does not — the keyless node falls
	// back to its export id so the scan stays total.
	dump := `{"type":"node","id":"0","labels":["Person"],"properties":{"email":"ada@x"}}
{"type":"node","id":"1","labels":["Person"],"properties":{"name":"nokey"}}
`
	u := writeDump(t, "graph.json", []byte(dump), "format=neo4j&label=Person&key=email")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	require.Contains(t, recs, "ada@x")           // keyed by the email property.
	require.Contains(t, recs, "1")               // keyless node falls back to the id.
	require.Equal(t, "ada@x", recs["ada@x"].Key) // envelope still carries _id="0".
	require.Equal(t, "0", recs["ada@x"].Value.(map[string]any)["_id"])
}

func TestNeo4jKeyStringForms(t *testing.T) {
	// ?key= on a non-string property renders the key the way Cypher toString would.
	dump := `{"type":"node","id":"0","labels":["N"],"properties":{"n":36}}
{"type":"node","id":"1","labels":["N"],"properties":{"n":true}}
`
	t.Run("numeric key", func(t *testing.T) {
		u := writeDump(t, "n.json", []byte(dump), "format=neo4j&label=N&key=n")
		st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
		require.NoError(t, err)
		recs := collect(t, st)
		require.Contains(t, recs, "36") // json.Number keyed by its literal.
	})
	t.Run("bool key", func(t *testing.T) {
		u := writeDump(t, "n.json", []byte(dump), "format=neo4j&label=N&key=n")
		st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
		require.NoError(t, err)
		recs := collect(t, st)
		require.Contains(t, recs, "true") // bool keyed as true/false.
	})
}

func TestNeo4jNestedProperties(t *testing.T) {
	// A list property and a nested object have their numbers narrowed recursively,
	// so an array element or a map value is an exact int, not a float.
	dump := `{"type":"node","id":"0","labels":["N"],"properties":{"scores":[1,2,3],"meta":{"rank":5,"tag":"x"}}}` + "\n"
	u := writeDump(t, "nest.json", []byte(dump), "format=neo4j&label=N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	val := collect(t, st)["0"].Value.(map[string]any)
	require.Equal(t, []any{1, 2, 3}, val["scores"])
	require.Equal(t, map[string]any{"rank": 5, "tag": "x"}, val["meta"])
}

func TestNeo4jBigInteger(t *testing.T) {
	// An integer past int64 keeps full precision as a *big.Int, matching the live driver.
	dump := `{"type":"node","id":"0","labels":["N"],"properties":{"big":99999999999999999999}}` + "\n"
	u := writeDump(t, "big.json", []byte(dump), "format=neo4j&label=N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	recs := collect(t, st)
	want, _ := new(big.Int).SetString("99999999999999999999", 10)
	require.Equal(t, want, recs["0"].Value.(map[string]any)["big"])
}

func TestNeo4jEmptyDump(t *testing.T) {
	u := writeDump(t, "empty.json", []byte(""), "format=neo4j&label=N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Empty(t, collect(t, st)) // empty dump yields no records, no error.
}

func TestNeo4jMalformed(t *testing.T) {
	u := writeDump(t, "bad.json", []byte(`{"type":"node",`), "format=neo4j&label=N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "decode neo4j json")
}

func TestNeo4jArrayJSON(t *testing.T) {
	// APOC's ARRAY_JSON wraps the same objects in a single top-level array.
	dump := `[
		{"type":"node","id":"0","labels":["Person"],"properties":{"name":"Ada"}},
		{"type":"node","id":"1","labels":["Person"],"properties":{"name":"Grace"}}
	]`
	u := writeDump(t, "graph.json", []byte(dump), "format=neo4j&label=Person")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	require.Len(t, recs, 2)
	require.Equal(t, "Ada", recs["0"].Value.(map[string]any)["name"])
	require.Equal(t, "Grace", recs["1"].Value.(map[string]any)["name"])
}

func TestNeo4jGzip(t *testing.T) {
	u := writeDump(t, "graph.json.gz", gzipBytes(t, []byte(apocNodes)), "format=neo4j&label=Book")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	require.Len(t, recs, 1)
	require.Equal(t, "Notes", recs["2"].Value.(map[string]any)["title"])
}

func TestNeo4jDecimalString(t *testing.T) {
	dump := `{"type":"node","id":"0","labels":["N"],"properties":{"f":1.5,"i":7}}` + "\n"
	u := writeDump(t, "g.json", []byte(dump), "format=neo4j&label=N")
	st, err := Open(u, numfmt.DecimalString, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	val := recs["0"].Value.(map[string]any)
	require.Equal(t, "1.5", val["f"]) // fractional value rendered as its exact literal.
	require.Equal(t, 7, val["i"])     // integers are unaffected by the decimal mode.
}

func TestNeo4jSelectorErrors(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr string
	}{
		{name: "no selector", query: "format=neo4j", wantErr: "needs a keyspace"},
		{name: "both label and rel", query: "format=neo4j&label=Person&rel=KNOWS", wantErr: "only one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := writeDump(t, "g.json", []byte(apocNodes), tt.query)
			st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNeo4jUnknownType(t *testing.T) {
	dump := `{"type":"banana","id":"0","properties":{}}` + "\n"
	u := writeDump(t, "g.json", []byte(dump), "format=neo4j&label=Person")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, `type "banana"`)
}

func TestNeo4jFormatString(t *testing.T) {
	require.Equal(t, "neo4j-json", FormatNeo4jJSON.String())
	for _, syn := range []string{"neo4j-json", "neo4j", "apoc-json", "apoc"} {
		f, err := ParseFormat(syn)
		require.NoErrorf(t, err, "synonym %q", syn)
		require.Equalf(t, FormatNeo4jJSON, f, "synonym %q", syn)
	}
}

func TestPagingNeo4j(t *testing.T) {
	var buf bytes.Buffer
	for i := range bigCount {
		fmt.Fprintf(&buf, `{"type":"node","id":"n%d","labels":["Person"],"properties":{}}`+"\n", i)
	}
	u := writeDump(t, "big.json", buf.Bytes(), "format=neo4j&label=Person")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

// TestNeo4jKeyspaceSelector pins the keyspace resolution behind ?label=/?rel=,
// including the kind reported alongside each rejection: exactly one selector is
// required, and a rejected call reports the zero kind rather than a stray one.
func TestNeo4jKeyspaceSelector(t *testing.T) {
	tests := []struct {
		name     string
		hints    Hints
		wantKind neo4jKind
		wantName string
		wantErr  string
	}{
		{name: "a label selects the node keyspace", hints: Hints{Label: "Person"}, wantKind: nodeKind, wantName: "Person"},
		{name: "a rel selects the relationship keyspace", hints: Hints{Rel: "KNOWS"}, wantKind: relKind, wantName: "KNOWS"},
		{name: "both selectors is a contradiction", hints: Hints{Label: "Person", Rel: "KNOWS"}, wantErr: "only one"},
		{name: "neither selector leaves the keyspace unnamed", hints: Hints{}, wantErr: "needs a keyspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, name, err := neo4jKeyspace(tt.hints)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, neo4jKind(0), kind, "a rejected selector reports the zero kind")
				require.Empty(t, name)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.wantName, name)
		})
	}
}

// TestNeo4jKeyspaceIsolation is the rule a dump read depends on: a dump interleaves
// every label and type, so a read must emit only the selected kind. The fixture
// deliberately names a node label and a relationship type the same, so a filter that
// checked only the name would leak the other kind into the scan.
func TestNeo4jKeyspaceIsolation(t *testing.T) {
	dump := `{"type":"node","id":"0","labels":["KNOWS"],"properties":{"n":1}}
{"id":"5","type":"relationship","label":"KNOWS","properties":{"r":1},"start":{"id":"0"},"end":{"id":"0"}}
{"id":"6","type":"relationship","label":"LIKES","properties":{"r":2},"start":{"id":"0"},"end":{"id":"0"}}
`
	tests := []struct {
		name     string
		query    string
		wantKeys []string
		wantType string
	}{
		{name: "a rel read skips a node sharing its name", query: "format=neo4j&rel=KNOWS", wantKeys: []string{"5"}, wantType: "relationship"},
		{name: "a rel read skips another relationship type", query: "format=neo4j&rel=LIKES", wantKeys: []string{"6"}, wantType: "relationship"},
		{name: "a node read skips a relationship sharing its name", query: "format=neo4j&label=KNOWS", wantKeys: []string{"0"}, wantType: "node"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := writeDump(t, "g.json", []byte(dump), tt.query)
			st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			recs := collect(t, st)
			require.Len(t, recs, len(tt.wantKeys))
			for _, k := range tt.wantKeys {
				require.Contains(t, recs, k)
				require.Equal(t, tt.wantType, recs[k].Type)
			}
		})
	}
}

// TestNeo4jRecordKey pins how a record's map key is chosen: the export id unless
// ?key= names a property, and the export id again when that property is absent or
// null — including the corner where no ?key= is set at all but the node happens to
// carry an empty-named property.
func TestNeo4jRecordKey(t *testing.T) {
	tests := []struct {
		name    string
		keyProp string
		props   map[string]any
		want    string
	}{
		{name: "no key property uses the export id", props: map[string]any{"email": "ada@x"}, want: "9"},
		{name: "an empty-named property is not a key property", props: map[string]any{"": "weird"}, want: "9"},
		{name: "a named property is the key", keyProp: "email", props: map[string]any{"email": "ada@x"}, want: "ada@x"},
		{name: "a missing property falls back to the export id", keyProp: "email", props: map[string]any{"name": "Ada"}, want: "9"},
		{name: "a null property falls back to the export id", keyProp: "email", props: map[string]any{"email": nil}, want: "9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, recordKey("9", tt.props, tt.keyProp))
		})
	}
}

// TestNeo4jNullKeyPropertyEndToEnd drives the null fallback through a real dump, so
// the rule holds for the JSON null a decoder actually produces rather than only for
// a hand-built map.
func TestNeo4jNullKeyPropertyEndToEnd(t *testing.T) {
	dump := `{"type":"node","id":"0","labels":["Person"],"properties":{"email":null,"name":"Ada"}}` + "\n"
	u := writeDump(t, "g.json", []byte(dump), "format=neo4j&label=Person&key=email")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	recs := collect(t, st)
	require.Len(t, recs, 1)
	require.Contains(t, recs, "0", "a null key property falls back to the export id")
}

// partialReader yields a fixed prefix and then fails, so a test can drive a failure
// that lands mid-decode rather than in the prologue peek.
type partialReader struct {
	prefix []byte
	err    error
}

func (p *partialReader) Read(b []byte) (int, error) {
	if len(p.prefix) == 0 {
		return 0, p.err
	}
	n := copy(b, p.prefix)
	p.prefix = p.prefix[n:]
	return n, nil
}

// TestNeo4jReadFailures separates the reader's two failure points. A stream that
// fails before any record is a prologue read failure; one that fails mid-record is a
// decode failure whose cause stays reachable through the wrap.
func TestNeo4jReadFailures(t *testing.T) {
	boom := errors.New("disk gone")
	drain := func(r io.Reader) error {
		src, err := neo4jSource(r, pageSize, numfmt.DecimalAuto, Hints{Label: "Person"})
		require.NoError(t, err)
		return src(context.Background(), func([]query.Record) error { return nil })
	}
	t.Run("a prologue read failure is reported as such", func(t *testing.T) {
		err := drain(errReader{err: boom})
		require.ErrorContains(t, err, "read neo4j json")
		require.ErrorIs(t, err, boom)
	})
	t.Run("a mid-record read failure keeps its cause", func(t *testing.T) {
		err := drain(&partialReader{prefix: []byte(`{"type":"node","id":"1"`), err: boom})
		require.ErrorContains(t, err, "decode neo4j json")
		require.ErrorIs(t, err, boom)
	})
	t.Run("a stray closing bracket is a decode failure", func(t *testing.T) {
		err := drain(strings.NewReader(`{"type":"node","id":"1","labels":["Person"],"properties":{}}` + "\n]\n"))
		require.ErrorContains(t, err, "decode neo4j json")
	})
	t.Run("an empty array hands over no page", func(t *testing.T) {
		src, err := neo4jSource(strings.NewReader("[]"), pageSize, numfmt.DecimalAuto, Hints{Label: "Person"})
		require.NoError(t, err)
		called := false
		require.NoError(t, src(context.Background(), func([]query.Record) error { called = true; return nil }))
		require.False(t, called)
	})
	t.Run("a cancelled scan stops", func(t *testing.T) {
		src, err := neo4jSource(strings.NewReader(apocNodes), pageSize, numfmt.DecimalAuto, Hints{Label: "Person"})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, src(ctx, func([]query.Record) error { return nil }), context.Canceled)
	})
	t.Run("a consumer error propagates from a full page", func(t *testing.T) {
		var buf bytes.Buffer
		for i := range pageSize + 1 {
			fmt.Fprintf(&buf, `{"type":"node","id":"%d","labels":["Person"],"properties":{"n":%d}}`+"\n", i, i)
		}
		src, err := neo4jSource(&buf, 2, numfmt.DecimalAuto, Hints{Label: "Person"})
		require.NoError(t, err)
		consumer := &failFirstCall{err: boom}
		require.ErrorIs(t, src(context.Background(), consumer.accept), boom)
	})
}

// TestNeo4jSourceReportsAnEOFReadErrorInsideAnArray gives the APOC reader a
// reader that fails inside a top-level array with an error that wraps io.EOF. An
// array must end with its closing bracket, so the scan reports the error and does
// not end in silence with a truncated array.
func TestNeo4jSourceReportsAnEOFReadErrorInsideAnArray(t *testing.T) {
	gone := fmt.Errorf("disk gone: %w", io.EOF)
	node := `{"type":"node","id":"0","labels":["Person"],"properties":{"name":"Ada"}}`
	tests := []struct {
		name  string
		input string
	}{
		{name: "after a comma", input: "[" + node + ","},
		{name: "after an entry", input: "[" + node},
		{name: "after the opening bracket", input: "["},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := neo4jSource(&dataThenErrReader{data: tt.input, err: gone}, pageSize, numfmt.DecimalAuto, Hints{Label: "Person"})
			require.NoError(t, err)
			err = src(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, "decode neo4j json")
			require.ErrorIs(t, err, gone)
		})
	}
}
