package rawpred_test

import (
	"encoding/json"
	"math"
	"math/big"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// TestMatchNeverDropsAMatch is the load-bearing invariant test: for every
// generated (document, predicate) pair rawpred.Match reports CannotMatch, the
// document decoded exactly as the driver decodes it and evaluated against the same
// predicate by an independent reference evaluator must NOT match. A single
// counterexample is a wrong drop — a document the full jq would have kept. It runs
// without a container, on a fixed seed, so it is deterministic and repeatable.
func TestMatchNeverDropsAMatch(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(0xC0FFEE))

	const docs, preds = 300, 300
	rawDocs := make([]string, docs)
	decoded := make([]any, docs)
	for i := range rawDocs {
		rawDocs[i] = genDoc(rng)
		decoded[i] = decodeDoc(t, rawDocs[i])
	}
	predSet := make([]predicate.Node, preds)
	for i := range predSet {
		predSet[i] = genPred(rng, 2)
	}

	drops := 0
	for _, p := range predSet {
		for i, raw := range rawDocs {
			if rawpred.Match([]byte(raw), p) != rawpred.CannotMatch {
				continue
			}
			drops++
			require.Falsef(t, refMatch(decoded[i], p),
				"wrong drop: Match said CannotMatch but the decoded document matches\n doc:  %s\n pred: %#v", raw, p)
		}
	}
	// Guard the test itself: if nothing ever dropped, the invariant is vacuous and
	// the generators need widening.
	require.Positive(t, drops, "generators produced no CannotMatch cases to check")
}

// TestMatcherEqualsMatch pins that the prepared Matcher and the package-level Match
// agree on every (document, predicate) pair the generator emits: NewMatcher(pred)
// reused across a scan must return exactly what a fresh Match(raw, pred) would, so
// the driver's prepared path is behaviourally identical to the convenience path.
func TestMatcherEqualsMatch(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(0xBADF00D))

	const docs, preds = 300, 300
	rawDocs := make([]string, docs)
	for i := range rawDocs {
		rawDocs[i] = genDoc(rng)
	}
	predSet := make([]predicate.Node, preds)
	for i := range predSet {
		predSet[i] = genPred(rng, 2)
	}

	checked := 0
	for _, p := range predSet {
		m := rawpred.NewMatcher(p)
		for _, raw := range rawDocs {
			want := rawpred.Match([]byte(raw), p)
			got := m.Match([]byte(raw))
			require.Equalf(t, want, got,
				"prepared Matcher disagrees with Match\n doc:  %s\n pred: %#v", raw, p)
			checked++
		}
	}
	require.Equal(t, docs*preds, checked, "every pair is compared")
}

// decodeDoc decodes a raw JSON document exactly as the Redis driver does — UseNumber
// plus numfmt conversion under the default decimal mode — so the reference sees the
// same Go values gojq will.
func decodeDoc(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return numfmt.ConvertNumbers(v, numfmt.DecimalAuto)
}

