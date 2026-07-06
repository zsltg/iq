package cassandra

import (
	"context"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the rows matching pred, translating the pushable part
// of the predicate to a CQL WHERE so the cluster pre-filters. pred is a conservative
// superset (the engine re-runs the full jq per page), so pushing fewer conjuncts is
// always safe; only equality and same-column membership are pushed. A WHERE that
// touches anything but the full partition key needs ALLOW FILTERING, appended here —
// an opt-in cost surfaced in --explain. When nothing is pushable it falls back to a
// full scan, identical to ScanBatches.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.meta == nil {
		return errNoTable
	}
	where, args, ok := toCQL(columnTypes(s.meta), pred)
	cql := "SELECT * FROM " + s.tableRef()
	if ok && where != "" {
		cql += " WHERE " + where + " ALLOW FILTERING"
	}
	return s.pageScan(ctx, cql, args, fn)
}

// columnTypes indexes a table's columns by name to their CQL type, so toCQL can
// confirm a predicate names a real column and coerce its literal to the column type.
func columnTypes(meta *gocql.TableMetadata) map[string]gocql.TypeInfo {
	out := make(map[string]gocql.TypeInfo, len(meta.Columns))
	for name, col := range meta.Columns {
		out[name] = col.Type
	}
	return out
}

// toCQL translates the pushable part of a neutral predicate into a CQL WHERE clause
// and its bind values. Only equality (Eq) and a same-column or of equalities (→ IN)
// are pushed; an And pushes the subset of its conjuncts that compile and drops the
// rest (widening, safe because the full jq re-runs). Ranges, regex, existence, size,
// element matches, and negations are never pushed. A path that is not a single
// top-level column, or (when cols is non-nil) not a real column, or whose literal
// will not coerce to the column type, is treated as not pushable. cols may be nil
// for a connection-free description, which skips validation and coercion.
func toCQL(cols map[string]gocql.TypeInfo, n predicate.Node) (where string, args []any, ok bool) {
	switch t := n.(type) {
	case predicate.Eq:
		return eqToCQL(cols, t)
	case predicate.Or:
		return orToIN(cols, t)
	case predicate.And:
		return andToCQL(cols, t)
	default:
		return "", nil, false
	}
}

// eqToCQL renders a single-column equality, coercing and validating against the
// column type when cols is provided.
func eqToCQL(cols map[string]gocql.TypeInfo, eq predicate.Eq) (string, []any, bool) {
	if len(eq.Path) != 1 {
		return "", nil, false
	}
	col := eq.Path[0]
	arg, ok := bindArg(cols, col, eq.Value)
	if !ok {
		return "", nil, false
	}
	return quoteIdent(col) + " = ?", []any{arg}, true
}

// orToIN collapses an or of equalities on one column into a single IN membership
// test, the only or CQL can express (it has no general OR before 5.0). Every branch
// must be a pushable equality on the same column, else the whole or is dropped —
// dropping a branch would narrow the result and lose rows, which the superset
// contract forbids.
func orToIN(cols map[string]gocql.TypeInfo, or predicate.Or) (string, []any, bool) {
	if len(or) < 2 {
		return "", nil, false
	}
	var col string
	args := make([]any, 0, len(or))
	for i, n := range or {
		eq, isEq := n.(predicate.Eq)
		if !isEq || len(eq.Path) != 1 {
			return "", nil, false
		}
		if i == 0 {
			col = eq.Path[0]
		} else if eq.Path[0] != col {
			return "", nil, false
		}
		arg, ok := bindArg(cols, col, eq.Value)
		if !ok {
			return "", nil, false
		}
		args = append(args, arg)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
	return quoteIdent(col) + " IN (" + placeholders + ")", args, true
}

// andToCQL conjoins the pushable conjuncts of an and, dropping any that do not
// compile. It reports ok when at least one conjunct pushed, so the caller adds a
// WHERE; dropping conjuncts only widens the pre-filter.
func andToCQL(cols map[string]gocql.TypeInfo, and predicate.And) (string, []any, bool) {
	clauses := make([]string, 0, len(and))
	args := make([]any, 0, len(and))
	for _, n := range and {
		w, a, ok := toCQL(cols, n)
		if !ok {
			continue
		}
		clauses = append(clauses, w)
		args = append(args, a...)
	}
	if len(clauses) == 0 {
		return "", nil, false
	}
	return strings.Join(clauses, " AND "), args, true
}

// bindArg validates and coerces a predicate literal against a column. With no column
// index (a connection-free description) it passes the literal through unchanged;
// otherwise the column must exist and the literal must coerce to its type, else the
// predicate is not pushable.
func bindArg(cols map[string]gocql.TypeInfo, col string, value any) (any, bool) {
	if cols == nil {
		return value, true
	}
	t, ok := cols[col]
	if !ok {
		return nil, false
	}
	bv, err := bindValue(t, value)
	if err != nil {
		return nil, false
	}
	return bv, true
}
