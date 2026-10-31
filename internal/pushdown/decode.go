package pushdown

import (
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// HidesDecodedNumber reports whether a server that compares stored numbers as
// numbers can drop a row that the jq filter keeps, for the predicate leaf n. A
// store that holds JSON numbers on the server can decode some of them to jq
// strings. Then jq compares a string where the server compares a number. The
// driver must not push such a leaf.
//
// The leaves are an equality with a string that equals a decoded number text, a
// lower bound (jq orders every string above every number), and a regex (a regex
// matches a string and never a number). A lower bound and a regex are affected only
// in string mode. In the other modes only a number literal that float64 cannot hold
// decodes to a string. The function does not see such a literal, so a lower bound
// and a regex on that rare literal stay pushed. Every other node returns false.
func HidesDecodedNumber(n predicate.Node, mode numfmt.DecimalMode) bool {
	switch t := n.(type) {
	case predicate.Eq:
		s, ok := t.Value.(string)
		return ok && numfmt.IsDecodedText(s, mode)
	case predicate.Cmp:
		return mode == numfmt.DecimalString && (t.Op == predicate.Gt || t.Op == predicate.Ge)
	case predicate.Regex:
		return mode == numfmt.DecimalString
	}
	return false
}

// HidesDecimalNumber reports whether a server that compares a stored decimal as a
// number can drop a row that the jq filter keeps, for the predicate leaf n. A store
// with an exact decimal type (a MongoDB Decimal128, a DynamoDB fractional N) decodes
// every such value to a jq string in auto and string mode. In number mode it decodes
// to a float64, and this function reports false. See HidesDecimal for that case.
//
// The leaves are an equality with a string that has the text of a number, a lower
// bound (jq orders every string above every number), and a regex (a regex matches a
// string and never a number). Every other node returns false. The driver keeps such
// a leaf but widens it, so the rows that hold a decimal come back. The client-side
// filter then decides them.
func HidesDecimalNumber(n predicate.Node, mode numfmt.DecimalMode) bool {
	if mode == numfmt.DecimalNumber {
		return false
	}
	switch t := n.(type) {
	case predicate.Eq:
		s, ok := t.Value.(string)
		return ok && isDecimalText(s)
	case predicate.Cmp:
		return t.Op == predicate.Gt || t.Op == predicate.Ge
	case predicate.Regex:
		return true
	}
	return false
}

// isDecimalText reports whether s is a text that a decimal decodes to: the text of a
// number, or NaN, Infinity, or -Infinity, which a MongoDB Decimal128 can hold.
func isDecimalText(s string) bool {
	switch s {
	case "NaN", "Infinity", "-Infinity":
		return true
	}
	return numfmt.IsNumberText(s)
}

// HidesDecimal reports whether a server with an exact decimal type (a MongoDB
// Decimal128) can drop a row that the jq filter keeps, for the predicate leaf n.
// It covers the leaves of HidesDecimalNumber. It also covers number mode, where a
// decimal decodes to a float64 and jq compares numbers. The server does not equate
// a decimal with the closest double: the Decimal128 9.99 is not the double 9.99. So
// an equality with a number and every comparison with a number can miss a decimal
// that jq keeps. The driver keeps such a leaf but widens it, as for
// HidesDecimalNumber.
func HidesDecimal(n predicate.Node, mode numfmt.DecimalMode) bool {
	if mode != numfmt.DecimalNumber {
		return HidesDecimalNumber(n, mode)
	}
	switch t := n.(type) {
	case predicate.Eq:
		return isNumberLiteral(t.Value)
	case predicate.Cmp:
		return isNumberLiteral(t.Value)
	}
	return false
}

// isNumberLiteral reports whether v is a number that the jq compiler emits as a
// predicate literal.
func isNumberLiteral(v any) bool {
	switch v.(type) {
	case float64, int, int64:
		return true
	}
	return false
}
