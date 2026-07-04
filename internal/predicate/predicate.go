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

// Regex matches documents whose field at Path is a string the Pattern matches
// (unanchored, like jq's test). Flags is a subset of "ims". Only patterns whose
// meaning is identical across regex engines are ever put here, so a backend can
// hand Pattern to its own engine without changing which documents match.
type Regex struct {
	Path    []string
	Pattern string
	Flags   string
}

// Exists matches documents whose field at Path is present (regardless of value),
// the analogue of jq's has(). It is exact: key presence means the same to jq and
// to a store.
type Exists struct {
	Path []string
}

// Size matches documents whose field at Path has jq length N. jq's length is
// polymorphic (array elements, string characters, object keys, |number|,
// null→0), so a backend must not treat this as an array-only test; it is a
// superset in the same spirit as Cmp.
type Size struct {
	Path []string
	N    int
}

// Ne matches documents whose field at Path is not the scalar Value — the exact
// negation of Eq (jq's !=). Unlike Eq it must be exact, not a superset, because
// re-running jq cannot recover a document a negation wrongly excluded.
type Ne struct {
	Path  []string
	Value any
}

// NotExists matches documents lacking the field at Path (`has(...) | not`).
type NotExists struct {
	Path []string
}

// NoneMatch matches documents whose array at Path has no element satisfying Cond
// (`.path | any(cond) | not`). Cond is an exact equality predicate (Eq, or And/Or
// of Eq), so the negation does not silently drop documents.
type NoneMatch struct {
	Path []string
	Cond Node
}

// ElemMatch matches documents whose array at Path has at least one element
// satisfying Cond, the analogue of jq's `.path | any(cond)`. Cond is a predicate
// over the element's own fields. Because jq's any also iterates an object's
// values, a backend must treat an object at Path as a possible match too.
type ElemMatch struct {
	Path []string
	Cond Node
}

// And matches documents satisfying every child. An empty And matches everything.
type And []Node

// Or matches documents satisfying any child.
type Or []Node

func (Eq) node()        {}
func (Cmp) node()       {}
func (Regex) node()     {}
func (Exists) node()    {}
func (Size) node()      {}
func (ElemMatch) node() {}
func (Ne) node()        {}
func (NotExists) node() {}
func (NoneMatch) node() {}
func (And) node()       {}
func (Or) node()        {}
