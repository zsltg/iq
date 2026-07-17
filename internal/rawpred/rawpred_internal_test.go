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

// requireTriple asserts eval(raw, node) equals want, naming both sides.
func requireTriple(t *testing.T, raw string, node predicate.Node, want triple) {
	t.Helper()
	got := eval([]byte(raw), node)
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

func TestEvalUnhandledNodes(t *testing.T) {
	raw := `{"a":[1,2,3]}`
	nodes := []predicate.Node{
		predicate.Regex{Path: []string{"a"}, Pattern: "x"},
		predicate.Size{Path: []string{"a"}, N: 3},
		predicate.ElemMatch{Path: []string{"a"}, Cond: predicate.Eq{Path: []string{"x"}, Value: 1.0}},
		predicate.NoneMatch{Path: []string{"a"}, Cond: predicate.Eq{Path: []string{"x"}, Value: 1.0}},
	}
	for _, n := range nodes {
		t.Run(fmt.Sprintf("%T", n), func(t *testing.T) {
			requireTriple(t, raw, n, unknown)
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
		_, ok = valueRank(true)
		require.False(t, ok)
		_, ok = valueRank(nil)
		require.False(t, ok)
	})
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
