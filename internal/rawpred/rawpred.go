// Package rawpred is a client-side, raw-byte predicate prefilter. Given a
// document's raw JSON bytes and a pushed-down predicate, Match decides whether the
// document can be dropped before it is decoded, without ever parsing it into Go
// values.
//
// The invariant is one-directional and load-bearing: Match returns CannotMatch
// only when the decoded document provably fails the predicate. Because a pushed
// predicate is already a conservative superset of the jq filter it came from (see
// internal/predicate), and the engine re-runs the full jq client-side over
// whatever survives, dropping a document rawpred proves the predicate rejects can
// never change results — the full jq would reject it too. Every uncertainty, every
// unhandled node, and every structural surprise collapses to MayMatch, so the
// prefilter only ever skips a decode it can justify.
//
// A predicate that carries Regex nodes compiles their patterns once via NewMatcher:
// the returned Matcher is reused across a whole scan so the per-document path never
// recompiles. The package-level Match is the convenience path — it prepares a fresh
// Matcher per call — and stays correct and API-compatible for callers that hold no
// scan of their own.
//
// It is a driver-side utility: the query core never imports it, so the JSON
// extraction dependency stays out of the engine and its ports.
package rawpred

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/buger/jsonparser"

	"github.com/zsltg/iq/internal/predicate"
)

// Verdict is Match's two-valued answer. It is a closed type: a caller drops a
// document only on CannotMatch, and the zero value is the safe MayMatch, so an
// unset verdict never drops.
type Verdict int

const (
	// MayMatch means the document is not provably rejected by the predicate and
	// must be decoded and re-filtered by the full jq. It is the zero value.
	MayMatch Verdict = iota
	// CannotMatch means the decoded document provably fails the predicate, so it
	// can be dropped without decoding.
	CannotMatch
)

// Match reports whether raw can be dropped against pred without decoding it. It
// returns CannotMatch only when the decoded document provably fails pred; every
// other case — a match, an ambiguity, an unhandled node, malformed JSON — is
// MayMatch. It is the convenience path: it prepares a fresh Matcher, so a caller
// evaluating many documents against one predicate should build a Matcher once with
// NewMatcher and reuse it instead.
func Match(raw []byte, pred predicate.Node) Verdict {
	return NewMatcher(pred).Match(raw)
}

// Matcher is a predicate prepared for repeated matching. NewMatcher walks the tree
// once and compiles every Regex pattern, so the per-document Match never compiles a
// pattern again; a driver builds one per scan and reuses it across every document.
type Matcher struct {
	pred    predicate.Node
	regexes map[regexKey]*regexp.Regexp
}

// regexKey identifies a compiled pattern by the exact (pattern, flags) pair a Regex
// node carries, so identical nodes share one compilation.
type regexKey struct {
	pattern string
	flags   string
}

// NewMatcher prepares pred for matching, compiling each Regex pattern once. A
// pattern that fails to compile is left uncompiled and its node degrades to a decode
// (evalRegex reads a missing key as unknown), never surfacing an error to the
// caller: a prefilter that cannot evaluate a node keeps the document, exactly as an
// unhandled node does.
func NewMatcher(pred predicate.Node) *Matcher {
	m := &Matcher{pred: pred, regexes: map[regexKey]*regexp.Regexp{}}
	m.prepare(pred)
	return m
}

// prepare walks the predicate tree compiling every Regex reachable through And/Or
// nodes and through an ElemMatch or NoneMatch Cond. Those container nodes now
// evaluate their Cond against each element on raw bytes, so a Regex nested in one is
// compiled here too, deduplicated by (pattern, flags) exactly like a top-level one.
func (m *Matcher) prepare(node predicate.Node) {
	switch n := node.(type) {
	case predicate.Regex:
		key := regexKey{pattern: n.Pattern, flags: n.Flags}
		if _, seen := m.regexes[key]; seen {
			return
		}
		re, ok := compileRegex(n.Pattern, n.Flags)
		if !ok {
			// A pattern portableRegex passed but Go's RE2 rejects: leave it
			// uncompiled so evalRegex reads the missing key as unknown and the
			// document is decoded. The failure is deliberately not surfaced as an
			// error — a prefilter that cannot evaluate a node keeps the document.
			return
		}
		m.regexes[key] = re
	case predicate.And:
		for _, c := range n {
			m.prepare(c)
		}
	case predicate.Or:
		for _, c := range n {
			m.prepare(c)
		}
	case predicate.ElemMatch:
		m.prepare(n.Cond)
	case predicate.NoneMatch:
		m.prepare(n.Cond)
	}
}