// refMatch is the independent oracle: it evaluates a predicate over decoded Go
// values with gojq.Compare as ground truth for jq's cross-type ordering, mirroring
// predicate semantics directly rather than through rawpred's byte logic.
func refMatch(v any, node predicate.Node) bool {
	switch n := node.(type) {
	case predicate.Eq:
		return gojq.Compare(pathValue(v, n.Path), n.Value) == 0
	case predicate.Ne:
		return gojq.Compare(pathValue(v, n.Path), n.Value) != 0
	case predicate.Cmp:
		c := gojq.Compare(pathValue(v, n.Path), n.Value)
		switch n.Op {
		case predicate.Gt:
			return c > 0
		case predicate.Ge:
			return c >= 0
		case predicate.Lt:
			return c < 0
		default:
			return c <= 0
		}
	case predicate.Exists:
		_, ok := pathLookup(v, n.Path)
		return ok
	case predicate.NotExists:
		_, ok := pathLookup(v, n.Path)
		return !ok
	case predicate.Regex:
		s, ok := pathValue(v, n.Path).(string)
		if !ok {
			// jq's test() errors on a non-string, so such a document must never be
			// dropped; the oracle treats it as a match (must-keep), which would flag
			// any wrong drop as a failed invariant.
			return true
		}
		return refRegexp(n.Pattern, n.Flags).MatchString(s)
	case predicate.Size:
		return refSize(pathValue(v, n.Path), n.N)
	case predicate.ElemMatch:
		// jq's `.path | any(Cond)`: an error evaluating any (a non-container, or a
		// cond that indexes a non-indexable element) must never drop the document, so
		// an erroring fold counts as a match (must-keep).
		match, errored := refAny(pathValue(v, n.Path), n.Cond)
		return errored || match
	case predicate.NoneMatch:
		// jq's `.path | any(Cond) | not`: an erroring any still errors under not, so
		// an erroring fold is must-keep; otherwise it is the negation of the match.
		match, errored := refAny(pathValue(v, n.Path), n.Cond)
		return errored || !match
	case predicate.And:
		for _, c := range n {
			if !refMatch(v, c) {
				return false
			}
		}
		return true
	case predicate.Or:
		for _, c := range n {
			if refMatch(v, c) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// refSize mirrors jq's polymorphic length compared to n: array elements, object
// keys, the rune count of a string, 0 for null, and |value| for a number. jq's
// length errors on a boolean, which the driver must not drop, so a boolean (and any
// unexpected decoded type) is treated as a match (must-keep).
func refSize(v any, n int) bool {
	switch t := v.(type) {
	case nil:
		return n == 0
	case string:
		return utf8.RuneCountInString(t) == n
	case []any:
		return len(t) == n
	case map[string]any:
		return len(t) == n
	case bool:
		return true
	case int:
		if t < 0 {
			t = -t
		}
		return t == n
	case *big.Int:
		return new(big.Int).Abs(t).Cmp(big.NewInt(int64(n))) == 0
	case float64:
		return math.Abs(t) == float64(n)
	default:
		return true
	}
}

// refAny mirrors jq's `any(Cond)`: isempty over `.[] | select(Cond)` short-circuits
// in element order, so it stops at the first match and errors if an earlier element
// cannot be iterated or indexed. It returns match=true at the first element that
// satisfies Cond, and errored=true when the container is a non-container or an
// element ahead of any match is not indexable the way Cond needs (jq would error).
func refAny(v any, cond predicate.Node) (match, errored bool) {
	var elems []any
	switch t := v.(type) {
	case []any:
		elems = t
	case map[string]any:
		for _, e := range t {
			elems = append(elems, e)
		}
	default:
		return false, true
	}
	for _, e := range elems {
		if !refIndexable(e) {
			return false, true
		}
		if refMatch(e, cond) {
			return true, false
		}
	}
	return false, false
}

// refIndexable reports whether jq can apply a field-path condition to an element
// without erroring: only an object (indexed to a value) and null (indexed to null)
// qualify; a scalar or array errors on `.field`.
func refIndexable(v any) bool {
	if v == nil {
		return true
	}
	_, ok := v.(map[string]any)
	return ok
}

// refRegexp compiles a test() pattern the way gojq does — i -> (?i), jq m -> (?s)
// dotall, unanchored MatchString — as an independent restatement of the mapping
// rawpred must match. It is the oracle's regex, so it never shares rawpred's code.
func refRegexp(pattern, flags string) *regexp.Regexp {
	if strings.ContainsRune(flags, 'i') {
		pattern = "(?i)" + pattern
	}
	if strings.ContainsRune(flags, 'm') {
		pattern = "(?s)" + pattern
	}
	return regexp.MustCompile(pattern)
}

// pathValue returns the value at path, or nil (jq's null) when the field is absent,
// matching how jq reads a missing field.
func pathValue(v any, path []string) any {
	got, ok := pathLookup(v, path)
	if !ok {
		return nil
	}
	return got
}

// pathLookup walks path through nested objects, reporting whether the leaf is
// present.
func pathLookup(v any, path []string) (any, bool) {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// valueSnippets are the raw JSON field values the document generator draws from,
// spanning every type and both integer and fractional numbers, precision edges,
// and escaped strings.
var valueSnippets = []string{
	`5`, `6`, `0`, `-3`, `10`, `9007199254740993`, `100000000000000000001`,
	`1.5`, `-2.5`, `1e2`,
	`"x"`, `"y"`, `"apple"`, `"banana"`, `"a\"b"`, `"a\nb"`, `"A\nB"`,
	`true`, `false`, `null`, `[1,2]`, `{"k":1}`,
}

// genDoc builds a random JSON object over the field universe {a,b,c,n}, each field
// independently included or omitted so predicates exercise present, absent, and
// nested paths.
func genDoc(rng *rand.Rand) string {
	var parts []string
	for _, f := range []string{"a", "b", "c"} {
		if rng.Intn(4) != 0 { // ~75% present
			parts = append(parts, `"`+f+`":`+valueSnippets[rng.Intn(len(valueSnippets))])
		}
	}
	switch rng.Intn(3) {
	case 0:
		parts = append(parts, `"n":{"x":`+valueSnippets[rng.Intn(len(valueSnippets))]+`}`)
	case 1:
		// n present but not an object, so a nested path is structurally ambiguous.
		parts = append(parts, `"n":`+valueSnippets[rng.Intn(len(valueSnippets))])
	}
	if rng.Intn(4) != 0 { // ~75% present, exercising the container-node fields
		parts = append(parts, `"xs":`+xsSnippets[rng.Intn(len(xsSnippets))])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// xsSnippets are the raw values the "xs" field draws from for ElemMatch/NoneMatch:
// arrays and objects of element objects (definite folds), empty containers, mixes
// with scalars and nulls (undecidable folds), and non-containers (jq any errors).
var xsSnippets = []string{
	`[{"k":1},{"k":2}]`, `[{"k":1}]`, `[{"k":2},{"k":3}]`, `[]`,
	`[{"k":1},5]`, `[5,{"k":1}]`, `[1,2]`, `[null,{"k":1}]`,
	`{"p":{"k":1},"q":{"k":2}}`, `{"p":{"k":2}}`, `{}`,
	`"str"`, `5`,
}

// sizeNs are the target lengths Size is generated with, spanning zero, the exact
// counts of the generated values, and misses.
var sizeNs = []int{0, 1, 2, 3, 5}

// elemCondValues and elemCondNums are the scalars an element condition compares the
// element's own k (or absent j) against, spanning the element key values and a type
// mismatch.
var (
	elemCondValues = []any{1.0, 2.0, 5.0, "x"}
	elemCondNums   = []any{1.0, 2.0}
)

// genElemCond builds the element condition of an ElemMatch/NoneMatch over the
// element's own fields, covering Eq, Cmp, and And/Or of equalities — the exact
// shapes pushdown emits as an any(cond) argument.
func genElemCond(rng *rand.Rand) predicate.Node {
	k := []string{"k"}
	j := []string{"j"}
	switch rng.Intn(5) {
	case 0:
		return predicate.Eq{Path: k, Value: elemCondValues[rng.Intn(len(elemCondValues))]}
	case 1:
		ops := []predicate.Op{predicate.Gt, predicate.Ge, predicate.Lt, predicate.Le}
		return predicate.Cmp{Path: k, Op: ops[rng.Intn(len(ops))], Value: elemCondNums[rng.Intn(len(elemCondNums))]}
	case 2:
		return predicate.And{
			predicate.Eq{Path: k, Value: elemCondValues[rng.Intn(len(elemCondValues))]},
			predicate.Eq{Path: j, Value: elemCondValues[rng.Intn(len(elemCondValues))]},
		}
	case 3:
		return predicate.Or{
			predicate.Eq{Path: k, Value: elemCondValues[rng.Intn(len(elemCondValues))]},
			predicate.Eq{Path: k, Value: elemCondValues[rng.Intn(len(elemCondValues))]},
		}
	default:
		return predicate.Eq{Path: j, Value: elemCondValues[rng.Intn(len(elemCondValues))]}
	}
}

// predPaths are the field paths predicates are generated over: three top-level and
// one nested.
var predPaths = [][]string{{"a"}, {"b"}, {"c"}, {"n", "x"}}

// eqValues are the scalar values Eq/Ne are generated with, spanning every scalar
// type; the numbers stay in the exact float64 range so the reference stays a sound
// oracle for the cases rawpred can decide.
var eqValues = []any{5.0, 6.0, 0.0, -3.0, "x", "apple", true, false, nil}

// cmpValues are the number/string values Cmp is generated with.
var cmpValues = []any{5.0, 0.0, -3.0, "banana", "b"}

// regexPatterns are portable test() patterns the generator draws from: literals,
// anchors, a dot (to exercise the dotall flag against the newline snippets), and a
// character class. Each is valid RE2 so NewMatcher always compiles it.
var regexPatterns = []string{"a", "x", "pp", "^a", "an", "n$", "a.b", "[0-9]"}

// regexFlags are the flag strings the generator draws from, covering none, each of
// i and m, and both together, so the translation is exercised in every combination.
var regexFlags = []string{"", "i", "m", "im"}

// genPred builds a random predicate tree up to the given depth over predPaths,
// covering every node type rawpred decides on plus And/Or nesting.
func genPred(rng *rand.Rand, depth int) predicate.Node {
	if depth <= 0 || rng.Intn(3) == 0 {
		return genLeaf(rng)
	}
	kids := []predicate.Node{genPred(rng, depth-1), genPred(rng, depth-1)}
	if rng.Intn(2) == 0 {
		return predicate.And(kids)
	}
	return predicate.Or(kids)
}

// genLeaf builds a random leaf predicate. Scalar-field nodes draw a path from
// predPaths; the container nodes ElemMatch/NoneMatch always target the "xs" field,
// whose values span arrays, objects, and non-containers.
func genLeaf(rng *rand.Rand) predicate.Node {
	path := predPaths[rng.Intn(len(predPaths))]
	switch rng.Intn(9) {
	case 0:
		return predicate.Eq{Path: path, Value: eqValues[rng.Intn(len(eqValues))]}
	case 1:
		return predicate.Ne{Path: path, Value: eqValues[rng.Intn(len(eqValues))]}
	case 2:
		ops := []predicate.Op{predicate.Gt, predicate.Ge, predicate.Lt, predicate.Le}
		return predicate.Cmp{Path: path, Op: ops[rng.Intn(len(ops))], Value: cmpValues[rng.Intn(len(cmpValues))]}
	case 3:
		return predicate.Exists{Path: path}
	case 4:
		return predicate.NotExists{Path: path}
	case 5:
		return predicate.Regex{
			Path:    path,
			Pattern: regexPatterns[rng.Intn(len(regexPatterns))],
			Flags:   regexFlags[rng.Intn(len(regexFlags))],
		}
	case 6:
		return predicate.Size{Path: path, N: sizeNs[rng.Intn(len(sizeNs))]}
	case 7:
		return predicate.ElemMatch{Path: []string{"xs"}, Cond: genElemCond(rng)}
	default:
		return predicate.NoneMatch{Path: []string{"xs"}, Cond: genElemCond(rng)}
	}
}
