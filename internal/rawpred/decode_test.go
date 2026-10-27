package rawpred_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
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

// TestMatcherKeepsNumberTokensThatDecodeToStrings pins the rule. A number token that
// the store decodes to a jq string is never a definite non-match. This holds for every
// comparison operator, every mode, and string and number operands.
func TestMatcherKeepsNumberTokensThatDecodeToStrings(t *testing.T) {
	for _, m := range decodeModes {
		for _, tok := range decodeTokens {
			if !numfmt.DecodesToString(tok, m.mode) {
				continue
			}
			raw := fmt.Sprintf(`{"a":%s}`, tok)
			for _, op := range decodeOps {
				for _, operand := range decodeOperands {
					filter := fmt.Sprintf(`.[] | select(.a %s %s)`, op, operand)
					t.Run(m.name+"/"+tok+"/"+op+operand, func(t *testing.T) {
						pred, err := jqtruth.Predicate(filter)
						if err != nil {
							t.Skip("the filter does not compile to a predicate")
						}
						got := rawpred.NewMatcher(pred, m.mode).Match([]byte(raw))
						require.Equal(t, rawpred.MayMatch, got)
					})
				}
			}
		}
	}
}

// TestMatcherNeverDropsWhatTheDecodedDocumentKeeps compares the matcher with the full
// jq filter on the decoded document, for every token, mode, operator, and operand.
func TestMatcherNeverDropsWhatTheDecodedDocumentKeeps(t *testing.T) {
	for _, m := range decodeModes {
		for _, tok := range decodeTokens {
			raw := fmt.Sprintf(`{"a":%s}`, tok)
			doc := decodeLikeStore(t, raw, m.mode)
			for _, op := range decodeOps {
				for _, operand := range decodeOperands {
					filter := fmt.Sprintf(`.[] | select(.a %s %s)`, op, operand)
					t.Run(m.name+"/"+tok+"/"+op+operand, func(t *testing.T) {
						pred, err := jqtruth.Predicate(filter)
						if err != nil {
							t.Skip("the filter does not compile to a predicate")
						}
						keeps, err := jqtruth.Keeps(filter, doc)
						require.NoError(t, err)
						got := rawpred.NewMatcher(pred, m.mode).Match([]byte(raw))
						require.Falsef(t, keeps && got == rawpred.CannotMatch,
							"dropped a document that jq keeps: %s on %s", filter, raw)
					})
				}
			}
		}
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
