package rawpred_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// decodeTokens are number tokens that decode differently by mode. The first six are
// fractional or out of range. 7 and the big integer decode to an int and a *big.Int.
var decodeTokens = []string{"1.5", "1.50", "-0.5", "1e3", "2e308", "-1e999", "7", "123456789012345678901234567890"}

// decodeOperands are the right-hand sides of the filters, with string and number kinds.
var decodeOperands = []string{`"1.5"`, `"1"`, `"2e308"`, `"-1e999"`, `"x"`, `1.5`, `0`, `7`, `null`, `true`}

var decodeOps = []string{"==", "!=", "<", "<=", ">", ">="}

var decodeModes = []struct {
	name string
	mode numfmt.DecimalMode
}{
	{"auto", numfmt.DecimalAuto},
	{"number", numfmt.DecimalNumber},
	{"string", numfmt.DecimalString},
}

// decodeLikeStore decodes raw as a document store does: UseNumber, then ConvertNumbers.
func decodeLikeStore(t *testing.T, raw string, mode numfmt.DecimalMode) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return numfmt.ConvertNumbers(v, mode)
}

// decodeCase is one document and one filter, with the decimal mode of the store.
type decodeCase struct {
	name   string
	mode   numfmt.DecimalMode
	raw    string
	filter string
}

// decodeCases lists a case for every mode, token, operator, and operand. A case is
// kept when want returns true for its mode and token.
func decodeCases(want func(mode numfmt.DecimalMode, token string) bool) []decodeCase {
	var cases []decodeCase
	for _, m := range decodeModes {
		for _, tok := range decodeTokens {
			if !want(m.mode, tok) {
				continue
			}
			for _, op := range decodeOps {
				for _, operand := range decodeOperands {
					cases = append(cases, decodeCase{
						name:   m.name + "/" + tok + "/" + op + operand,
						mode:   m.mode,
						raw:    fmt.Sprintf(`{"a":%s}`, tok),
						filter: fmt.Sprintf(`.[] | select(.a %s %s)`, op, operand),
					})
				}
			}
		}
	}
	return cases
}

// matchDecodeCase runs the matcher on the case. It skips the test when the filter does
// not compile to a predicate.
func matchDecodeCase(t *testing.T, c decodeCase) rawpred.Verdict {
	t.Helper()
	pred, err := jqtruth.Predicate(c.filter)
	if err != nil {
		t.Skip("the filter does not compile to a predicate")
	}
	return rawpred.NewMatcher(pred, c.mode).Match([]byte(c.raw))
}

// TestMatcherKeepsNumberTokensThatDecodeToStrings pins the rule. A number token that
// the store decodes to a jq string is never a definite non-match. This holds for every
// comparison operator, every mode, and string and number operands.
func TestMatcherKeepsNumberTokensThatDecodeToStrings(t *testing.T) {
	cases := decodeCases(func(mode numfmt.DecimalMode, tok string) bool {
		return numfmt.DecodesToString(tok, mode)
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, rawpred.MayMatch, matchDecodeCase(t, c))
		})
	}
}

// TestMatcherNeverDropsWhatTheDecodedDocumentKeeps compares the matcher with the full
// jq filter on the decoded document, for every token, mode, operator, and operand.
func TestMatcherNeverDropsWhatTheDecodedDocumentKeeps(t *testing.T) {
	cases := decodeCases(func(numfmt.DecimalMode, string) bool { return true })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchDecodeCase(t, c)
			keeps, err := jqtruth.Keeps(c.filter, decodeLikeStore(t, c.raw, c.mode))
			require.NoError(t, err)
			require.Falsef(t, keeps && got == rawpred.CannotMatch,
				"dropped a document that jq keeps: %s on %s", c.filter, c.raw)
		})
	}
}

// TestMatcherKeepsOrWithDecodedToken pins that a decoded token inside an Or stays
// unknown. A provable no from the other branch must not turn it into a drop.
func TestMatcherKeepsOrWithDecodedToken(t *testing.T) {
	absent := predicate.Eq{Path: []string{"b"}, Value: "x"}
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		leaf predicate.Node
		want rawpred.Verdict
	}{
		{"eq, string mode", numfmt.DecimalString, predicate.Eq{Path: []string{"a"}, Value: "1.5"}, rawpred.MayMatch},
		{"cmp, string mode", numfmt.DecimalString, predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: "z"}, rawpred.MayMatch},
		{"eq, auto mode", numfmt.DecimalAuto, predicate.Eq{Path: []string{"a"}, Value: "1.5"}, rawpred.CannotMatch},
		{"cmp, auto mode", numfmt.DecimalAuto, predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: "z"}, rawpred.CannotMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := rawpred.NewMatcher(predicate.Or{tt.leaf, absent}, tt.mode)
			require.Equal(t, tt.want, m.Match([]byte(`{"a":1.5,"b":"y"}`)))
		})
	}
}

// TestMatcherStillDropsNumbersThatStayNumbers pins that the rule costs nothing for a
// token that decodes to a number. An integer never equals a string.
func TestMatcherStillDropsNumbersThatStayNumbers(t *testing.T) {
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		raw  string
		want rawpred.Verdict
	}{
		{"int against string, auto", numfmt.DecimalAuto, `{"a":7}`, rawpred.CannotMatch},
		{"int against string, string", numfmt.DecimalString, `{"a":7}`, rawpred.CannotMatch},
		{"big int against string", numfmt.DecimalAuto, `{"a":123456789012345678901234567890}`, rawpred.CannotMatch},
		{"fraction against string, auto", numfmt.DecimalAuto, `{"a":1.5}`, rawpred.CannotMatch},
		{"fraction against string, number", numfmt.DecimalNumber, `{"a":1.5}`, rawpred.CannotMatch},
		{"fraction against string, string", numfmt.DecimalString, `{"a":1.5}`, rawpred.MayMatch},
		{"out of range against string, auto", numfmt.DecimalAuto, `{"a":2e308}`, rawpred.MayMatch},
	}
	pred, err := jqtruth.Predicate(`.[] | select(.a == "x")`)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, rawpred.NewMatcher(pred, tt.mode).Match([]byte(tt.raw)))
		})
	}
}
