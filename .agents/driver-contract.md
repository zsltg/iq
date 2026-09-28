# Driver contract
Cross-driver conventions every backend adapter and driver plan doc follows. Telegraph style, every line binds.
## Scope
- Companion to AGENTS.md "Adding a driver": the four gates decide whether a datastore gets an adapter; this contract decides what the adapter and its plan look like.
- Binds shipped adapters (`drivers/*`), the plan docs in the repo root, and every review of either; a plan that deviates states the deviation and why.
## URL contract
- TLS scheme: the backend's official TLS scheme wins (`rediss`, `couchbases`, `valkeys`); otherwise mint a `+s` twin (`elasticsearch+s` precedent); `?tls=true` only when the URL feeds an SDK parser that owns the scheme (ferretdb).
- Credentials ride in the URL userinfo, spliced from the keyring when the source is keyring-backed; every rendering of a location goes through the redaction helper; never log or trace a credential, and print one only behind an explicit `--reveal`.
- Exception by design: dynamodb-style backends take credentials from the SDK's default environment chain, never the URL.
- Keyspace param uses the backend's native noun: `?collection=` `?table=` `?set=` `?type=` `?measurement=` `?label=` `?edge=` `?rel=` `?index=` `?bucket=`; a query overrides it with the dotted `handle.<keyspace>` suffix.
- The driver registry entry lists its keyspace params in `addressParams`, most specific first; `iq add` names a source after the first one the URL sets, and a backend declaring none rejects every spelling.
- Database-level container: the URL path where the backend natively addresses by path, else `?database=`; never `?db=`.
- `?key=` picks which field or property is the key; `?keytype=` pins the key's type or encoding and is the only spelling — it supersedes `?idtype=`, `?vid=` and `?rowkeytype=`.
- Missing-keyspace sentinel error follows the uniform shape: "no <keyspace> selected; address it as handle.<keyspace> or set ?<keyspace>= in the source url".
## Key model
- Every driver defines one canonical string rendering per key and documents it in its README block.
- Key mapping is bijective on canonical keys; where alternate spellings collapse (case, padding, display syntax), state the "identity holds on canonical keys only" caveat.
- Composite key = JSON array of the components in schema order.
- Validate the key at function entry and fail fast; never pass a malformed key to the backend.
## Value shape
- Two-tier identity rule: a field the backend stores inside the document (mongo `_id`, couchdb `_id`/`_rev`, arango `_key`) always stays in the value; synthetic injection of identity into the value happens only where the backend returns identity separately from the payload and the driver needs self-describing streams or file-format parity (shipped elasticsearch and neo4j, planned janusgraph).
- Injecting a reserved key guards against collision: a document that already carries the reserved key with a different value fails the read; never silently overwrite user data.
- Vectors ride under a `_vectors` key when the backend stores them outside the payload (qdrant, weaviate); they stay named schema fields when the backend stores them inside it (milvus).
- Exec search rows always carry the row identity plus the backend's own metric vocabulary (`_score`, `_distance`); do not rename a backend's metric to another backend's word.
- TypedScan `Type` discriminator: `"document"` default; backend natives where the type changes decoding (redis `string|hash|list|set|zset|stream|json`, `"point"`, `"node"`, `"vertex"`/`"edge"`).
## Reads
- Get on a missing key omits it from the returned map, never an error; a key present with a nil value means a stored JSON null.
- Pagination ladder, take the highest rung the SDK offers: SDK iterator or stream, then native cursor/PIT/bookmark, then keyset (sorted key + range predicate) with a short-page exit, then offset paging only when the backend offers nothing else and then with the three-exit pattern (empty page, short page, offset >= total).
- Never hand-roll OFFSET/SKIP arithmetic when a cursor or keyset exists; manual skip paging times out the mutation gate and rescans the keyspace per page.
- Page size 100 unless the backend dictates otherwise; state a deviation in the plan.
- Weak-scan honesty: where the backend's scan guarantees are weak (resized-keyspace repeats, eventual consistency), say so in the README block instead of pretending exactness.
## Pushdown
- A pushed predicate is a conservative superset of the jq filter; the full jq re-runs client-side as the safety net, so results are identical with or without the push.
- Negations must be exact: push a negation only when the backend's semantics match jq's on every input including missing and null; otherwise decline the conjunct.
- Gate pushes on schema or mapping where the backend has one (elasticsearch precedent: push a term only onto an exactly-matchable field).
- The core portable-regex gate exists (`internal/pushdown`); a plan refusing Regex or Cmp pushdown states why the gate or a widened driver-local form is insufficient, never blanket-refuses.
- Every plan and every README driver block carries the push/not-push table in the valkey plan's format.
- `--explain` reuses the scan translator so the plan shows the real pushed fragment; never a second hand-written rendering.
- An unproven push is an E-numbered experiment in the plan, never an assumption.
- The per-driver push table plus client re-filter is SQL++'s pushdown-correctness-as-configuration-matching (arXiv:1405.3631): push a conjunct only where the driver's semantics match jq's, the client re-run is the correctness net.
- Trino is the industrial precedent: per-connector pushdown capability, conservative refusal on a type coercion the backend cannot match, EXPLAIN-observable delegation (trino.io pushdown docs) — iq's push tables and `--explain` are the same contract.
## Writes
- Exact-round-trip invariant: a successful Put stores the value exactly as given or the driver rejects; never silently wrap, coerce or truncate.
- Non-object value where the backend needs a document rejects with the uniform hint: `<driver>: value for key %q is not a JSON object; transform explicitly, e.g. iq 'if type == "object" then . else {value: .} end' --insert <dest>`.
- Never invent data on write: no synthesized vectors, no defaulted fields the user did not pass.
- Keyless-Put ladder: backend or SDK mints the id, else the client mints UUIDv4 (never ad-hoc hex), else `ErrNoKey` for external-identity KV stores, else derive-from-value or refuse where identity is value-derived (influx).
- Where the backend lacks primary-key uniqueness (milvus), the uniqueness pre-read is load-bearing; do not drop it for speed.
- Overwritten accounting ladder: derive from the write response, else one batch key pre-read per page (accounting only, non-atomicity documented, never changes what is written), else report all Written and document the gap; never a per-record query.
- InsertOnly is exact where the backend can express it; a best-effort mechanism states its caveat (hbase guard-cell precedent).
- Clear is refused when it would exceed the keyspace the source addresses (valkey/dragonfly `?index=` scoping).
## Admin ports
- Estimator exists iff the backend answers without a row scan: O(1) metadata or an index-backed server-side count; staleness and overcount are tolerated and documented (the firestore COUNT ruling is the precedent).
- No cheap answer, no port: couchbase, influx, dgraph, surreal and janusgraph omit Estimator; redis, valkey, dragonfly and memcached carry one (DBSIZE, curr_items).
- Dropper exists iff the container is removable by the backend; a redis logical DB is not.
- Deleter exists iff the backend removes a record by its canonical key; a backend with no stable per-key identity (influx point, file dump) omits it and `iq data delete` reports it unsupported.
- Deleter accounting mirrors Writes: exact where the backend signals absence (redis DEL, mongo DeletedCount, es deleted/not_found), a bounded key pre-read where it does not (cassandra, dynamodb, hbase); a missing key is Missing, never an error.
- Exec `count` verb is exact, Estimator is a hint; the two coexist and neither substitutes for the other.
## Exec families
- Native-language backends: `iq exec` passes the query verbatim (plus an optional JSON params argument); no dialect wrapping.
- Single-JSON-document backends keep that document as the exec payload.
- Routed-native-expression backends — native query text but no top-level statement language (milvus expressions, qdrant filter JSON) — ship `query|search|count|delete`: one routing verb per API family, every payload native syntax; iq invents the routing word only, never an expression grammar.
- Raw-protocol backends (redis family, memcached) pass the wire command through.
- No textual query language → no exec surface: `Query` returns a clear "exec is not supported" error pointing at the structured commands; invented verb vocabularies (get/scan/put words mapped onto RPCs) are barred. Shipped hbase and the aerospike/firestore/etcd plans predate this rule; retrofit is a tracked follow-up.
- Exec may write where a native form expresses it; each driver documents that it does; per-key delete rides in exec as the raw escape hatch where exec exists, and the typed Deleter port (`iq data delete`) covers it alongside.
## Safety
- Every outbound call is context-bounded with an explicit timeout; no infinite waits.
- No retry loop in driver code unless the operation is idempotent and the loop is bounded with backoff and jitter (dynamodb precedent); never retry validation or permanent failures.
- Every plan states its read-consistency or staleness default in one line.
- Build queries injection-safe: parameters or structured builders, charset-whitelisted identifiers, never string-concatenated user input.
- Trace logs record shapes and counts, never values or credentials.
- Telemetry off on both client and server; known switches: gocb `AppTelemetry`, `FERRETDB_TELEMETRY`, weaviate `DISABLE_TELEMETRY`, dgraph events endpoint, dragonfly `--version_check`, influx `--disable-telemetry-upload`.
## Test & infra
- Use a testcontainers module only when it pays its way; otherwise `GenericContainer` with an explicit wait strategy.
- Readiness means serving, not listening: wait on a real protocol probe, not an open port.
- Every integration suite honors an `IQ_<DRIVER>_URL` override before starting a container.
- Isolate per test at the cheapest unit the backend offers (logical DB, keyspace, unique collection per run).
- Ship `scripts/seed-<driver>.sh` and a compose service named after the driver with `container_name: iq-<driver>`.
- Mutation gate: run with the stack up, commit before gating, prefer SDK paginators and keyset loops (offset loops time out the gate), known residue classes are cursor loops and backoff timing.
- Host ports are allocated once, below; a plan that claims a taken port remaps its own side, never the incumbent's.