// Match reports whether raw can be dropped against the prepared predicate without
// decoding it, reusing the patterns compiled at NewMatcher time. Like the package
// Match it returns CannotMatch only on a provable non-match.
func (m *Matcher) Match(raw []byte) Verdict {
	if m.eval(raw, m.pred) == definiteNo {
		return CannotMatch
	}
	return MayMatch
}

// compileRegex translates a jq test() pattern and flag string to a Go RE2 matcher,
// exactly as gojq's own compileRegexp does (github.com/itchyny/gojq func.go): the i
// flag prepends the (?i) inline group (case-insensitive) and jq's m flag prepends
// (?s) — jq's m means "dot matches newline" (Go's dotall), NOT PCRE's line-anchor
// multiline. Matching is unanchored, like test()'s regexp.MatchString. The pushdown
// compiler only ever emits i and m (portableFlags) over an engine-portable pattern
// (portableRegex), so this never narrows which documents match against jq. It
// reports ok=false for a pattern RE2 rejects rather than wrapping an error: the
// failure is never surfaced (the node degrades to a decode), so a returned error
// would only be discarded.
func compileRegex(pattern, flags string) (re *regexp.Regexp, ok bool) {
	if strings.ContainsRune(flags, 'i') {
		pattern = "(?i)" + pattern
	}
	if strings.ContainsRune(flags, 'm') {
		pattern = "(?s)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false
	}
	return re, true
}

// triple is the internal three-valued node verdict. It is collapsed to the
// two-valued Verdict by Match: only definiteNo becomes CannotMatch, and both
// definiteYes and unknown become MayMatch (the document is decoded).
type triple int

const (
	// The zero value is reserved and never returned, so a mutated `return 0` is a
	// distinct, observably-wrong verdict rather than an alias of a real one.
	_ triple = iota
	// definiteNo means the node provably does not match: the document fails it.
	definiteNo
	// definiteYes means the node provably matches.
	definiteYes
	// unknown means the node cannot be decided from the raw bytes, so the document
	// must be decoded to know.
	unknown
)

// eval evaluates a predicate node against raw's bytes, three-valued. An unhandled
// or future node type is unknown, so a new predicate never causes a wrong drop.
func (m *Matcher) eval(raw []byte, node predicate.Node) triple {
	switch n := node.(type) {
	case predicate.Eq:
		return evalEq(raw, n.Path, n.Value)
	case predicate.Ne:
		// Ne is the exact negation of Eq: it provably fails only when the field
		// provably equals Value, and provably holds only when it provably differs.
		return negate(evalEq(raw, n.Path, n.Value))
	case predicate.Exists:
		return evalExists(raw, n.Path, true)
	case predicate.NotExists:
		return evalExists(raw, n.Path, false)
	case predicate.Cmp:
		return evalCmp(raw, n.Path, n.Op, n.Value)
	case predicate.Regex:
		return m.evalRegex(raw, n)
	case predicate.Size:
		return evalSize(raw, n.Path, n.N)
	case predicate.ElemMatch:
		return m.evalElemMatch(raw, n)
	case predicate.NoneMatch:
		return m.evalNoneMatch(raw, n)
	case predicate.And:
		return m.evalAnd(raw, n)
	case predicate.Or:
		return m.evalOr(raw, n)
	default:
		// A nil predicate, or any node type added later, is not evaluated on raw
		// bytes; the document is decoded and re-filtered.
		return unknown
	}
}

// evalRegex decides a Regex node from the prepared matcher's compiled patterns. It
// is definiteNo only when the field is a present string the pattern does not match,
// and definiteYes when it is a present string the pattern matches; every other case
// — a missing, ambiguous, or non-string field, or a pattern that failed to compile —
// is unknown. jq's test() errors on a non-string, so a non-string field must reach
// the client unchanged; leaving it unknown keeps it, so the prefilter never
// suppresses that error by dropping the document.
func (m *Matcher) evalRegex(raw []byte, n predicate.Regex) triple {
	re, ok := m.regexes[regexKey{pattern: n.Pattern, flags: n.Flags}]
	if !ok {
		// The pattern failed to compile (NewMatcher left it uncompiled), so the node
		// is never provable: decode.
		return unknown
	}
	val, typ, _ := getField(raw, n.Path)
	if typ != jsonparser.String {
		// Missing (getField reports Unknown), ambiguous (Unknown), or a non-string
		// value: not a definite string, so decode and let the full jq run.
		return unknown
	}
	s, err := jsonparser.ParseString(val)
	if err != nil {
		return unknown
	}
	return boolTriple(re.MatchString(s))
}

