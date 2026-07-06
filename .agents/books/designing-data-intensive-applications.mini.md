# Designing Data-Intensive Applications — mini
Martin Kleppmann.
- Make trade-offs explicit at every call: source of truth, consistency expected, staleness tolerated, retry behaviour, duplicate or reordered results, partial failure.
- Treat crashes, partial writes, duplicate delivery, timeouts and stale reads as normal inputs, not exceptions; distinguish accepted, applied and durable success.
- Describe the workload concretely — request rate, data volume, access pattern, latency, percentiles — before choosing a query shape or index assumption.
- Choose the query and data model from access patterns, relationships and update locality; a document store rewards queries that match its partition and index layout.
- Know the consistency of a read: a replica or secondary read may be stale; require read-your-writes, monotonic or consistent-prefix only where the logic needs it.
- Respect partitioning: expect hot keys, skew and routing cost; a cross-partition scan or secondary-index query is not free; keep the ordinary path local.
- Treat the document or record shape as an evolving contract; tolerate old fields, missing fields and new fields across mixed producer and consumer versions.
- Make any command retried after a timeout idempotent — a dedup key or naturally idempotent transition — because an unknown-success write may have applied.
- Preserve only the ordering the logic needs, scoped per key or partition; do not assume global order across a NoSQL cluster.
- Paginate or stream large reads; treat a full-collection scan as a cost to justify, not a default.
- Avoid exactly-once wishful thinking and hidden distributed-system contracts; make lag, retries and failures observable.
