package redis

import (
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Redis calls this driver would make for a classified
// query, without connecting. Redis pushes no filter to the server (a scan always
// uses MATCH *), but when a predicate compiles it prefilters each RedisJSON
// document client-side against its raw bytes, dropping a provable non-match before
// decode; the ops reflect the pipelined TYPE + per-type read the driver issues,
// and Filter carries the compiled predicate the prefilter applies (nil when none
// compiles, or for a bounded key read).
func ExplainPlan(keys selector.KeySet, pred predicate.Node, _ bool) query.AccessPlan {
	const typedReads = "then per key by type: GET / HGETALL / LRANGE 0 -1 / SMEMBERS / ZRANGE 0 -1 WITHSCORES / JSON.GET"
	if !keys.Scan {
		return query.AccessPlan{Ops: []string{
			fmt.Sprintf("pipeline TYPE for %d requested key(s)", len(keys.Keys)),
			typedReads,
		}}
	}
	lastLine := "no server-side filter — every key is read and filtered client-side"
	var filter map[string]any
	if pred != nil {
		lastLine = "client-side raw-byte prefilter (rawpred) on RedisJSON values: a provably-non-matching " +
			"document is dropped before decode; every other type is decoded and the full jq re-runs client-side"
		filter = describePredicate(pred)
	}
	return query.AccessPlan{
		Ops: []string{
			fmt.Sprintf("SCAN 0 MATCH * COUNT %d: cursor over the whole keyspace", scanCount),
			"per page: pipeline TYPE for the page's keys",
			typedReads,
			lastLine,
		},
		Filter: filter,
	}
}

// describePredicate renders a compiled predicate into a nested map for the query
// plan, so `--explain` shows exactly what the client-side prefilter evaluates.
// There is no server query language to mirror (as the other drivers' plans do), so
// it renders the neutral predicate tree itself.
func describePredicate(pred predicate.Node) map[string]any {
	switch n := pred.(type) {
	case predicate.Eq:
		return map[string]any{"eq": fieldValue(n.Path, n.Value)}
	case predicate.Ne:
		return map[string]any{"ne": fieldValue(n.Path, n.Value)}
	case predicate.Cmp:
		return map[string]any{opName(n.Op): fieldValue(n.Path, n.Value)}
	case predicate.Exists:
		return map[string]any{"exists": pathString(n.Path)}
	case predicate.NotExists:
		return map[string]any{"notExists": pathString(n.Path)}
	case predicate.Regex:
		return map[string]any{"regex": map[string]any{"path": pathString(n.Path), "pattern": n.Pattern, "flags": n.Flags}}
	case predicate.Size:
		return map[string]any{"size": map[string]any{"path": pathString(n.Path), "length": n.N}}
	case predicate.ElemMatch:
		return map[string]any{"elemMatch": map[string]any{"path": pathString(n.Path), "cond": describePredicate(n.Cond)}}
	case predicate.NoneMatch:
		return map[string]any{"noneMatch": map[string]any{"path": pathString(n.Path), "cond": describePredicate(n.Cond)}}
	case predicate.And:
		return map[string]any{"and": describeChildren(n)}
	case predicate.Or:
		return map[string]any{"or": describeChildren(n)}
	default:
		return map[string]any{"unsupported": fmt.Sprintf("%T", pred)}
	}
}

// describeChildren renders each child of an And or Or.
func describeChildren(children []predicate.Node) []map[string]any {
	out := make([]map[string]any, len(children))
	for i, c := range children {
		out[i] = describePredicate(c)
	}
	return out
}

// fieldValue renders the {field, value} pair an Eq, Ne, or Cmp compares.
func fieldValue(path []string, value any) map[string]any {
	return map[string]any{"field": pathString(path), "value": value}
}

// pathString joins a field path with dots.
func pathString(path []string) string {
	return strings.Join(path, ".")
}

// opName is the plan label for a range operator.
func opName(op predicate.Op) string {
	switch op {
	case predicate.Gt:
		return "gt"
	case predicate.Ge:
		return "ge"
	case predicate.Lt:
		return "lt"
	default: // Le
		return "le"
	}
}

// ExplainWrite describes, without connecting, the write a copy into this keyspace
// would make: a pipelined, type-aware reconstruction of each key. Upsert DEL's an
// aggregate before rewriting it (so it is replaced, not appended); InsertOnly
// writes only absent keys.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := "pipeline per key: DEL then SET / HSET / RPUSH / SADD / ZADD / XADD / JSON.SET by type (replace)"
	if mode == query.InsertOnly {
		op = "pipeline EXISTS then, for absent keys only, SET / HSET / RPUSH / SADD / ZADD / XADD / JSON.SET by type"
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a Redis keyspace.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"FLUSHDB: empty the logical database, which itself persists"}}
}

// ExplainDrop reports that `iq data drop` is unsupported for Redis: a DB index is
// a fixed container that can be emptied (clear) but not removed. The false return
// is what the command surfaces, matching the absent Dropper capability at runtime.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"unsupported: a redis DB index cannot be removed; use `iq data clear` to empty it"}}, false
}

// ExplainDelete describes the `iq data delete` op for a Redis keyspace: a chunked
// DEL whose reply gives the exact deleted count.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DEL key...: remove the named keys (reply counts those that existed; the rest were already absent)"}}, true
}