// negate flips a three-valued verdict, leaving unknown unknown. It is how Ne reads
// the Eq verdict of the same field and value.
func negate(t triple) triple {
	switch t {
	case definiteYes:
		return definiteNo
	case definiteNo:
		return definiteYes
	default:
		return unknown
	}
}

// evalEq decides `field == value`. A cleanly-absent field is jq's null, so it
// equals a nil (null) value and differs from any scalar; a found value is compared
// by type and, for numbers, by the numeric rule.
func evalEq(raw []byte, path []string, value any) triple {
	val, typ, st := getField(raw, path)
	switch st {
	case fieldFound:
		return eqFound(val, typ, value)
	case fieldAbsent:
		// Missing field is null in jq: it equals null, nothing else.
		if value == nil {
			return definiteYes
		}
		return definiteNo
	default:
		// fieldAmbiguous: the path did not resolve cleanly, so decode to know.
		return unknown
	}
}

// eqFound compares a present value (its raw bytes and jsonparser type) to a scalar
// pred value. A type mismatch is a definite non-match; a same-type comparison is
// decided exactly, except a number the numeric rule cannot pin down, which is
// unknown.
func eqFound(val []byte, typ jsonparser.ValueType, value any) triple {
	switch v := value.(type) {
	case nil:
		return boolTriple(typ == jsonparser.Null)
	case bool:
		if typ != jsonparser.Boolean {
			return definiteNo
		}
		b, err := jsonparser.ParseBoolean(val)
		if err != nil {
			return unknown
		}
		return boolTriple(b == v)
	case string:
		if typ != jsonparser.String {
			return definiteNo
		}
		s, err := jsonparser.ParseString(val)
		if err != nil {
			return unknown
		}
		return boolTriple(s == v)
	case float64:
		if typ != jsonparser.Number {
			return definiteNo
		}
		ord, ok := numOrd(val, v)
		if !ok {
			return unknown
		}
		return boolTriple(ord == 0)
	default:
		return unknown
	}
}

// evalExists decides Exists (want=true) or NotExists (want=false). A clean
// key-path miss is definitively absent; a structural surprise is unknown.
func evalExists(raw []byte, path []string, want bool) triple {
	_, _, st := getField(raw, path)
	switch st {
	case fieldFound:
		return boolTriple(want)
	case fieldAbsent:
		return boolTriple(!want)
	default:
		return unknown
	}
}

// evalCmp decides `field OP value` under jq's total type ordering. A cleanly-absent
// field is null, the least value, so it ranks below every number and string
// value; a found value is ranked by type, then compared within a type.
func evalCmp(raw []byte, path []string, op predicate.Op, value any) triple {
	vr, ok := valueRank(value)
	if !ok {
		return unknown
	}
	val, typ, st := getField(raw, path)
	switch st {
	case fieldFound:
		return cmpFound(val, typ, op, value, vr)
	case fieldAbsent:
		// Missing field is null (rank 0), below any number or string value.
		return applyOp(op, cmpInt(nullRank, vr))
	default:
		// fieldAmbiguous: the path did not resolve cleanly, so decode to know.
		return unknown
	}
}

// cmpFound compares a present value to a number or string pred value under jq's
// ordering. A cross-type comparison is settled by type rank alone; a same-type
// comparison is settled within the type — numbers by the numeric rule (unknown
// when it cannot decide), strings by their unescaped bytes.
func cmpFound(val []byte, typ jsonparser.ValueType, op predicate.Op, value any, vr int) triple {
	dr, ok := docRank(typ)
	if !ok {
		return unknown
	}
	if dr != vr {
		return applyOp(op, cmpInt(dr, vr))
	}
	switch v := value.(type) {
	case float64:
		ord, ok := numOrd(val, v)
		if !ok {
			return unknown
		}
		return applyOp(op, ord)
	case string:
		s, err := jsonparser.ParseString(val)
		if err != nil {
			return unknown
		}
		return applyOp(op, strings.Compare(s, v))
	default:
		return unknown
	}
}

