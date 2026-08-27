package file

import (
	"bytes"
	"fmt"
	"math/big"
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
