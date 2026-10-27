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
	default:
		return false
	}
}