// evalAnd decides an And: it fails as soon as one child fails, and holds only when
// every child holds; anything else is unknown. An empty And matches everything.
func (m *Matcher) evalAnd(raw []byte, children predicate.And) triple {
	all := definiteYes
	for _, c := range children {
		switch m.eval(raw, c) {
		case definiteNo:
			return definiteNo
		case unknown:
			all = unknown
		}
	}
	return all
}

// evalOr decides an Or: it holds as soon as one child holds, and fails only when
// every child fails; anything else is unknown. An empty Or is left unknown rather
// than dropping everything, since the pushdown compiler never emits one.
func (m *Matcher) evalOr(raw []byte, children predicate.Or) triple {
	if len(children) == 0 {
		return unknown
	}
	all := definiteNo
	for _, c := range children {
		switch m.eval(raw, c) {
		case definiteYes:
			return definiteYes
		case unknown:
			all = unknown
		}
	}
	return all
}

// evalSize decides a Size node — jq's `.path | length == n` — from raw bytes. jq's
// length is polymorphic, so the field's jsonparser type picks the count: array
// elements, object keys, the rune length of a string, 0 for null, and |literal| for
// an integral number. A cleanly-absent field is jq's null, whose length is 0; a
// boolean, a number the numeric rule cannot pin down, or an ambiguous path is
// unknown.
func evalSize(raw []byte, path []string, n int) triple {
	val, typ, st := getField(raw, path)
	switch st {
	case fieldFound:
		return sizeFound(val, typ, n)
	case fieldAbsent:
		// A cleanly-missing field is jq's null, whose length is 0.
		return boolTriple(n == 0)
	default:
		// fieldAmbiguous: the path did not resolve cleanly, so decode to know.
		return unknown
	}
}

// sizeFound decides `length == n` for a present value from its jsonparser type. Only
// a boolean (jq's length errors on it) and a number the numeric rule cannot pin down
// stay unknown; every other type counts definitively.
func sizeFound(val []byte, typ jsonparser.ValueType, n int) triple {
	switch typ {
	case jsonparser.Array:
		c, ok := countElements(val)
		if !ok {
			return unknown
		}
		return boolTriple(c == n)
	case jsonparser.Object:
		c, ok := countKeys(val)
		if !ok {
			return unknown
		}
		return boolTriple(c == n)
	case jsonparser.String:
		s, err := jsonparser.ParseString(val)
		if err != nil {
			return unknown
		}
		return boolTriple(utf8.RuneCountInString(s) == n)
	case jsonparser.Null:
		// jq's length of null is 0.
		return boolTriple(n == 0)
	case jsonparser.Number:
		return sizeOfNumber(val, n)
	default:
		// Boolean: jq's length errors on a boolean, so the error must reach the
		// client — unknown, exactly as Regex over a non-string stays unknown. An
		// unknown/malformed type is unknown too.
		return unknown
	}
}

// countElements counts the elements of a JSON array, jq's length of an array, or
// ok=false when jsonparser cannot walk it. The count starts at zero (an empty array
// has length 0) and rises one per element.
func countElements(val []byte) (int, bool) {
	count := 0
	_, err := jsonparser.ArrayEach(val, func(_ []byte, _ jsonparser.ValueType, _ int, _ error) {
		count++
	})
	if err != nil {
		return 0, false
	}
	return count, true
}

// countKeys counts the keys of a JSON object, jq's length of an object, or ok=false
// when jsonparser cannot walk it.
func countKeys(val []byte) (int, bool) {
	count := 0
	err := jsonparser.ObjectEach(val, func(_, _ []byte, _ jsonparser.ValueType, _ int) error {
		count++
		return nil
	})
	if err != nil {
		return 0, false
	}
	return count, true
}

// sizeOfNumber decides `length == n` for a number field. jq's length of a number is
// its magnitude, so the field matches only when |literal| == n. Stripping an optional
// leading sign and reading the absolute value with ParseUint at bitSize 63 accepts
// exactly the magnitudes that fit in an int64 (0 .. 2^63-1) and rejects everything
// else — a fractional number, an exponent form, an integer beyond int64 (a *big.Int
// once decoded), and MinInt64, whose magnitude 2^63 needs a 64th bit — all left to the
// full jq. Parsing the magnitude directly avoids a separate sign negation and its
// MinInt64 overflow corner.
func sizeOfNumber(raw []byte, n int) triple {
	if n < 0 {
		// A number's length is its magnitude, never negative, so it cannot equal a
		// negative n — jq would reject the document. Handling this first also makes the
		// uint64(n) conversion below provably non-negative.
		return definiteNo
	}
	mag, err := strconv.ParseUint(strings.TrimPrefix(string(raw), "-"), 10, 63)
	if err != nil {
		return unknown
	}
	return boolTriple(mag == uint64(n))
}

