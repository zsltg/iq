package hbase

import (
	"context"
	"fmt"

	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the rows matching pred, translating the pushable part of
// the predicate to an HBase server-side filter so the region servers pre-filter. pred
// is a conservative superset (the engine re-runs the full jq per page), so pushing
// fewer conjuncts is always safe; only column equality is pushed. When nothing is
// pushable it falls back to a full scan, identical to ScanBatches.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.table == "" {
		return errNoTable
	}
	opts := []func(hrpc.Call) error{hrpc.MaxVersions(1)}
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
	return s.pageScan(req, fn)
}

// toFilter translates the pushable part of a neutral predicate into an HBase filter.
// Only column equality (Eq on a family:qualifier path) and an And of such equalities
// are pushed; an And pushes the subset of its conjuncts that compile and drops the
// rest (widening, safe because the full jq re-runs). Ranges, regex, existence, size,
// negation, and Or are never pushed. A path that is not a two-segment
// family:qualifier, or whose literal will not encode to the column's type, is not
// pushable. types declares column encodings so the comparator bytes match the stored
// bytes; the connection-free plan description walks the predicate itself
// (pushdownColumns) rather than building filters.
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
// literal is encoded through the column's declared type so the binary comparison
// matches the stored bytes; filterIfMissing excludes rows lacking the column, which
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
	return filter.NewSingleColumnValueFilter(
		[]byte(family), []byte(qualifier), filter.Equal, comparator,
		true /*filterIfMissing*/, true, /*latestVersionOnly*/
	), true
}

// andToFilter conjoins the pushable conjuncts of an And into a MustPassAll list,
// dropping any that do not compile. It reports ok when at least one conjunct pushed;
// dropping conjuncts only widens the pre-filter.
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

// pushdownColumns walks a predicate and returns the family:qualifier columns a
// server-side filter would pre-filter on, and whether any push at all. It mirrors
// toFilter's pushable set (column equality, and And of such) without connecting or
// encoding, so the connection-free plan description matches what ScanFiltered issues.
func pushdownColumns(n predicate.Node) ([]string, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		if len(t.Path) != 2 {
			return nil, false
		}
		return []string{cellKey(t.Path[0], t.Path[1])}, true
	case predicate.And:
		var cols []string
		for _, c := range t {
			if cc, ok := pushdownColumns(c); ok {
				cols = append(cols, cc...)
			}
		}
		return cols, len(cols) > 0
	default:
		return nil, false
	}
}
