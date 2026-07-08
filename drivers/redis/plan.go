package redis

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Redis calls this driver would make for a classified
// query, without connecting. Redis has no server-side filter (a scan always uses
// MATCH *), so Filter is always nil and the whole jq runs client-side; the ops
// reflect the pipelined TYPE + per-type read the driver actually issues. The
// predicate is unused: nothing pushes down to Redis.
func ExplainPlan(keys selector.KeySet, _ predicate.Node, _ bool) query.AccessPlan {
	const typedReads = "then per key by type: GET / HGETALL / LRANGE 0 -1 / SMEMBERS / ZRANGE 0 -1 WITHSCORES / JSON.GET"
	if !keys.Scan {
		return query.AccessPlan{Ops: []string{
			fmt.Sprintf("pipeline TYPE for %d requested key(s)", len(keys.Keys)),
			typedReads,
		}}
	}
	return query.AccessPlan{Ops: []string{
		fmt.Sprintf("SCAN 0 MATCH * COUNT %d: cursor over the whole keyspace", scanCount),
		"per page: pipeline TYPE for the page's keys",
		typedReads,
		"no server-side filter — every key is read and filtered client-side",
	}}
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
