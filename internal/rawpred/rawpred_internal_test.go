package rawpred

import (
	"fmt"
	"math"
	"testing"

	"github.com/buger/jsonparser"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// tripleName labels a triple for readable assertion failures.
func tripleName(t triple) string {
	switch t {
	case definiteNo:
		return "definiteNo"
	case definiteYes:
		return "definiteYes"
	default:
		return "unknown"
	}
}

// requireTriple asserts eval(raw, node) equals want, naming both sides. It prepares
// a Matcher over node first, so a Regex node's pattern is compiled exactly as
// production does.
func requireTriple(t *testing.T, raw string, node predicate.Node, want triple) {
	t.Helper()
	got := NewMatcher(node).eval([]byte(raw), node)
	require.Equalf(t, want, got, "eval: got %s want %s", tripleName(got), tripleName(want))
}

func TestEvalEq(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		pred predicate.Eq
		want triple
	}{
		{"number equal", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, definiteYes},
		{"number not equal", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: 6.0}, definiteNo},
		{"string equal", `{"a":"x"}`, predicate.Eq{Path: []string{"a"}, Value: "x"}, definiteYes},
		{"string not equal", `{"a":"x"}`, predicate.Eq{Path: []string{"a"}, Value: "y"}, definiteNo},
		{"escaped string equal", `{"a":"x\"y"}`, predicate.Eq{Path: []string{"a"}, Value: `x"y`}, definiteYes},
		{"backslash string equal", `{"a":"c:\\tmp"}`, predicate.Eq{Path: []string{"a"}, Value: `c:\tmp`}, definiteYes},
		{"unicode escape equal", `{"a":"é"}`, predicate.Eq{Path: []string{"a"}, Value: "é"}, definiteYes},
		{"bool equal", `{"a":true}`, predicate.Eq{Path: []string{"a"}, Value: true}, definiteYes},
		{"bool not equal", `{"a":true}`, predicate.Eq{Path: []string{"a"}, Value: false}, definiteNo},
		{"null equal", `{"a":null}`, predicate.Eq{Path: []string{"a"}, Value: nil}, definiteYes},
		{"null value vs present scalar", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: nil}, definiteNo},
		{"string value vs number doc", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: "5"}, definiteNo},
		{"number value vs string doc", `{"a":"5"}`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, definiteNo},
		{"bool value vs number doc", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: true}, definiteNo},
		{"missing field null value", `{"b":1}`, predicate.Eq{Path: []string{"a"}, Value: nil}, definiteYes},
		{"missing field scalar value", `{"b":1}`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, definiteNo},
		{"nested present equal", `{"n":{"x":5}}`, predicate.Eq{Path: []string{"n", "x"}, Value: 5.0}, definiteYes},
		{"nested leaf absent scalar", `{"n":{"y":5}}`, predicate.Eq{Path: []string{"n", "x"}, Value: 5.0}, definiteNo},
		{"nested leaf absent null", `{"n":{"y":5}}`, predicate.Eq{Path: []string{"n", "x"}, Value: nil}, definiteYes},
		{"intermediate not object", `{"n":5}`, predicate.Eq{Path: []string{"n", "x"}, Value: 5.0}, unknown},
		{"root is array", `[1,2]`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, unknown},
		{"root is scalar", `5`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, unknown},
		{"malformed json", `{"a":`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, unknown},
		{"empty raw", ``, predicate.Eq{Path: []string{"a"}, Value: 5.0}, unknown},
		{"empty path", `{"a":5}`, predicate.Eq{Path: nil, Value: 5.0}, unknown},
		{"big int doc", `{"a":10000000000000000001}`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, unknown},
		{"fractional doc", `{"a":1.5}`, predicate.Eq{Path: []string{"a"}, Value: 1.5}, unknown},
		{"leading whitespace object", "  {\"a\":5}", predicate.Eq{Path: []string{"a"}, Value: 5.0}, definiteYes},
		{"exact 2^53 equal", `{"a":9007199254740992}`, predicate.Eq{Path: []string{"a"}, Value: 9007199254740992.0}, definiteYes},
		// A pred value of a Go type that the pushdown compiler never emits (an int,
		// not a float64) is unknown, whatever the type of the document value.
		{"int value vs number doc", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: 5}, unknown},
		{"int value vs string doc", `{"a":"5"}`, predicate.Eq{Path: []string{"a"}, Value: 5}, unknown},
		{"int value vs null doc", `{"a":null}`, predicate.Eq{Path: []string{"a"}, Value: 5}, unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalNe(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		pred predicate.Ne
		want triple
	}{
		{"present equal drops", `{"a":5}`, predicate.Ne{Path: []string{"a"}, Value: 5.0}, definiteNo},
		{"present not equal holds", `{"a":5}`, predicate.Ne{Path: []string{"a"}, Value: 6.0}, definiteYes},
		{"missing scalar holds", `{"b":1}`, predicate.Ne{Path: []string{"a"}, Value: 5.0}, definiteYes},
		{"missing null value drops", `{"b":1}`, predicate.Ne{Path: []string{"a"}, Value: nil}, definiteNo},
		{"fractional doc unknown", `{"a":1.5}`, predicate.Ne{Path: []string{"a"}, Value: 1.5}, unknown},
		{"ambiguous unknown", `{"n":5}`, predicate.Ne{Path: []string{"n", "x"}, Value: 5.0}, unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalExists(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		pred predicate.Node
		want triple
	}{
		{"exists present", `{"a":5}`, predicate.Exists{Path: []string{"a"}}, definiteYes},
		{"exists null present", `{"a":null}`, predicate.Exists{Path: []string{"a"}}, definiteYes},
		{"exists absent", `{"b":1}`, predicate.Exists{Path: []string{"a"}}, definiteNo},
		{"exists ambiguous", `{"n":5}`, predicate.Exists{Path: []string{"n", "x"}}, unknown},
		{"notexists present", `{"a":5}`, predicate.NotExists{Path: []string{"a"}}, definiteNo},
		{"notexists absent", `{"b":1}`, predicate.NotExists{Path: []string{"a"}}, definiteYes},
		{"notexists ambiguous", `[1]`, predicate.NotExists{Path: []string{"a"}}, unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalCmp(t *testing.T) {
	p := func(op predicate.Op, v any) predicate.Cmp {
		return predicate.Cmp{Path: []string{"a"}, Op: op, Value: v}
	}
	tests := []struct {
		name string
		raw  string
		pred predicate.Cmp
		want triple
	}{
		{"gt true", `{"a":10}`, p(predicate.Gt, 5.0), definiteYes},
		{"gt false", `{"a":3}`, p(predicate.Gt, 5.0), definiteNo},
		{"gt equal false", `{"a":5}`, p(predicate.Gt, 5.0), definiteNo},
		{"ge equal true", `{"a":5}`, p(predicate.Ge, 5.0), definiteYes},
		{"lt true", `{"a":3}`, p(predicate.Lt, 5.0), definiteYes},
		{"lt equal false", `{"a":5}`, p(predicate.Lt, 5.0), definiteNo},
		{"le equal true", `{"a":5}`, p(predicate.Le, 5.0), definiteYes},
		{"le above false", `{"a":6}`, p(predicate.Le, 5.0), definiteNo},
		{"negative numbers", `{"a":-10}`, p(predicate.Lt, -5.0), definiteYes},
		{"fractional pred int doc", `{"a":5}`, p(predicate.Lt, 5.5), definiteYes},
		{"fractional doc unknown", `{"a":5.5}`, p(predicate.Gt, 5.0), unknown},
		{"big int doc unknown", `{"a":9007199254740993}`, p(predicate.Gt, 5.0), unknown},
		{"string greater", `{"a":"m"}`, p(predicate.Gt, "k"), definiteYes},
		{"string less", `{"a":"a"}`, p(predicate.Lt, "b"), definiteYes},
		{"string equal ge", `{"a":"b"}`, p(predicate.Ge, "b"), definiteYes},
		{"escaped string compare", `{"a":"a\"b"}`, p(predicate.Lt, "b"), definiteYes},
		{"doc array vs number", `{"a":[1]}`, p(predicate.Gt, 5.0), definiteYes},
		{"doc object vs number", `{"a":{"k":1}}`, p(predicate.Gt, 5.0), definiteYes},
		{"doc null vs number", `{"a":null}`, p(predicate.Lt, 5.0), definiteYes},
		{"doc bool vs number", `{"a":true}`, p(predicate.Lt, 5.0), definiteYes},
		{"doc number vs string value", `{"a":5}`, p(predicate.Lt, "x"), definiteYes},
		{"doc string vs number value", `{"a":"x"}`, p(predicate.Gt, 5.0), definiteYes},
		{"absent lt number", `{"b":1}`, p(predicate.Lt, 5.0), definiteYes},
		{"absent gt number", `{"b":1}`, p(predicate.Gt, 5.0), definiteNo},
		{"absent lt string", `{"b":1}`, p(predicate.Lt, "x"), definiteYes},
		{"ambiguous", `{"n":5}`, predicate.Cmp{Path: []string{"n", "x"}, Op: predicate.Gt, Value: 5.0}, unknown},
		{"non-comparable value bool", `{"a":5}`, p(predicate.Gt, true), unknown},
		{"non-comparable value null", `{"a":5}`, p(predicate.Gt, nil), unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalAndOr(t *testing.T) {
	eqA := predicate.Eq{Path: []string{"a"}, Value: 5.0}   // on {"a":5} → yes
	eqA6 := predicate.Eq{Path: []string{"a"}, Value: 6.0}  // on {"a":5} → no
	eqB := predicate.Eq{Path: []string{"b"}, Value: 1.0}   // on {"b":1} → yes
	fracC := predicate.Eq{Path: []string{"c"}, Value: 1.5} // on {"c":1.5} → unknown
	raw := `{"a":5,"b":1,"c":1.5}`
	tests := []struct {
		name string
		pred predicate.Node
		want triple
	}{
		{"and all yes", predicate.And{eqA, eqB}, definiteYes},
		{"and one no short circuits", predicate.And{eqA6, eqB}, definiteNo},
		{"and no after yes", predicate.And{eqA, eqA6}, definiteNo},
		{"and yes and unknown", predicate.And{eqA, fracC}, unknown},
		{"and empty matches all", predicate.And{}, definiteYes},
		{"or one yes", predicate.Or{eqA6, eqB}, definiteYes},
		{"or all no", predicate.Or{eqA6, predicate.Eq{Path: []string{"b"}, Value: 2.0}}, definiteNo},
		{"or no and unknown", predicate.Or{eqA6, fracC}, unknown},
		{"or empty unknown", predicate.Or{}, unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, raw, tt.pred, tt.want)
		})
	}
}

func TestEvalNilNode(t *testing.T) {
	// A nil predicate falls through eval's type switch to the default branch and is
	// never provable, so it is unknown (the document is kept).
	requireTriple(t, `{"a":5}`, nil, unknown)
}

func TestEvalSize(t *testing.T) {
	sz := func(path []string, n int) predicate.Size {
		return predicate.Size{Path: path, N: n}
	}
	a := func(n int) predicate.Size { return sz([]string{"a"}, n) }
	tests := []struct {
		name string
		raw  string
		pred predicate.Size
		want triple
	}{
		// Array: jq length is the element count.
		{"array count matches", `{"a":[1,2,3]}`, a(3), definiteYes},
		{"array count below drops", `{"a":[1,2,3]}`, a(2), definiteNo},
		// Object with a repeated key: a decoder keeps one entry, so the raw count
		// is not jq's length.
		{"object with a repeated key is unknown", `{"a":{"k":1,"k":2}}`, a(1), unknown},
		{"object with a repeated key is unknown for the raw count", `{"a":{"k":1,"k":2}}`, a(2), unknown},
		{"array count above drops", `{"a":[1,2,3]}`, a(4), definiteNo},
		{"empty array is zero", `{"a":[]}`, a(0), definiteYes},
		{"empty array vs one drops", `{"a":[]}`, a(1), definiteNo},
		{"array vs negative drops", `{"a":[1,2,3]}`, a(-1), definiteNo},
		{"nested array count", `{"n":{"x":[1,2]}}`, sz([]string{"n", "x"}, 2), definiteYes},
		// Object: jq length is the key count.
		{"object key count matches", `{"a":{"x":1,"y":2}}`, a(2), definiteYes},
		{"object key count drops", `{"a":{"x":1,"y":2}}`, a(3), definiteNo},
		{"empty object is zero", `{"a":{}}`, a(0), definiteYes},
		// String: jq length is the rune count of the unescaped value.
		{"string rune count matches", `{"a":"hello"}`, a(5), definiteYes},
		{"string rune count drops", `{"a":"hello"}`, a(4), definiteNo},
		{"empty string is zero", `{"a":""}`, a(0), definiteYes},
		{"unicode literal counts runes not bytes", `{"a":"café"}`, a(4), definiteYes},
		{"unicode literal byte count drops", `{"a":"café"}`, a(5), definiteNo},
		{"unicode escape counts one rune", `{"a":"café"}`, a(4), definiteYes},
		{"multibyte emoji counts once", `{"a":"a😀b"}`, a(3), definiteYes},
		{"multibyte emoji byte count drops", `{"a":"a😀b"}`, a(6), definiteNo},
		{"escaped quote counts runes", `{"a":"a\"b"}`, a(3), definiteYes},
		// Null and absent: jq length of null is 0.
		{"null is zero", `{"a":null}`, a(0), definiteYes},
		{"null vs one drops", `{"a":null}`, a(1), definiteNo},
		{"absent field is zero", `{"b":1}`, a(0), definiteYes},
		{"absent field vs one drops", `{"b":1}`, a(1), definiteNo},
		// Number: jq length is |literal|, decided only for a plain int64.
		{"integer magnitude matches", `{"a":5}`, a(5), definiteYes},
		{"integer magnitude drops", `{"a":5}`, a(4), definiteNo},
		{"zero number matches zero", `{"a":0}`, a(0), definiteYes},
		{"negative uses absolute value", `{"a":-3}`, a(3), definiteYes},
		{"negative absolute value drops", `{"a":-3}`, a(2), definiteNo},
		// The digit 9 must parse in base 10 (a base-9 parse would reject it), and a
		// multi-digit value must read in base 10 (base 11 would read "19" as 20).
		{"single digit nine decides", `{"a":9}`, a(9), definiteYes},
		{"two-digit value decides in base ten", `{"a":19}`, a(19), definiteYes},
		{"two-digit value miss drops in base ten", `{"a":19}`, a(20), definiteNo},
		{"negative nine uses magnitude", `{"a":-9}`, a(9), definiteYes},
		// int64 max needs the full 63 magnitude bits: a narrower parse would reject it.
		{"int64 max magnitude decides", `{"a":9223372036854775807}`, a(9223372036854775807), definiteYes},
		{"int64 max magnitude miss drops", `{"a":9223372036854775807}`, a(5), definiteNo},
		// A number's length is never negative, so a negative n drops it definitively
		// even for a value the numeric rule would otherwise leave unknown.
		{"integer vs negative n drops", `{"a":5}`, a(-1), definiteNo},
		{"fractional vs negative n drops", `{"a":1.5}`, a(-1), definiteNo},
		{"fractional number unknown", `{"a":1.5}`, a(1), unknown},
		{"fractional number unknown two", `{"a":1.5}`, a(2), unknown},
		{"exponent number unknown", `{"a":1e2}`, a(100), unknown},
		{"big int number unknown", `{"a":100000000000000000001}`, a(5), unknown},
		{"min int64 unknown", `{"a":-9223372036854775808}`, a(0), unknown},
		// Boolean: jq length errors on a boolean, so it is kept.
		{"boolean unknown", `{"a":true}`, a(1), unknown},
		{"boolean unknown zero", `{"a":false}`, a(0), unknown},
		// Structural surprises are unknown.
		{"ambiguous path unknown", `{"n":5}`, sz([]string{"n", "x"}, 1), unknown},
		{"root not object unknown", `[1,2]`, a(2), unknown},
		{"malformed json unknown", `{"a":`, a(1), unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

// elemCondK is the standard element condition used across the ElemMatch/NoneMatch
// tables: the element's own field k equals 1.
var elemCondK = predicate.Eq{Path: []string{"k"}, Value: 1.0}

func TestEvalElemMatch(t *testing.T) {
	em := func(path []string, cond predicate.Node) predicate.ElemMatch {
		return predicate.ElemMatch{Path: path, Cond: cond}
	}
	xs := func(cond predicate.Node) predicate.ElemMatch { return em([]string{"xs"}, cond) }
	tests := []struct {
		name string
		raw  string
		pred predicate.ElemMatch
		want triple
	}{
		// Array container.
		{"array single match", `{"xs":[{"k":1}]}`, xs(elemCondK), definiteYes},
		{"array match among failures", `{"xs":[{"k":2},{"k":1}]}`, xs(elemCondK), definiteYes},
		{"array all fail drops", `{"xs":[{"k":2},{"k":3}]}`, xs(elemCondK), definiteNo},
		{"empty array drops", `{"xs":[]}`, xs(elemCondK), definiteNo},
		{"array match wins over scalar", `{"xs":[5,{"k":1}]}`, xs(elemCondK), definiteYes},
		{"array fail plus scalar unknown", `{"xs":[{"k":2},5]}`, xs(elemCondK), unknown},
		{"array of scalars unknown", `{"xs":[1,2]}`, xs(elemCondK), unknown},
		{"array null element blocks drop", `{"xs":[null,{"k":2}]}`, xs(elemCondK), unknown},
		{"array nested-array element match", `{"xs":[[1],{"k":1}]}`, xs(elemCondK), definiteYes},
		// jsonparser hands a string element back without its quotes, so the
		// string "{}" reads as an empty object. jq's .k errors on a string, so the
		// element must stay unknown and must not fail the cond as an absent k.
		{"string element that looks like an object unknown", `{"xs":["{}"]}`, xs(elemCondK), unknown},
		// Object container: jq any iterates the values.
		{"object value match", `{"xs":{"a":{"k":1},"b":{"k":2}}}`, xs(elemCondK), definiteYes},
		{"object values all fail drops", `{"xs":{"a":{"k":2},"b":{"k":3}}}`, xs(elemCondK), definiteNo},
		{"empty object drops", `{"xs":{}}`, xs(elemCondK), definiteNo},
		{"object scalar value with match", `{"xs":{"a":5,"b":{"k":1}}}`, xs(elemCondK), definiteYes},
		{"object scalar value all fail unknown", `{"xs":{"a":{"k":2},"b":5}}`, xs(elemCondK), unknown},
		// Non-container, absent, ambiguous: jq any errors or cannot resolve.
		{"absent field unknown", `{"other":1}`, xs(elemCondK), unknown},
		{"scalar field unknown", `{"xs":5}`, xs(elemCondK), unknown},
		{"string field unknown", `{"xs":"hi"}`, xs(elemCondK), unknown},
		{"null field unknown", `{"xs":null}`, xs(elemCondK), unknown},
		{"nested container path", `{"n":{"xs":[{"k":1}]}}`, em([]string{"n", "xs"}, elemCondK), definiteYes},
		// Object with a repeated key: a decoder keeps one value, so the fold proves
		// nothing, even when one value matches or every value fails.
		{"object with a repeated key and a match is unknown", `{"xs":{"p":{"k":1},"p":{"k":2}}}`, xs(elemCondK), unknown},
		{"object with a repeated key and no match is unknown", `{"xs":{"p":{"k":2},"p":{"k":3}}}`, xs(elemCondK), unknown},
		// Compound and nested-regex conditions (prepare now descends into Cond).
		{"and cond match", `{"xs":[{"k":1,"j":2}]}`, xs(predicate.And{elemCondK, predicate.Eq{Path: []string{"j"}, Value: 2.0}}), definiteYes},
		{"and cond one leg fails drops", `{"xs":[{"k":1,"j":3}]}`, xs(predicate.And{elemCondK, predicate.Eq{Path: []string{"j"}, Value: 2.0}}), definiteNo},
		{"or cond match", `{"xs":[{"k":9}]}`, xs(predicate.Or{elemCondK, predicate.Eq{Path: []string{"k"}, Value: 9.0}}), definiteYes},
		{"regex cond match", `{"xs":[{"k":"hi"}]}`, xs(predicate.Regex{Path: []string{"k"}, Pattern: "^h"}), definiteYes},
		{"regex cond non-match drops", `{"xs":[{"k":"bye"}]}`, xs(predicate.Regex{Path: []string{"k"}, Pattern: "^h"}), definiteNo},
		{"regex cond non-string unknown", `{"xs":[{"k":5}]}`, xs(predicate.Regex{Path: []string{"k"}, Pattern: "^h"}), unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalNoneMatch(t *testing.T) {
	nm := func(path []string, cond predicate.Node) predicate.NoneMatch {
		return predicate.NoneMatch{Path: path, Cond: cond}
	}
	xs := func(cond predicate.Node) predicate.NoneMatch { return nm([]string{"xs"}, cond) }
	tests := []struct {
		name string
		raw  string
		pred predicate.NoneMatch
		want triple
	}{
		// Array container.
		{"array all fail holds", `{"xs":[{"k":2},{"k":3}]}`, xs(elemCondK), definiteYes},
		{"array clean match drops", `{"xs":[{"k":1},{"k":2}]}`, xs(elemCondK), definiteNo},
		// Object with a repeated key: the first value is shadowed after decoding,
		// so a match in it proves nothing.
		{"object with a repeated key is unknown", `{"xs":{"p":{"k":1},"p":{"k":2}}}`, xs(elemCondK), unknown},
		// The load-bearing exactness case: a match beside an undecidable element must
		// NOT drop, because jq's any short-circuits in order and may error first.
		{"array match plus scalar keeps", `{"xs":[{"k":1},5]}`, xs(elemCondK), unknown},
		{"array fail plus scalar keeps", `{"xs":[{"k":2},5]}`, xs(elemCondK), unknown},
		{"empty array holds", `{"xs":[]}`, xs(elemCondK), definiteYes},
		{"array null element keeps", `{"xs":[null,{"k":1}]}`, xs(elemCondK), unknown},
		{"string element that looks like an object keeps", `{"xs":["{}"]}`, xs(elemCondK), unknown},
		// Object container.
		{"object all fail holds", `{"xs":{"a":{"k":2}}}`, xs(elemCondK), definiteYes},
		{"object clean match drops", `{"xs":{"a":{"k":1},"b":{"k":2}}}`, xs(elemCondK), definiteNo},
		{"object scalar value keeps", `{"xs":{"a":{"k":1},"b":5}}`, xs(elemCondK), unknown},
		// Non-container, absent, ambiguous.
		{"absent field unknown", `{"other":1}`, xs(elemCondK), unknown},
		{"scalar field unknown", `{"xs":5}`, xs(elemCondK), unknown},
		{"nested container path holds", `{"n":{"xs":[{"k":2}]}}`, nm([]string{"n", "xs"}, elemCondK), definiteYes},
		// Nested-regex condition (prepare descends into a NoneMatch Cond too).
		{"regex cond all fail holds", `{"xs":[{"k":"bye"}]}`, xs(predicate.Regex{Path: []string{"k"}, Pattern: "^h"}), definiteYes},
		{"regex cond clean match drops", `{"xs":[{"k":"hi"},{"k":"bye"}]}`, xs(predicate.Regex{Path: []string{"k"}, Pattern: "^h"}), definiteNo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalRegex(t *testing.T) {
	p := func(pattern, flags string) predicate.Regex {
		return predicate.Regex{Path: []string{"s"}, Pattern: pattern, Flags: flags}
	}
	tests := []struct {
		name string
		raw  string
		pred predicate.Regex
		want triple
	}{
		{"plain match", `{"s":"hello"}`, p("ell", ""), definiteYes},
		{"plain non-match drops", `{"s":"hello"}`, p("^z", ""), definiteNo},
		{"unanchored matches midway", `{"s":"abcde"}`, p("cd", ""), definiteYes},
		{"anchor start matches", `{"s":"hello"}`, p("^he", ""), definiteYes},
		{"anchor start non-match drops", `{"s":"ohello"}`, p("^he", ""), definiteNo},
		{"anchor end matches", `{"s":"hello"}`, p("lo$", ""), definiteYes},
		// The pattern runs against the UNESCAPED value: the raw bytes carry a JSON
		// escape that decodes to the character the pattern expects.
		{"escaped quote unescaped before match", `{"s":"a\"b"}`, p(`a"b`, ""), definiteYes},
		{"escaped backslash unescaped before match", `{"s":"c:\\tmp"}`, p(`c:\\tmp`, ""), definiteYes},
		{"unicode escape unescaped before match", `{"s":"café"}`, p("café", ""), definiteYes},
		{"escaped value non-match drops", `{"s":"a\"b"}`, p("^x", ""), definiteNo},
		// i flag: an uppercase document matches a lowercase pattern only with i.
		{"i flag matches differing case", `{"s":"HELLO"}`, p("hello", "i"), definiteYes},
		{"no i flag differing case drops", `{"s":"HELLO"}`, p("hello", ""), definiteNo},
		{"i flag still requires the letters", `{"s":"world"}`, p("hello", "i"), definiteNo},
		// jq m flag is dotall: `.` matches a newline only with m -> (?s).
		{"m flag dot matches newline", "{\"s\":\"a\\nb\"}", p("a.b", "m"), definiteYes},
		{"no m flag dot skips newline drops", "{\"s\":\"a\\nb\"}", p("a.b", ""), definiteNo},
		{"im flags combine case and dotall", "{\"s\":\"A\\nB\"}", p("a.b", "im"), definiteYes},
		// Non-string field types -> unknown (jq test() errors, so the doc must reach
		// the client).
		{"number field unknown", `{"s":5}`, p("5", ""), unknown},
		{"bool field unknown", `{"s":true}`, p("true", ""), unknown},
		{"null field unknown", `{"s":null}`, p("null", ""), unknown},
		{"object field unknown", `{"s":{"k":1}}`, p("k", ""), unknown},
		{"array field unknown", `{"s":["x"]}`, p("x", ""), unknown},
		// Missing, ambiguous -> unknown.
		{"missing field unknown", `{"other":"hello"}`, p("hello", ""), unknown},
		{"ambiguous path unknown", `{"s":5}`, predicate.Regex{Path: []string{"s", "deep"}, Pattern: "x"}, unknown},
		{"root not object unknown", `["hello"]`, p("hello", ""), unknown},
		// A pattern that fails to compile -> unknown (never dropped).
		{"invalid pattern unknown", `{"s":"hello"}`, p("(", ""), unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, tt.raw, tt.pred, tt.want)
		})
	}
}

func TestEvalRegexUnderAndOr(t *testing.T) {
	raw := `{"s":"hello","n":5}`
	reMiss := predicate.Regex{Path: []string{"s"}, Pattern: "^z"} // definiteNo on "hello"
	reHit := predicate.Regex{Path: []string{"s"}, Pattern: "^he"} // definiteYes on "hello"
	eqN := predicate.Eq{Path: []string{"n"}, Value: 5.0}          // definiteYes
	eqNmiss := predicate.Eq{Path: []string{"n"}, Value: 9.0}      // definiteNo
	tests := []struct {
		name string
		pred predicate.Node
		want triple
	}{
		{"and regex non-match propagates no", predicate.And{eqN, reMiss}, definiteNo},
		{"and regex match with yes", predicate.And{eqN, reHit}, definiteYes},
		{"or regex match short circuits yes", predicate.Or{eqNmiss, reHit}, definiteYes},
		{"or regex non-match with no", predicate.Or{eqNmiss, reMiss}, definiteNo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireTriple(t, raw, tt.pred, tt.want)
		})
	}
}

func TestCompileRegex(t *testing.T) {
	t.Run("translation matches gojq", func(t *testing.T) {
		// i -> (?i) case-insensitive; jq m -> (?s) dotall; both prepend, matching is
		// unanchored (MatchString). These assertions pin the exact flag mapping.
		cases := []struct {
			flags   string
			input   string
			pattern string
			want    bool
		}{
			{"", "HELLO", "hello", false},
			{"i", "HELLO", "hello", true},
			{"", "a\nb", "a.b", false},
			{"m", "a\nb", "a.b", true},
			{"im", "A\nB", "a.b", true},
			{"", "abc", "b", true},
		}
		for _, c := range cases {
			re, ok := compileRegex(c.pattern, c.flags)
			require.True(t, ok)
			require.NotNil(t, re)
			require.Equal(t, c.want, re.MatchString(c.input), "pattern %q flags %q on %q", c.pattern, c.flags, c.input)
		}
	})
	t.Run("invalid pattern reports not-ok and returns nil", func(t *testing.T) {
		re, ok := compileRegex("(", "")
		require.False(t, ok)
		require.Nil(t, re)
	})
}

func TestNewMatcherCompileFailureLeavesNodeUncompiled(t *testing.T) {
	// A pattern portableRegex would never emit but that RE2 rejects is left
	// uncompiled: the key is absent, so evalRegex reads it as unknown and the
	// document is kept. A valid pattern is stored.
	bad := predicate.Regex{Path: []string{"s"}, Pattern: "("}
	mBad := NewMatcher(bad)
	_, ok := mBad.regexes[regexKey{pattern: "(", flags: ""}]
	require.False(t, ok, "an uncompilable pattern must not be stored")
	require.Equal(t, MayMatch, mBad.Match([]byte(`{"s":"hello"}`)), "an uncompilable pattern keeps every document")

	good := predicate.Regex{Path: []string{"s"}, Pattern: "^z"}
	mGood := NewMatcher(good)
	re, ok := mGood.regexes[regexKey{pattern: "^z", flags: ""}]
	require.True(t, ok, "a valid pattern is compiled and stored")
	require.NotNil(t, re)
	require.Equal(t, CannotMatch, mGood.Match([]byte(`{"s":"hello"}`)), "a compiled non-matching pattern drops the document")
}

func TestNewMatcherDeduplicatesAndDescendsCond(t *testing.T) {
	// Identical (pattern, flags) share one compilation, and prepare now descends into
	// an ElemMatch and a NoneMatch Cond, compiling nested regexes. The NoneMatch
	// Cond here repeats the top-level (^h, i), so it deduplicates into the same entry
	// rather than adding a third; only the distinct ElemMatch Cond (deep, "") is new.
	pred := predicate.And{
		predicate.Regex{Path: []string{"s"}, Pattern: "^h", Flags: "i"},
		predicate.Regex{Path: []string{"t"}, Pattern: "^h", Flags: "i"},
		predicate.ElemMatch{Path: []string{"xs"}, Cond: predicate.Regex{Path: []string{"u"}, Pattern: "deep"}},
		predicate.NoneMatch{Path: []string{"ys"}, Cond: predicate.Regex{Path: []string{"v"}, Pattern: "^h", Flags: "i"}},
	}
	m := NewMatcher(pred)
	require.Len(t, m.regexes, 2, "identical (pattern, flags) share one entry; the ElemMatch Cond adds a second")
	_, ok := m.regexes[regexKey{pattern: "^h", flags: "i"}]
	require.True(t, ok)
	_, ok = m.regexes[regexKey{pattern: "deep", flags: ""}]
	require.True(t, ok, "a Regex under an ElemMatch Cond is compiled now that prepare descends")

	// Dedup means reuse, not recompilation: preparing the same (pattern, flags)
	// again must leave the stored pointer untouched, which a re-store of an equal
	// but freshly compiled Regexp would not.
	first := m.regexes[regexKey{pattern: "^h", flags: "i"}]
	require.NotNil(t, first)

	m.prepare(predicate.Regex{Path: []string{"w"}, Pattern: "^h", Flags: "i"})

	require.Same(t, first, m.regexes[regexKey{pattern: "^h", flags: "i"}])
	require.Len(t, m.regexes, 2, "a repeat pattern adds no entry")
}

func TestIsObject(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"object", `{"a":1}`, true},
		{"object behind whitespace", " \t\n\r{}", true},
		{"empty", "", false},
		{"whitespace only", " \t\n\r", false},
		{"array holding an object", `[{"a":1}]`, false},
		{"string holding a brace", `"x{"`, false},
		{"number", "5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isObject([]byte(tt.raw)))
		})
	}
}

func TestNumOrd(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		pv      float64
		wantOrd int
		wantOK  bool
	}{
		{"equal ints", "5", 5.0, 0, true},
		{"doc less", "5", 6.0, -1, true},
		{"doc greater", "6", 5.0, 1, true},
		{"negative equal", "-3", -3.0, 0, true},
		{"zero", "0", 0.0, 0, true},
		{"neg zero literal", "-0", 0.0, 0, true},
		{"neg zero pv", "0", math.Copysign(0, -1), 0, true},
		{"fractional pv below", "5", 5.5, -1, true},
		{"fractional pv above", "5", 4.5, 1, true},
		{"large int64 doc with fractional pv", "4611686018427387904", 5.5, 1, true},
		{"fractional doc gives up", "1.5", 1.5, 0, false},
		{"exponent doc gives up", "1e3", 1000.0, 0, false},
		{"big int doc gives up", "10000000000000000001", 5.0, 0, false},
		{"doc at 2^53 plus one gives up", "9007199254740993", 5.0, 0, false},
		{"both at 2^53 equal", "9007199254740992", 9007199254740992.0, 0, true},
		{"both at negative 2^53 equal", "-9007199254740992", -9007199254740992.0, 0, true},
		{"doc at negative 2^53 minus one gives up", "-9007199254740993", -5.0, 0, false},
		{"pv beyond exact gives up", "5", 1e60, 0, false},
		{"pv beyond negative exact gives up", "5", -1e60, 0, false},
		{"pv nan gives up", "5", math.NaN(), 0, false},
		{"pv inf gives up", "5", math.Inf(1), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ord, ok := numOrd([]byte(tt.raw), tt.pv)
			require.Equal(t, tt.wantOK, ok)
			// The ord is asserted even when ok is false: a give-up canonically
			// returns 0, so a mutated non-zero ord on a give-up path is caught.
			require.Equal(t, tt.wantOrd, ord)
		})
	}
}

func TestGetField(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		path      []string
		wantVal   string
		wantType  jsonparser.ValueType
		wantState fieldState
	}{
		{"found scalar", `{"a":5}`, []string{"a"}, "5", jsonparser.Number, fieldFound},
		{"found nested", `{"n":{"x":true}}`, []string{"n", "x"}, "true", jsonparser.Boolean, fieldFound},
		{"absent key", `{"b":1}`, []string{"a"}, "", jsonparser.Unknown, fieldAbsent},
		{"absent nested leaf", `{"n":{"y":1}}`, []string{"n", "x"}, "", jsonparser.Unknown, fieldAbsent},
		{"ambiguous array root", `[1,2]`, []string{"a"}, "", jsonparser.Unknown, fieldAmbiguous},
		{"ambiguous scalar intermediate", `{"n":5}`, []string{"n", "x"}, "", jsonparser.Unknown, fieldAmbiguous},
		{"ambiguous malformed", `{"a":`, []string{"a"}, "", jsonparser.Unknown, fieldAmbiguous},
		{"ambiguous empty path", `{"a":5}`, nil, "", jsonparser.Unknown, fieldAmbiguous},
		{"ambiguous duplicate key", `{"a":1,"a":0}`, []string{"a"}, "", jsonparser.Unknown, fieldAmbiguous},
		{"ambiguous duplicate intermediate", `{"n":{"x":1},"n":{"x":2}}`, []string{"n", "x"}, "", jsonparser.Unknown, fieldAmbiguous},
		{"found beside another key", `{"a":1,"b":0}`, []string{"a"}, "1", jsonparser.Number, fieldFound},
		{"ambiguous duplicate escaped key", `{"\\u0061":0,"\\u0061":1}`, []string{`\u0061`}, "", jsonparser.Unknown, fieldAmbiguous},
		{"found unescaped key beside its escaped form", `{"\\u0061":0,"a":1}`, []string{"a"}, "1", jsonparser.Number, fieldFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, typ, st := getField([]byte(tt.raw), tt.path)
			require.Equal(t, tt.wantState, st, "state")
			require.Equal(t, tt.wantType, typ, "type")
			require.Equal(t, tt.wantVal, string(val), "value")
		})
	}
}

func TestCmpHelpers(t *testing.T) {
	// The helpers are exercised in production only with unequal operands (type
	// ranks never tie, integers never equal a fractional pv), so their equal and
	// boundary cases are asserted directly here to pin the sign contract.
	t.Run("cmpInt", func(t *testing.T) {
		require.Equal(t, -1, cmpInt(1, 3))
		require.Equal(t, 0, cmpInt(3, 3))
		require.Equal(t, 1, cmpInt(4, 3))
	})
	t.Run("cmpInt64", func(t *testing.T) {
		require.Equal(t, -1, cmpInt64(5, 6))
		require.Equal(t, 0, cmpInt64(6, 6))
		require.Equal(t, 1, cmpInt64(7, 6))
	})
	t.Run("cmpFloat", func(t *testing.T) {
		require.Equal(t, -1, cmpFloat(1.5, 2.5))
		require.Equal(t, 0, cmpFloat(2.5, 2.5))
		require.Equal(t, 1, cmpFloat(3.5, 2.5))
	})
}

func TestApplyOp(t *testing.T) {
	tests := []struct {
		op   predicate.Op
		ord  int
		want triple
	}{
		{predicate.Gt, -1, definiteNo},
		{predicate.Gt, 0, definiteNo},
		{predicate.Gt, 1, definiteYes},
		{predicate.Ge, -1, definiteNo},
		{predicate.Ge, 0, definiteYes},
		{predicate.Ge, 1, definiteYes},
		{predicate.Lt, -1, definiteYes},
		{predicate.Lt, 0, definiteNo},
		{predicate.Lt, 1, definiteNo},
		{predicate.Le, -1, definiteYes},
		{predicate.Le, 0, definiteYes},
		{predicate.Le, 1, definiteNo},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("op%d_ord%d", tt.op, tt.ord), func(t *testing.T) {
			require.Equal(t, tt.want, applyOp(tt.op, tt.ord))
		})
	}
}

func TestRanks(t *testing.T) {
	t.Run("docRank", func(t *testing.T) {
		cases := []struct {
			typ  jsonparser.ValueType
			rank int
			ok   bool
		}{
			{jsonparser.Null, nullRank, true},
			{jsonparser.Boolean, boolRank, true},
			{jsonparser.Number, numberRank, true},
			{jsonparser.String, stringRank, true},
			{jsonparser.Array, arrayRank, true},
			{jsonparser.Object, objectRank, true},
			{jsonparser.Unknown, 0, false},
			{jsonparser.NotExist, 0, false},
		}
		// No rank is the zero value, so a docRank that returns 0 is observably wrong.
		for _, r := range []int{nullRank, boolRank, numberRank, stringRank, arrayRank, objectRank} {
			require.NotZero(t, r)
		}
		// The ranks must be strictly ordered null<bool<number<string<array<object.
		require.True(t, nullRank < boolRank && boolRank < numberRank &&
			numberRank < stringRank && stringRank < arrayRank && arrayRank < objectRank)
		for _, c := range cases {
			r, ok := docRank(c.typ)
			require.Equal(t, c.ok, ok, c.typ.String())
			require.Equal(t, c.rank, r, c.typ.String())
		}
	})
	t.Run("valueRank", func(t *testing.T) {
		r, ok := valueRank(1.5)
		require.True(t, ok)
		require.Equal(t, numberRank, r)
		r, ok = valueRank("x")
		require.True(t, ok)
		require.Equal(t, stringRank, r)
		r, ok = valueRank(true)
		require.False(t, ok)
		require.Zero(t, r)
		r, ok = valueRank(nil)
		require.False(t, ok)
		require.Zero(t, r)
	})
}

func TestCountKeys(t *testing.T) {
	// A count that is not usable comes back as 0 with ok=false.
	tests := []struct {
		name      string
		raw       string
		wantCount int
		wantOK    bool
	}{
		{"empty object", `{}`, 0, true},
		{"two keys", `{"a":1,"b":2}`, 2, true},
		{"repeated key", `{"a":1,"a":2}`, 0, false},
		{"malformed object", `{"a":}`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, ok := countKeys([]byte(tt.raw))
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantCount, count)
		})
	}
}

func TestMatchVerdict(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		pred predicate.Node
		want Verdict
	}{
		{"provable non-match drops", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: 6.0}, CannotMatch},
		{"match is kept", `{"a":5}`, predicate.Eq{Path: []string{"a"}, Value: 5.0}, MayMatch},
		{"unknown is kept", `{"a":1.5}`, predicate.Eq{Path: []string{"a"}, Value: 1.5}, MayMatch},
		{"nil predicate is kept", `{"a":5}`, nil, MayMatch},
		// A cleanly-missing key on an object is jq's null: an Eq to a scalar and an
		// Exists both provably fail, so both drop. This exercises the fieldAbsent
		// classification branch through to the Verdict.
		{"missing key eq scalar drops", `{"b":1}`, predicate.Eq{Path: []string{"absent"}, Value: "x"}, CannotMatch},
		{"exists on missing key drops", `{"b":1}`, predicate.Exists{Path: []string{"absent"}}, CannotMatch},
		{"missing key null eq kept", `{"b":1}`, predicate.Eq{Path: []string{"absent"}, Value: nil}, MayMatch},
		// An ambiguous path (a structural mismatch) is never provable, so it is kept.
		{"ambiguous path kept", `{"a":5}`, predicate.Eq{Path: []string{"a", "b"}, Value: "x"}, MayMatch},
		// A JSON-null field ranks below every number in jq, so `> 5` provably fails;
		// this drives docRank's null case (rank and ok) to the Verdict.
		{"null field gt number drops", `{"a":null}`, predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0}, CannotMatch},
		{"null field lt number kept", `{"a":null}`, predicate.Cmp{Path: []string{"a"}, Op: predicate.Lt, Value: 5.0}, MayMatch},
		{"and drop", `{"a":5,"b":1}`, predicate.And{
			predicate.Eq{Path: []string{"a"}, Value: 5.0},
			predicate.Eq{Path: []string{"b"}, Value: 2.0},
		}, CannotMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Match([]byte(tt.raw), tt.pred))
		})
	}
}
