// Package predicate is the backend-neutral representation of a pushable filter
// predicate. A jq compiler (internal/pushdown) builds one from a select(...)
// expression, and a backend adapter translates it into that store's own query language.
// The neutral form lets the jq-to-predicate extraction and the predicate-to-query
// translation change independently, and lets one predicate serve every backend.
//
// A predicate is always a conservative *superset* of the jq expression it came from: a
// store can return extra documents, never fewer. See query.FilteredScanner.
package predicate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Node is one node of a boolean predicate tree.
type Node interface {
	node()
	// String returns the node in one line of neutral notation. It is not jq
	// syntax and not the syntax of a backend.
	String() string
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

// Regex matches documents whose field at Path is a string that Pattern matches
// (unanchored, like jq's test). Flags is a subset of "im" with jq's meaning: i is
// case-insensitive and m is "dot matches newline" (dotall). jq's m is not PCRE's
// line-anchor multiline. Only patterns with identical meaning, or a superset, across
// regex engines are put here. A backend hands Pattern to its own engine and translates
// Flags to that engine's spelling (for example jq m to PCRE s), so the matched set
// never narrows against jq.
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
// polymorphic (array elements, string characters, object keys, |number|, null→0), so a
// backend must not treat this as an array-only test. It is a superset in the same way
// as Cmp.
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

// identifier matches a field name that renders without quotes.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pathString joins the field names with dots. A name that is not a plain
// identifier renders as a JSON string. An empty path renders as @, the current
// array element inside any or none.
func pathString(path []string) string {
	if len(path) == 0 {
		return "@"
	}
	parts := make([]string, len(path))
	for i, name := range path {
		if identifier.MatchString(name) {
			parts[i] = name
		} else {
			parts[i] = valueString(name)
		}
	}
	return strings.Join(parts, ".")
}

// valueString renders v as a JSON literal. A whole float64 prints with no
// fraction. A value that JSON cannot encode (NaN, for example) prints with %v.
func valueString(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// escapeSlash escapes each / in a regex pattern as \/, so that a / never ends
// the literal. A backslash and the character after it stay as they are, so an
// already escaped \/ is not escaped twice.
func escapeSlash(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			b.WriteByte('\\')
			if i+1 < len(pattern) {
				i++
				b.WriteByte(pattern[i])
			}
		case '/':
			b.WriteString(`\/`)
		default:
			b.WriteByte(pattern[i])
		}
	}
	return b.String()
}

// String returns the operator as written in jq.
func (o Op) String() string {
	switch o {
	case Gt:
		return ">"
	case Ge:
		return ">="
	case Lt:
		return "<"
	case Le:
		return "<="
	default:
		return fmt.Sprintf("Op(%d)", int(o))
	}
}

// String renders `author.name == "Kleppmann"`.
func (n Eq) String() string { return pathString(n.Path) + " == " + valueString(n.Value) }

// String renders `status != "draft"`.
func (n Ne) String() string { return pathString(n.Path) + " != " + valueString(n.Value) }

// String renders `year > 2015`.
func (n Cmp) String() string {
	return pathString(n.Path) + " " + n.Op.String() + " " + valueString(n.Value)
}

// String renders `title =~ /^go/i`. Each / in the pattern renders as \/. The
// flags follow the closing slash as they are.
func (n Regex) String() string {
	return pathString(n.Path) + " =~ /" + escapeSlash(n.Pattern) + "/" + n.Flags
}

// String renders `has(isbn)`.
func (n Exists) String() string { return "has(" + pathString(n.Path) + ")" }

// String renders `!has(isbn)`.
func (n NotExists) String() string { return "!has(" + pathString(n.Path) + ")" }

// String renders `len(tags) == 3`.
func (n Size) String() string { return fmt.Sprintf("len(%s) == %d", pathString(n.Path), n.N) }

// String renders `tags any(@ == "go")`.
func (n ElemMatch) String() string { return pathString(n.Path) + " any(" + condString(n.Cond) + ")" }

// String renders `tags none(@ == "draft")`.
func (n NoneMatch) String() string { return pathString(n.Path) + " none(" + condString(n.Cond) + ")" }

// String renders `(a == 1 and b == 2)`. A single child renders as `(a == 1)`.
// An empty And matches everything and renders as `(true)`.
func (n And) String() string { return "(" + join(n, " and ", "true") + ")" }

// String renders `(a == 1 or c == 2)`. A single child renders as `(a == 1)`.
// An empty Or matches nothing and renders as `(false)`.
func (n Or) String() string { return "(" + join(n, " or ", "false") + ")" }

// condString renders the condition of any or none. An And or Or has no outer
// parentheses there, because the brackets of any(...) already enclose it. A nil
// condition renders as `true`, so String never panics.
func condString(c Node) string {
	switch c := c.(type) {
	case nil:
		return "true"
	case And:
		return join(c, " and ", "true")
	case Or:
		return join(c, " or ", "false")
	default:
		return c.String()
	}
}

// join renders the children with sep between them. An empty list renders as empty.
func join(children []Node, sep, empty string) string {
	if len(children) == 0 {
		return empty
	}
	parts := make([]string, len(children))
	for i, c := range children {
		if c == nil {
			parts[i] = "true"
		} else {
			parts[i] = c.String()
		}
	}
	return strings.Join(parts, sep)
}
