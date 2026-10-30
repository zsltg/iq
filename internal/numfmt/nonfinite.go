package numfmt

import (
	"math"
	"strconv"
	"strings"
)

// FiniteFloat returns a float that JSON can encode. A finite float comes back
// unchanged, with false. JSON has no form for an infinity or NaN, so FiniteFloat
// returns a string and true. NaN becomes "NaN", +Inf becomes "Infinity", and
// -Inf becomes "-Infinity". These are the texts that Cassandra, MongoDB, Neo4j
// and HBase print for these values. FiniteScore keeps the Redis texts.
func FiniteFloat(f float64) (any, bool) {
	switch {
	case math.IsNaN(f):
		return "NaN", true
	case math.IsInf(f, 1):
		return "Infinity", true
	case math.IsInf(f, -1):
		return "-Infinity", true
	default:
		return f, false
	}
}

// segment is one step in the path of a nested value: a field name or a list
// index.
type segment struct {
	name    string
	index   int
	isIndex bool
}

// Hits records the fields that Float converted in one record. A normalize
// function walks a value once. It calls Enter and Leave around each child, and
// Float for each float. So Hits knows the path of every conversion without a
// second walk. A nil *Hits is valid. It converts and records nothing.
type Hits struct {
	path []segment
	// Fields holds the dotted path of each converted float, in walk order. A
	// list index is written as [i]. The record that was walked has no field
	// when Fields is empty.
	Fields []string
}

// Enter adds a field name to the current path.
func (h *Hits) Enter(name string) {
	if h != nil {
		h.path = append(h.path, segment{name: name})
	}
}

// EnterIndex adds a list index to the current path.
func (h *Hits) EnterIndex(i int) {
	if h != nil {
		h.path = append(h.path, segment{index: i, isIndex: true})
	}
}

// Leave removes the last step from the current path.
func (h *Hits) Leave() {
	if h != nil && len(h.path) > 0 {
		h.path = h.path[:len(h.path)-1]
	}
}

// Float returns f as FiniteFloat does. When it converts f, it also records the
// current path.
func (h *Hits) Float(f float64) any {
	v, converted := FiniteFloat(f)
	if converted && h != nil {
		h.Fields = append(h.Fields, h.dotted())
	}
	return v
}

// Score returns s as FiniteScore does, with the Redis texts. When it converts
// s, it also records the current path.
func (h *Hits) Score(s float64) any {
	v := FiniteScore(s)
	if _, converted := v.(string); converted && h != nil {
		h.Fields = append(h.Fields, h.dotted())
	}
	return v
}

// dotted writes the current path as text.
func (h *Hits) dotted() string {
	var b strings.Builder
	for i, s := range h.path {
		switch {
		case s.isIndex:
			b.WriteString("[" + strconv.Itoa(s.index) + "]")
		case i > 0:
			b.WriteString("." + s.name)
		default:
			b.WriteString(s.name)
		}
	}
	return b.String()
}
