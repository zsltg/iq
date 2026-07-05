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