| Service | Host port(s) | Note |
|---|---|---|
| redis | 6379 | shipped |
| mongo | 27017 | shipped |
| cassandra | 9042 | shipped |
| dynamodb | 8000 | shipped |
| couchdb | 5984 | shipped |
| neo4j | 7687, 7474 | shipped |
| elasticsearch | 9200 | shipped |
| opensearch | 9201:9200 | shipped |
| hbase | host network | shipped; binds 2181 + 16000/16010/16020/16030 |
| aerospike | 3000 | |
| arangodb | 8529 | |
| bigtable | 8086 | emulator's native default |
| couchbase | 8091-8096, 11210 | |
| dgraph | 9080 gRPC, 8180:8080 health | remapped; 8080 goes to firestore |
| dragonfly | 6381:6379 | |
| ferretdb | 27018:27017 | |
| firestore | 8080 | keeps its claim |
| influxdb | 8181 | |
| janusgraph | 8182 | |
| memcached | 11211 | |
| milvus | 19530, 9091 health | |
| qdrant | 6333, 6334 | |
| scylladb | 9043:9042 | |
| surrealdb | 8001:8000 | remapped; 8000 is dynamodb |
| valkey | 6380:6379 | |
| weaviate | 8081:8080, 50051 gRPC | remapped; 8080 goes to firestore |
## Docs
- A driver change touches its README surface in the same change: the `driver ls` row, the scheme lists, the driver details block with its push/not-push table, and the Architecture prose.
- The README Mermaid diagrams are port-level: edit one only when a port or a diagram arm changes, never for a backend-adapter change.
- Environment variables land in `.env.example` in the same change.
- Plan docs open with the preamble line "Working document, untracked. Plan only; no code has been written." and use the canonical headings: Context (with Decisions locked with the user), Gate assessment, Design decisions, Package layout, Wiring & repo surface, Tests, Explicit experiments (E-numbered), Implementation order, Follow-ups (optional), Definition of Done & honest v1 cuts.
- A plan carrying unverified claims appends Unverified facts (flagged) and Sources sections.