// evalElemMatch decides an ElemMatch node — jq's `.path | any(Cond)` — from raw
// bytes. It folds Cond over the container's elements (an object's values count too,
// as jq's any iterates them): definiteYes as soon as one element definitely matches,
// definiteNo only when every element definitely fails, and unknown when any element
// cannot be decided (unless one already matched, since a match wins outright).
func (m *Matcher) evalElemMatch(raw []byte, n predicate.ElemMatch) triple {
	s, ok := m.evalAny(raw, n.Path, n.Cond)
	if !ok {
		return unknown
	}
	switch {
	case s.sawYes:
		// A definite match: any is true regardless of the undecidable elements, and
		// keeping the document on an unprovable any is always safe anyway.
		return definiteYes
	case s.sawUnknown:
		return unknown
	default:
		// Every element definitely failed Cond: any is definitely false.
		return definiteNo
	}
}

// evalNoneMatch decides a NoneMatch node — jq's `.path | any(Cond) | not` — from raw
// bytes. NoneMatch is exact, so it only ever drops (definiteNo) when a definitely
// matching element exists AND no element is undecidable: jq's any short-circuits in
// order (isempty over the first match), so an undecidable element ahead of a match
// could make jq error rather than return true, which must reach the client. When
// every element definitely fails, any is definitely false and any|not definitely
// true.
func (m *Matcher) evalNoneMatch(raw []byte, n predicate.NoneMatch) triple {
	s, ok := m.evalAny(raw, n.Path, n.Cond)
	if !ok {
		return unknown
	}
	switch {
	case s.sawUnknown:
		// An undecidable element: jq's any may error before reaching a match, so the
		// negation cannot be proven either way — keep the document.
		return unknown
	case s.sawYes:
		// A definite match and no undecidable element: any is definitely true, so
		// any|not is definitely false and the document is dropped.
		return definiteNo
	default:
		return definiteYes
	}
}

// anyState folds element verdicts for jq's any(): whether some element definitely
// matched Cond, and whether some element could not be decided. The pair decides both
// ElemMatch (any) and NoneMatch (any|not), which read it differently.
type anyState struct {
	sawYes     bool
	sawUnknown bool
}

// add folds one element's Cond verdict into the accumulator. A definiteNo element
// leaves both flags untouched, so an empty container and an all-failing container
// both end with neither flag set.
func (s *anyState) add(t triple) {
	switch t {
	case definiteYes:
		s.sawYes = true
	case unknown:
		s.sawUnknown = true
	}
}

// evalAny folds Cond over the elements of the container at path, the shared core of
// ElemMatch and NoneMatch. It reports ok=false when the field is not a definite array
// or object (absent, ambiguous, a scalar, or a container jsonparser cannot walk):
// jq's any errors on a non-container, so neither node can be decided there.
func (m *Matcher) evalAny(raw []byte, path []string, cond predicate.Node) (anyState, bool) {
	val, typ, st := getField(raw, path)
	if st != fieldFound {
		return anyState{}, false
	}
	switch typ {
	case jsonparser.Array:
		return m.foldArray(val, cond)
	case jsonparser.Object:
		return m.foldObject(val, cond)
	default:
		return anyState{}, false
	}
}

// foldArray evaluates Cond against every array element, folding the verdicts. A
// jsonparser walk error abandons the fold as undecidable (ok=false).
func (m *Matcher) foldArray(val []byte, cond predicate.Node) (anyState, bool) {
	var s anyState
	_, err := jsonparser.ArrayEach(val, func(elem []byte, typ jsonparser.ValueType, _ int, cbErr error) {
		if cbErr != nil {
			s.sawUnknown = true
			return
		}
		s.add(m.evalElement(elem, typ, cond))
	})
	if err != nil {
		return anyState{}, false
	}
	return s, true
}

