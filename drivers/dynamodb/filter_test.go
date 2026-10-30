package dynamodb

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

func TestCompilePushable(t *testing.T) {
	tests := []struct {
		name        string
		pred        predicate.Node
		wantDisplay string
		wantNames   int
		wantValues  int
		// wantBound, when set, is the exact attribute value bound to :v1, so a
		// placeholder can never be defined with an empty or nil payload.
		wantBound types.AttributeValue
	}{
		{
			name:        "eq string",
			pred:        predicate.Eq{Path: []string{"author"}, Value: "kleppmann"},
			wantDisplay: "author = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberS{Value: "kleppmann"},
		},
		{
			name:        "eq number",
			pred:        predicate.Eq{Path: []string{"year"}, Value: 2017.0},
			wantDisplay: "year = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberN{Value: "2017"},
		},
		{
			name:        "eq bool",
			pred:        predicate.Eq{Path: []string{"inprint"}, Value: true},
			wantDisplay: "inprint = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberBOOL{Value: true},
		},
		{
			name:        "eq false binds the false boolean",
			pred:        predicate.Eq{Path: []string{"inprint"}, Value: false},
			wantDisplay: "inprint = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberBOOL{Value: false},
		},
		{
			name:        "eq empty string binds the empty string",
			pred:        predicate.Eq{Path: []string{"author"}, Value: ""},
			wantDisplay: "author = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberS{Value: ""},
		},
		{
			name:        "eq int binds a number",
			pred:        predicate.Eq{Path: []string{"year"}, Value: 2017},
			wantDisplay: "year = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberN{Value: "2017"},
		},
		{
			name:        "eq int64 binds a number",
			pred:        predicate.Eq{Path: []string{"year"}, Value: int64(2017)},
			wantDisplay: "year = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberN{Value: "2017"},
		},
		{
			name:        "exists",
			pred:        predicate.Exists{Path: []string{"tags"}},
			wantDisplay: "attribute_exists(tags)",
			wantNames:   1,
			wantValues:  0,
		},
		{
			name:        "not exists",
			pred:        predicate.NotExists{Path: []string{"tags"}},
			wantDisplay: "attribute_not_exists(tags)",
			wantNames:   1,
			wantValues:  0,
		},
		{
			name: "and of two eq",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: "y"},
			},
			wantDisplay: "(a = ? AND b = ?)",
			wantNames:   2,
			wantValues:  2,
		},
		{
			name: "and drops the unpushable conjunct without orphan placeholders",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantDisplay: "a = ?",
			wantNames:   1,
			wantValues:  1,
		},
		{
			name:        "or of one branch still compiles",
			pred:        predicate.Or{predicate.Eq{Path: []string{"a"}, Value: "x"}},
			wantDisplay: "a = ?",
			wantNames:   1,
			wantValues:  1,
			wantBound:   &types.AttributeValueMemberS{Value: "x"},
		},
		{
			name: "and keeps the conjuncts after a leading unpushable one",
			pred: predicate.And{
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: "y"},
			},
			wantDisplay: "(a = ? AND b = ?)",
			wantNames:   2,
			wantValues:  2,
		},
		{
			name: "or of two eq",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: "y"},
			},
			wantDisplay: "(a = ? OR b = ?)",
			wantNames:   2,
			wantValues:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := compile(tt.pred, numfmt.DecimalNumber)
			require.True(t, ok)
			require.Equal(t, tt.wantDisplay, f.display)
			// Both maps are always allocated: DynamoDB is handed them verbatim, and a
			// nil map is not the same request as an empty one.
			require.NotNil(t, f.names)
			require.NotNil(t, f.values)
			require.Len(t, f.names, tt.wantNames)
			require.Len(t, f.values, tt.wantValues)
			if tt.wantBound != nil {
				require.Equal(t, tt.wantBound, f.values[":v1"])
			}
			// Every placeholder used in the expression is defined, and none is orphaned
			// (DynamoDB rejects unused ExpressionAttributeNames/Values).
			for ph := range f.names {
				require.Contains(t, f.expr, ph)
			}
			for ph := range f.values {
				require.Contains(t, f.expr, ph)
			}
		})
	}
}

func TestCompileNotPushable(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
	}{
		{"range comparison never pushes", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}},
		{"nested path", predicate.Eq{Path: []string{"author", "name"}, Value: "x"}},
		{"eq nil under-returns", predicate.Eq{Path: []string{"x"}, Value: nil}},
		{"not-equal never pushes", predicate.Ne{Path: []string{"x"}, Value: "y"}},
		{"regex never pushes", predicate.Regex{Path: []string{"x"}, Pattern: "^a"}},
		{"size never pushes", predicate.Size{Path: []string{"x"}, N: 3}},
		{"empty or", predicate.Or{}},
		{"or with one unpushable branch drops whole", predicate.Or{
			predicate.Eq{Path: []string{"a"}, Value: "x"},
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
		}},
		{"and all unpushable", predicate.And{
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := compile(tt.pred, numfmt.DecimalNumber)
			require.False(t, ok)
		})
	}
}

