package rawpred_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
	"github.com/zsltg/iq/internal/rawpred"
)

// maxFuzzInput bounds one fuzz input. A larger document tells the byte matcher
// nothing new and it makes the -fuzztime budget dishonest.
const maxFuzzInput = 64 << 10

// FuzzMatch drives the raw-byte prefilter with an arbitrary jq filter and an
// arbitrary document. The filter goes through the real front end, gojq.Parse then
// pushdown.Compile, so only predicates a scan can actually push are tested.
//
// Two oracles hold. The load-bearing one is the drop invariant: if Match reports
// CannotMatch, the decoded document must NOT match the same predicate under the
// independent reference evaluator in property_test.go. The second is that the
// prepared Matcher agrees with the package-level Match on every pair.
func FuzzMatch(f *testing.F) {
	// Seeds pair a pushable filter from the pushdown_test.go tables with a document
	// from the rawpred tables, plus hostile documents: empty, deep nesting, invalid
	// UTF-8 and an integer beyond float64.
	exprs := []string{
		`.[] | select(.a == "x")`,
		`.[] | select(.["a"] == 1)`,
		`.[] | select(.a == "x" and .b == 2)`,
		`.[] | select(.name > "m")`,
		`.[] | select(has("author"))`,
		`.[] | select(.meta | has("isbn"))`,
		`.[] | select(has("opt") | not)`,
		`.[] | select(.name | test("^A"))`,
		`.[] | select(.name | test("^a"; "i"))`,
		`.[] | select(.xs | any(.k == 1))`,
		`.[] | select(.xs | any(.k == 1) | not)`,
		`.[] | select(.a | length == 2)`,
		`.[] | select(.year > 2015 and .type == "book")`,
	}
	docs := []string{
		`{"a":"x","b":2}`,
		`{"a":5,"b":6,"c":0}`,
		`{"a":100000000000000000001}`,
		`{"n":{"x":1}}`,
		`{"xs":[{"k":1},{"k":2}]}`,
		`{"xs":{"p":{"k":1}}}`,
		`{"xs":"str"}`,
		`{"name":"Apple","year":2020,"type":"book"}`,
		`{}`,
		``,
		`{"a":[[[[[[[[[[1]]]]]]]]]]}`,
		"{\"a\":\"\xff\xfe\"}",
		`not json at all`,
	}
	for i, expr := range exprs {
		f.Add(expr, []byte(docs[i%len(docs)]))
	}
	for _, doc := range docs {
		f.Add(exprs[0], []byte(doc))
	}

	f.Fuzz(func(t *testing.T, expr string, raw []byte) {
		if len(expr) > maxFuzzInput || len(raw) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		q, err := gojq.Parse(expr)
		if err != nil {
			return // not a jq filter.
		}
		pred, ok := pushdown.Compile(q)
		if !ok {
			return // nothing to push, so the prefilter never runs.
		}

		got := rawpred.Match(raw, pred)
		require.Equalf(t, got, rawpred.NewMatcher(pred).Match(raw),
			"prepared Matcher disagrees with Match\n expr: %s\n doc:  %s", expr, raw)

		if got != rawpred.CannotMatch {
			return // only a drop can be wrong.
		}
		if !json.Valid(raw) {
			return // the reference evaluator needs a decodable document.
		}
		if !refCompilable(pred) {
			return // the reference regex is RE2, and this pattern does not compile.
		}
		require.Falsef(t, refMatch(decodeDoc(t, string(raw)), pred),
			"wrong drop: Match said CannotMatch but the decoded document matches\n expr: %s\n doc:  %s", expr, raw)
	})
}

// refCompilable reports whether every Regex node in pred has a pattern the
// reference evaluator can compile. pushdown accepts a pattern that jq and PCRE read
// the same way, which is not the same set RE2 accepts, so refRegexp can panic on a
// pattern rawpred handles by degrading the node to a decode. Such a pair proves
// nothing about the drop invariant, so the fuzz target passes over it.
func refCompilable(node predicate.Node) bool {
	switch n := node.(type) {
	case predicate.Regex:
		pattern := n.Pattern
		if strings.ContainsRune(n.Flags, 'i') {
			pattern = "(?i)" + pattern
		}
		if strings.ContainsRune(n.Flags, 'm') {
			pattern = "(?s)" + pattern
		}
		_, err := regexp.Compile(pattern)
		return err == nil
	case predicate.And:
		for _, c := range n {
			if !refCompilable(c) {
				return false
			}
		}
		return true
	case predicate.Or:
		for _, c := range n {
			if !refCompilable(c) {
				return false
			}
		}
		return true
	case predicate.ElemMatch:
		return refCompilable(n.Cond)
	case predicate.NoneMatch:
		return refCompilable(n.Cond)
	default:
		return true
	}
}
