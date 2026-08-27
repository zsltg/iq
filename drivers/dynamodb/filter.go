package dynamodb

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the items matching pred, translating the pushable part of
// the predicate to a DynamoDB Scan FilterExpression so the service pre-filters. pred is
// a conservative superset (the engine re-runs the full jq per page), so pushing fewer
// conjuncts is always safe; only equality and existence are pushed. Ranges are never
// pushed because jq's cross-type ordering (a string is greater than every number) means
// a typed comparison would wrongly exclude items and drop rows. When nothing is pushable
// it falls back to a full scan, identical to ScanBatches.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if len(s.keys) == 0 {
		return errNoTable
	}
	in := &dynamodb.ScanInput{TableName: &s.table}
	if f, ok := compile(pred); ok && f.expr != "" {
		in.FilterExpression = aws.String(f.expr)
		in.ExpressionAttributeNames = f.names
		in.ExpressionAttributeValues = f.values
	}
	return s.scan(ctx, in, fn)
}

// frag is one compiled predicate fragment: its DynamoDB FilterExpression form (using
// #nN name placeholders and :vN value placeholders, which DynamoDB requires so a
// reserved word like "name" or "size" is never interpolated raw), the placeholder maps
// it introduced, and a human-readable form for --explain. Keeping the execution and
// display forms in one struct means the plan shown can never drift from the filter run.
type frag struct {
	expr    string
	display string
	names   map[string]string
	values  map[string]types.AttributeValue
}

// counter hands out unique placeholder indices across a whole predicate tree, so two
// leaves never collide on #n0/:v0.
type counter struct{ n int }

func (c *counter) next() int {
	c.n++
	return c.n
}

// compile translates the pushable part of a neutral predicate into a DynamoDB filter
// fragment. Only equality (Eq on a scalar), existence (Exists, NotExists), and their
// And/Or combinations are pushed; a single top-level attribute path only. An And keeps
// the subset of its conjuncts that compile and drops the rest (widening, safe because
// the full jq re-runs); an Or must compile every branch or it is dropped whole, since
// dropping an Or branch would narrow the result and lose items. Ranges, regex, size,
// negations, and element matches are never pushed.
func compile(pred predicate.Node) (frag, bool) {
	return build(&counter{}, pred)
}

func build(c *counter, node predicate.Node) (frag, bool) {
	switch t := node.(type) {
	case predicate.Eq:
		return leafEq(c, t)
	case predicate.Exists:
		return leafFunc(c, "attribute_exists", t.Path)
	case predicate.NotExists:
		return leafFunc(c, "attribute_not_exists", t.Path)
	case predicate.And:
		return buildAnd(c, t)
	case predicate.Or:
		return buildOr(c, t)
	default:
		return frag{}, false
	}
}

// leafEq compiles a single-attribute equality against a scalar literal. A null value
// is never pushed: DynamoDB `#a = :null` matches only items whose attribute is the
// NULL type, missing the absent-attribute items jq's `== null` also matches, which
// would drop rows.
func leafEq(c *counter, eq predicate.Eq) (frag, bool) {
	if len(eq.Path) != 1 {
		return frag{}, false
	}
	av, ok := scalarAV(eq.Value)
	if !ok {
		return frag{}, false
	}
	id := c.next()
	np := fmt.Sprintf("#n%d", id)
	vp := fmt.Sprintf(":v%d", id)
	return frag{
		expr:    np + " = " + vp,
		display: eq.Path[0] + " = ?",
		names:   map[string]string{np: eq.Path[0]},
		values:  map[string]types.AttributeValue{vp: av},
	}, true
}

// leafFunc compiles an existence test (attribute_exists / attribute_not_exists) on a
// single-attribute path. These are exact: key presence means the same to jq's has()
// and to DynamoDB, so they never over- or under-return.
func leafFunc(c *counter, fn string, path []string) (frag, bool) {
	if len(path) != 1 {
		return frag{}, false
	}
	id := c.next()
	np := fmt.Sprintf("#n%d", id)
	return frag{
		expr:    fn + "(" + np + ")",
		display: fn + "(" + path[0] + ")",
		names:   map[string]string{np: path[0]},
		values:  map[string]types.AttributeValue{},
	}, true
}

// buildAnd conjoins the pushable conjuncts of an And, dropping any that do not compile
// (widening the pre-filter, safe). It reports ok when at least one conjunct pushed. It
// merges only the placeholder maps of the conjuncts that survived, so no orphan
// placeholder (which DynamoDB rejects as unused) is ever emitted.
func buildAnd(c *counter, and predicate.And) (frag, bool) {
	return join(c, and, " AND ", false)
}

// buildOr disjoins the branches of an Or, but only if every branch compiles; a single
// uncompilable branch drops the whole Or, because keeping the rest would narrow the
// match and lose items.
func buildOr(c *counter, or predicate.Or) (frag, bool) {
	if len(or) == 0 {
		return frag{}, false
	}
	return join(c, or, " OR ", true)
}

// join builds a boolean junction of the child fragments. When allRequired is set (Or),
// a single failing child fails the whole junction; otherwise (And) failing children are
// dropped. A single surviving child is returned unparenthesized; two or more are wrapped
// so precedence is explicit.
func join(c *counter, nodes []predicate.Node, sep string, allRequired bool) (frag, bool) {
	exprs := make([]string, 0, len(nodes))
	displays := make([]string, 0, len(nodes))
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	for _, n := range nodes {
		f, ok := build(c, n)
		if !ok {
			if allRequired {
				return frag{}, false
			}
			continue
		}
		exprs = append(exprs, f.expr)
		displays = append(displays, f.display)
		maps.Copy(names, f.names)
		maps.Copy(values, f.values)
	}
	if len(exprs) == 0 {
		return frag{}, false
	}
	if len(exprs) == 1 {
		return frag{expr: exprs[0], display: displays[0], names: names, values: values}, true
	}
	trim := strings.TrimSpace(sep)
	return frag{
		expr:    "(" + strings.Join(exprs, sep) + ")",
		display: "(" + strings.Join(displays, " "+trim+" ") + ")",
		names:   names,
		values:  values,
	}, true
}

// scalarAV converts a predicate literal into the DynamoDB attribute value an equality
// binds. Only a string, boolean, or number pushes; a null (or any richer value) is not
// pushable, so the equality is dropped and the full jq re-runs client-side. A number
// arrives as a float64 from the jq compiler, but an int is accepted defensively.
func scalarAV(v any) (types.AttributeValue, bool) {
	switch t := v.(type) {
	case string:
		return &types.AttributeValueMemberS{Value: t}, true
	case bool:
		return &types.AttributeValueMemberBOOL{Value: t}, true
	case float64:
		return &types.AttributeValueMemberN{Value: strconv.FormatFloat(t, 'g', -1, 64)}, true
	case int:
		return &types.AttributeValueMemberN{Value: strconv.Itoa(t)}, true
	case int64:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(t, 10)}, true
	default:
		return nil, false
	}
}