func TestCompileExprShape(t *testing.T) {
	// The execution expression uses aliased placeholders for the value and, for a
	// single equality, references the same index for name and value.
	f, ok := compile(predicate.Eq{Path: []string{"author"}, Value: "x"}, numfmt.DecimalNumber)
	require.True(t, ok)
	require.Equal(t, "#n1 = :v1", f.expr)
	require.Equal(t, map[string]string{"#n1": "author"}, f.names)
	require.Contains(t, f.values, ":v1")
}

// TestCompileWidensEqualityThatHidesDecimals pins the filter per decimal mode. A
// fractional N decodes to a jq string in auto and string mode. The server compares
// the stored N as a number. So an equality with a number text must also return the
// N attributes. The driver pushes no lower bound and no regex, so the equality is
// the only affected leaf.
func TestCompileWidensEqualityThatHidesDecimals(t *testing.T) {
	price := []string{"price"}
	eq := predicate.Eq{Path: price, Value: "9.99"}
	typeN := &types.AttributeValueMemberS{Value: "N"}
	tests := []struct {
		name        string
		pred        predicate.Node
		mode        numfmt.DecimalMode
		wantExpr    string
		wantDisplay string
		wantNames   map[string]string
		wantValues  map[string]types.AttributeValue
	}{
		{
			name: "eq number text, auto", pred: eq, mode: numfmt.DecimalAuto,
			wantExpr:    "(#n1 = :v1 OR attribute_type(#n1, :v2))",
			wantDisplay: "(price = ? OR attribute_type(price, N))",
			wantNames:   map[string]string{"#n1": "price"},
			wantValues:  map[string]types.AttributeValue{":v1": &types.AttributeValueMemberS{Value: "9.99"}, ":v2": typeN},
		},
		{
			name: "eq number text, string", pred: eq, mode: numfmt.DecimalString,
			wantExpr:    "(#n1 = :v1 OR attribute_type(#n1, :v2))",
			wantDisplay: "(price = ? OR attribute_type(price, N))",
			wantNames:   map[string]string{"#n1": "price"},
			wantValues:  map[string]types.AttributeValue{":v1": &types.AttributeValueMemberS{Value: "9.99"}, ":v2": typeN},
		},
		{
			name: "eq number text, number", pred: eq, mode: numfmt.DecimalNumber,
			wantExpr:    "#n1 = :v1",
			wantDisplay: "price = ?",
			wantNames:   map[string]string{"#n1": "price"},
			wantValues:  map[string]types.AttributeValue{":v1": &types.AttributeValueMemberS{Value: "9.99"}},
		},
		{
			name: "eq plain text, auto", pred: predicate.Eq{Path: price, Value: "apple"}, mode: numfmt.DecimalAuto,
			wantExpr:    "#n1 = :v1",
			wantDisplay: "price = ?",
			wantNames:   map[string]string{"#n1": "price"},
			wantValues:  map[string]types.AttributeValue{":v1": &types.AttributeValueMemberS{Value: "apple"}},
		},
		{
			name: "eq number, auto", pred: predicate.Eq{Path: price, Value: 9.99}, mode: numfmt.DecimalAuto,
			wantExpr:    "#n1 = :v1",
			wantDisplay: "price = ?",
			wantNames:   map[string]string{"#n1": "price"},
			wantValues:  map[string]types.AttributeValue{":v1": &types.AttributeValueMemberN{Value: "9.99"}},
		},
		{
			name: "or keeps unique placeholders, auto",
			pred: predicate.Or{eq, predicate.Eq{Path: []string{"name"}, Value: "ada"}}, mode: numfmt.DecimalAuto,
			wantExpr:    "((#n1 = :v1 OR attribute_type(#n1, :v2)) OR #n3 = :v3)",
			wantDisplay: "((price = ? OR attribute_type(price, N)) OR name = ?)",
			wantNames:   map[string]string{"#n1": "price", "#n3": "name"},
			wantValues: map[string]types.AttributeValue{
				":v1": &types.AttributeValueMemberS{Value: "9.99"},
				":v2": typeN,
				":v3": &types.AttributeValueMemberS{Value: "ada"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := compile(tt.pred, tt.mode)
			require.True(t, ok)
			require.Equal(t, tt.wantExpr, f.expr)
			require.Equal(t, tt.wantDisplay, f.display)
			require.Equal(t, tt.wantNames, f.names)
			require.Equal(t, tt.wantValues, f.values)
		})
	}
}

// TestCompileNeverPushesRangeOrRegexOnDecimals pins why only the equality needs a
// widening: a lower bound and a regex stay out of the filter in every mode.
func TestCompileNeverPushesRangeOrRegexOnDecimals(t *testing.T) {
	price := []string{"price"}
	nodes := map[string]predicate.Node{
		"gt":    predicate.Cmp{Path: price, Op: predicate.Gt, Value: 1000.0},
		"regex": predicate.Regex{Path: price, Pattern: "^9"},
	}
	for name, node := range nodes {
		for _, mode := range []numfmt.DecimalMode{numfmt.DecimalAuto, numfmt.DecimalString, numfmt.DecimalNumber} {
			t.Run(name, func(t *testing.T) {
				_, ok := compile(node, mode)
				require.False(t, ok)
			})
		}
	}
}
