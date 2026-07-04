// Package predicate is the backend-neutral representation of a pushable filter
// predicate. A jq compiler (internal/pushdown) builds one from a select(...)
// expression, and a backend adapter translates it into that store's own query
// language. Keeping it neutral lets the jq→predicate extraction and the
// predicate→query translation evolve independently, and lets one predicate serve
// every backend.
//
// A predicate is always a conservative *superset* of the jq expression it came
// from: a store may return extra documents, never fewer, because the caller
// re-runs the full jq client-side over whatever is fetched.
package predicate

// Node is one node of a boolean predicate tree.
type Node interface {
	node()
}

// Eq matches documents whose field at Path equals Value. Path is the field path
// from the document root (`.author.name` → ["author", "name"]). Value is a JSON
// scalar: string, float64, bool, or nil.
type Eq struct {
	Path  []string
	Value any
}

// Op is a range comparison operator.
type Op int

// The range operators. Value is always on the right: Cmp{Op: Gt} means
// `field > Value`.
const (
	Gt Op = iota // >
	Ge           // >=
	Lt           // <
	Le           // <=
)

// Cmp matches documents whose field at Path compares to Value under Op, using
// jq's total type ordering (null < bool < number < string < array < object).
// Value is a number (float64) or a string; the ordering across types is what a
// backend must reproduce to stay a superset.
type Cmp struct {
	Path  []string
	Op    Op
	Value any
}

// And matches documents satisfying every child. An empty And matches everything.
type And []Node

// Or matches documents satisfying any child.
type Or []Node

func (Eq) node()  {}
func (Cmp) node() {}
func (And) node() {}
func (Or) node()  {}
