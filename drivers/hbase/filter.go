package hbase

import (
	"context"
	"fmt"

	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered translates the pushable part of pred to an HBase server-side
// filter. See query.FilteredScanner. Only column equality is pushed. If nothing is
// pushable, the scan is the same as ScanBatches.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.table == "" {
		return errNoTable
	}
	opts := s.readOptions(ctx)
	if f, ok := toFilter(s.types, pred); ok {
		s.traceOp("scan %s (filtered)", s.table)
		opts = append(opts, hrpc.Filters(f))
	} else {
		s.traceOp("scan %s", s.table)
	}
	req, err := hrpc.NewScanStr(ctx, s.table, opts...)
	if err != nil {
		return fmt.Errorf("hbase scan: %w", err)
	}
	return s.pageScan(ctx, req, fn)
}

// toFilter translates the pushable part of a neutral predicate into an HBase filter.
// Only column equality (Eq on a family:qualifier path) and an And of such
// equalities are pushed. An And pushes the conjuncts that compile and drops the
// rest. Ranges, regex, existence, size, negation, and Or are never pushed. A path
// is not pushable if it is not a two-segment family:qualifier, or its literal will
// not encode to the column type. The types map declares column encodings, so the
// comparator bytes match the stored bytes. The connection-free plan runs it with
// the type map of the URI.
func toFilter(types typeMap, n predicate.Node) (filter.Filter, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		return eqToFilter(types, t)
	case predicate.And:
		return andToFilter(types, t)
	default:
		return nil, false
	}
}

// eqToFilter renders a single column equality as a SingleColumnValueFilter. The
// literal is encoded through the column's declared type, so the binary comparison
// matches the stored bytes. filterIfMissing excludes rows lacking the column. This
// is exact for equality (a missing column never equals a value).
func eqToFilter(types typeMap, eq predicate.Eq) (filter.Filter, bool) {
	if len(eq.Path) != 2 {
		return nil, false
	}
	family, qualifier := eq.Path[0], eq.Path[1]
	b, err := encodeCell(colTypeFor(types, family, qualifier), eq.Value)
	if err != nil {
		return nil, false
	}
	comparator := filter.NewBinaryComparator(filter.NewByteArrayComparable(b))
	// Named rather than passed as bare trailing literals: the two booleans are
	// meaningless at the call site otherwise. An inline-comment form here is
	// mangled by the mutation printer into code that does not parse.
	const (
		filterIfMissing   = true
		latestVersionOnly = true
	)
	return filter.NewSingleColumnValueFilter(
		[]byte(family), []byte(qualifier), filter.Equal, comparator,
		filterIfMissing, latestVersionOnly,
	), true
}

// andToFilter conjoins the pushable conjuncts of an And into a MustPassAll list,
// dropping any that do not compile. It reports ok when at least one conjunct
// pushed. Dropping conjuncts only widens the pre-filter.
func andToFilter(types typeMap, and predicate.And) (filter.Filter, bool) {
	filters := make([]filter.Filter, 0, len(and))
	for _, n := range and {
		if f, ok := toFilter(types, n); ok {
			filters = append(filters, f)
		}
	}
	switch len(filters) {
	case 0:
		return nil, false
	case 1:
		return filters[0], true
	default:
		return filter.NewList(filter.MustPassAll, filters...), true
	}
}

// pushedColumns lists the family:qualifier columns that toFilter accepts for n, in
// predicate order. It runs eqToFilter on each equality, so the plan names exactly the
// columns that the scan pushes.
func pushedColumns(types typeMap, n predicate.Node) []string {
	switch t := n.(type) {
	case predicate.Eq:
		if _, ok := eqToFilter(types, t); ok {
			return []string{cellKey(t.Path[0], t.Path[1])}
		}
	case predicate.And:
		var cols []string
		for _, c := range t {
			cols = append(cols, pushedColumns(types, c)...)
		}
		return cols
	}
	return nil
}
