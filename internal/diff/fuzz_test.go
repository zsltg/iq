package diff_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
)

// maxFuzzInput bounds one fuzz input. Both walks are recursive over the decoded
// value, so an unbounded document would spend the -fuzztime budget on one input.
const maxFuzzInput = 64 << 10

// FuzzPatch drives the two diff engines with a pair of arbitrary JSON documents,
// which is the pair `iq diff` builds from two backends. Three oracles hold. A
// document never differs from itself, so Patch reports the empty patch. Patch
// always answers with a patch, never with a null. The hand-rolled tree walk and the
// RFC 6902 patch agree on the one question both answer: whether the documents
// differ at all.
func FuzzPatch(f *testing.F) {
	// Seeds cover the walk branches (scalars, objects, arrays, added and removed
	// keys, a type mismatch) plus hostile input: empty, deep nesting, invalid UTF-8
	// and a number beyond float64.
	pairs := [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`{"a":1}`, `{"a":1}`},
		{`{"a":1}`, `{"b":1}`},
		{`{"a":{"b":[1,2,3]}}`, `{"a":{"b":[1,3]}}`},
		{`[1,2,3]`, `[3,2,1]`},
		{`{"a":[]}`, `{"a":{}}`},
		{`{"a":null}`, `{"a":false}`},
		{`"x"`, `1`},
		{`null`, `null`},
		{``, ``},
		{`{"a":[[[[[[[[[[1]]]]]]]]]]}`, `{"a":[[[[[[[[[[2]]]]]]]]]]}`},
		{"{\"a\":\"\xff\xfe\"}", `{"a":"x"}`},
		{`{"n":100000000000000000001}`, `{"n":1e400}`},
		{`{"a":-0}`, `{"a":0}`},
	}
	for _, p := range pairs {
		f.Add([]byte(p[0]), []byte(p[1]))
	}

	f.Fuzz(func(t *testing.T, left, right []byte) {
		if len(left) > maxFuzzInput || len(right) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		var a, b any
		if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
			return // `iq diff` only ever sees decoded documents.
		}

		self, err := diff.Patch(a, a)
		require.NoError(t, err, "a document must diff against itself")
		require.Equal(t, "[]", string(self), "a document differs from itself")

		patch, err := diff.Patch(a, b)
		require.NoError(t, err, "two decoded documents must diff")
		require.NotNil(t, patch, "Patch answered with no patch and no error")

		require.Equalf(t, string(patch) == "[]", len(diff.Tree(a, b)) == 0,
			"the tree walk and the patch disagree on whether the documents differ\n patch: %s", patch)
	})
}