// foldObject evaluates Cond against every object value, folding the verdicts, since
// jq's any iterates an object's values.
func (m *Matcher) foldObject(val []byte, cond predicate.Node) (anyState, bool) {
	var s anyState
	err := jsonparser.ObjectEach(val, func(_, v []byte, typ jsonparser.ValueType, _ int) error {
		s.add(m.evalElement(v, typ, cond))
		return nil
	})
	if err != nil {
		return anyState{}, false
	}
	return s, true
}

// evalElement evaluates Cond against one container element. Only an object element
// can be re-walked as a JSON root by Cond's field paths; every other type — an array,
// a string (whose bytes jsonparser hands back unquoted, so they must never be re-read
// as an object), a number, a boolean, or null — is not indexable the way jq's cond
// needs, so jq would error on it and the element stays unknown, keeping the document.
func (m *Matcher) evalElement(elem []byte, typ jsonparser.ValueType, cond predicate.Node) triple {
	if typ != jsonparser.Object {
		return unknown
	}
	return m.eval(elem, cond)
}

// boolTriple maps a decided boolean to definiteYes or definiteNo.
func boolTriple(b bool) triple {
	if b {
		return definiteYes
	}
	return definiteNo
}

// applyOp turns a comparison ordering (doc vs value: -1, 0, +1) into a verdict for
// a range operator. The ordering is already proven, so the result is definite.
func applyOp(op predicate.Op, ord int) triple {
	var res bool
	switch op {
	case predicate.Gt:
		res = ord > 0
	case predicate.Ge:
		res = ord >= 0
	case predicate.Lt:
		res = ord < 0
	case predicate.Le:
		res = ord <= 0
	}
	return boolTriple(res)
}

// fieldState is the outcome of walking a field path: found, cleanly absent, or
// structurally ambiguous. Only a clean absence lets a predicate treat the field as
// jq's null; ambiguity always decodes.
type fieldState int

const (
	// The zero value is reserved and never returned, so a mutated `return …, 0` is
	// an observably-wrong state the eval switches route to unknown, not a silent
	// alias of fieldFound.
	_ fieldState = iota
	// fieldFound means the leaf value is present.
	fieldFound
	// fieldAbsent means a key was cleanly missing from an object along the path —
	// jq would read the field as null.
	fieldAbsent
	// fieldAmbiguous means the path could not be resolved cleanly: an intermediate
	// value was not an object, or the JSON was malformed. jq's behaviour here is
	// not a plain absence, so the document must be decoded.
	fieldAmbiguous
)

// getField walks path through raw a key at a time. Every container it descends
// must be an object: indexing a field of an array or scalar is a structural
// mismatch, not a clean absence, so it is ambiguous. A key cleanly missing from an
// object is a clean absence; any other jsonparser error is ambiguous. When the
// leaf is not found the returned type is jsonparser.Unknown — a non-null value has
// no meaningful type, and callers read the type only on fieldFound, so this keeps
// the not-found type an explicit, testable sentinel rather than the zero value.
func getField(raw []byte, path []string) ([]byte, jsonparser.ValueType, fieldState) {
	cur := raw
	for i, key := range path {
		if !isObject(cur) {
			return nil, jsonparser.Unknown, fieldAmbiguous
		}
		val, typ, _, err := jsonparser.Get(cur, key)
		if err != nil {
			if errors.Is(err, jsonparser.KeyPathNotFoundError) {
				return nil, jsonparser.Unknown, fieldAbsent
			}
			return nil, jsonparser.Unknown, fieldAmbiguous
		}
		// A key that occurs twice in one object has no single value: a decoder
		// that keeps the last occurrence (encoding/json) can disagree with the
		// first occurrence jsonparser returns (FuzzMatch).
		if duplicateKey(cur, key) {
			return nil, jsonparser.Unknown, fieldAmbiguous
		}
		if i == len(path)-1 {
			return val, typ, fieldFound
		}
		cur = val
	}
	// A field path from the pushdown compiler is never empty; an empty path has no
	// field to resolve, so it is ambiguous rather than a clean absence.
	return nil, jsonparser.Unknown, fieldAmbiguous
}

// duplicateKey reports whether key occurs more than once among the top-level keys
// of the object obj. Keys compare after unescaping, as jsonparser.Get matches
// them. An object that does not iterate cleanly reports true, so the caller
// treats it as ambiguous.
func duplicateKey(obj []byte, key string) bool {
	seen := 0
	err := jsonparser.ObjectEach(obj, func(k, _ []byte, _ jsonparser.ValueType, _ int) error {
		name, err := jsonparser.ParseString(k)
		if err != nil || name == key {
			seen++
		}
		return nil
	})
	return err != nil || seen > 1
}

