package redis

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Redis calls that this driver makes if it runs a
// classified query, without connecting. Redis pushes no filter to the server (a
// scan always uses MATCH *). The ops show the pipelined TYPE and per-type read. A
// compiled predicate also runs the rawpred prefilter on RedisJSON values. A bounded
// key read has no filter stages.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, _ bool) query.AccessPlan {
	const typedReads = "then per key by type: GET / HGETALL / LRANGE 0 -1 / SMEMBERS / ZRANGE 0 -1 WITHSCORES / JSON.GET"
	if !keys.Scan {
		return query.AccessPlan{Ops: []string{
			fmt.Sprintf("pipeline TYPE for %d requested key(s)", len(keys.Keys)),
			typedReads,
		}}
	}
	prefilter := query.StagePlan{State: query.StageOff, Note: "no compiled predicate"}
	if pred != nil {
		prefilter = query.StagePlan{
			State: query.StageOn,
			Scope: "rawpred on the raw bytes of RedisJSON values, before decode",
		}
	}
	return query.AccessPlan{
		Ops: []string{
			fmt.Sprintf("SCAN 0 MATCH * COUNT %d: cursor over the whole keyspace", scanCount),
			"per page: pipeline TYPE for the page's keys",
			typedReads,
		},
		Server:    query.StagePlan{Note: "Redis filters nothing on the server"},
		Prefilter: prefilter,
	}
}

// ExplainWrite describes, without connecting, the write that a copy into this
// keyspace makes if it runs: a pipelined, type-aware reconstruction of each key.
// Upsert DEL's an aggregate before rewriting it (so it is replaced, not appended).
// InsertOnly writes only absent keys.
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