// isObject reports whether b's first non-whitespace byte opens a JSON object. Only
// an object can be indexed by a field name, so this gates a clean-absence verdict.
func isObject(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// The jq type ranks used to order values across types
// (null < bool < number < string < array < object). Only the relative order
// matters — a boolean value ranks below every number and string, so its exact
// false/true split never matters here (a Cmp value is only ever a number or a
// string). The ranks start at 1 so none is the zero value, keeping every docRank
// return an observable, non-zero constant.
const (
	nullRank = iota + 1
	boolRank
	numberRank
	stringRank
	arrayRank
	objectRank
)

// docRank returns the jq type rank of a present document value from its jsonparser
// type, or ok=false for a type that cannot be ranked (an unknown/malformed value).
func docRank(typ jsonparser.ValueType) (int, bool) {
	switch typ {
	case jsonparser.Null:
		return nullRank, true
	case jsonparser.Boolean:
		return boolRank, true
	case jsonparser.Number:
		return numberRank, true
	case jsonparser.String:
		return stringRank, true
	case jsonparser.Array:
		return arrayRank, true
	case jsonparser.Object:
		return objectRank, true
	default:
		return 0, false
	}
}

// valueRank returns the jq type rank of a pred value, which a pushed Cmp restricts
// to a number or a string, or ok=false otherwise.
func valueRank(value any) (int, bool) {
	switch value.(type) {
	case float64:
		return numberRank, true
	case string:
		return stringRank, true
	default:
		return 0, false
	}
}

// cmpInt returns the sign of a-b: -1, 0, or +1.
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// maxExactInt is 2^53, the largest magnitude for which every integer is exactly
// representable as a float64. Beyond it a float64 rounds, so a pred value or a
// document integer past it cannot be compared exactly against the other side.
const maxExactInt = 1 << 53

// numOrd compares a raw JSON number literal to a jq numeric pred value pv,
// returning the jq-comparison ordering (doc<pv: -1, ==: 0, doc>pv: +1) and ok=true
// only where the ordering provably equals gojq's own. It gives up (ok=false)
// wherever the exact result is not knowable from the raw bytes and pv alone:
//   - a fractional document number, whose decoded type depends on the decimal mode
//     Match is not told (a float64, or an exact string that jq orders above every
//     number) — so its comparison cannot be proven here;
//   - a document integer beyond int64, decoded to a *big.Int, left to the full jq;
//   - a pred value past the exact float64 integer range, which the pushdown
//     compiler may already have rounded, so it need not equal gojq's own literal;
//   - a document integer past that range when the pred value is integral, where
//     int and float comparison can disagree at the rounding boundary.
//
// It never replicates gojq's arithmetic; it only proves an ordering or gives up.
func numOrd(raw []byte, pv float64) (int, bool) {
	// A NaN pred value would slip past the integral/fractional split (NaN != Trunc
	// (NaN)) into a float comparison whose result is meaningless, so reject it up
	// front. An infinity needs no guard: it is caught by the exact-range bound below.
	if math.IsNaN(pv) {
		return 0, false
	}
	iv, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		// Not a plain int64 document integer, so its comparison is not provable
		// here: a fractional number (its decoded type — float64 or exact string —
		// depends on the decimal mode Match is not told), an exponent form, or a
		// *big.Int beyond int64, all left to the full jq.
		return 0, false
	}
	if pv == math.Trunc(pv) {
		// Integral pred value. Compare as integers only when both sides are exactly
		// representable as float64, so int and float comparison cannot disagree and
		// pv equals gojq's literal exactly.
		if pv < -maxExactInt || pv > maxExactInt {
			return 0, false
		}
		if iv < -maxExactInt || iv > maxExactInt {
			return 0, false
		}
		return cmpInt64(iv, int64(pv)), true
	}
	// Fractional pred value: gojq compares by promoting the document integer to
	// float64, exactly as this does, so the ordering matches even when iv rounds.
	return cmpFloat(float64(iv), pv), true
}

// cmpInt64 returns the sign of a-b: -1, 0, or +1.
func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpFloat returns the sign of a-b: -1, 0, or +1. Neither operand is NaN at its
// call sites, so the ordering is total.
func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
