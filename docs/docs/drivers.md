# Drivers

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

`iq` picks the backend from a source's URL scheme, and the query core is driver-agnostic, so
further backends slot in behind the same port. Each driver below documents its keyspace mapping,
value encoding, predicate pushdown, and raw-command escape hatch. `iq driver ls` lists the
registered backends — the same canonical names `iq ls -v`, `ping`, `inspect`, and `diff` report:

```bash
$ iq driver ls
DRIVER         DESCRIPTION                                     SCHEMES                                            VERSIONS       DOC
mongo          MongoDB document store                          mongodb, mongodb+srv                               4.2+           https://www.mongodb.com/docs/
cassandra      Apache Cassandra wide-column store              cassandra                                          3.11+          https://cassandra.apache.org/doc/
dynamodb       Amazon DynamoDB key-value and document store    dynamodb                                           AWS (managed)  https://docs.aws.amazon.com/dynamodb/
hbase          Apache HBase wide-column store                  hbase                                              1.0+           https://hbase.apache.org/book.html
couchdb        Apache CouchDB document store                   couchdb, couchdbs                                  2.x, 3.x       https://docs.couchdb.org/
couchbase      Couchbase document store                        couchbase, couchbases                              7.x, 8.x (Community or Enterprise)  https://docs.couchbase.com/
neo4j          Neo4j property graph store                      neo4j, neo4j+s, neo4j+ssc, bolt, bolt+s, bolt+ssc  5.x            https://neo4j.com/docs/
elasticsearch  Elasticsearch search engine and document store  elasticsearch, elasticsearch+s                     8.x            https://www.elastic.co/docs/
opensearch     OpenSearch search engine and document store     opensearch, opensearch+s                           2.x, 3.x       https://opensearch.org/docs/
redis          Redis key-value store                           redis, rediss                                      7.0+           https://redis.io/docs/
file           Local dump file, read-only                      file

# -v appends the file driver's readable dump formats:
$ iq driver ls -v
… (driver rows as above) …
file dump formats — a bare name auto-detects (file:///<file_path>), the ?format= form must be passed (file:///<file_path>?format=<source_format>):
  jsonl                  iq typed JSON Lines / array
  yaml                   iq typed YAML
  mongoexport            mongoexport Extended JSON
  bson                   mongodump BSON
  rdb                    Redis RDB snapshot
  ?format=dynamodb-json  DynamoDB S3 export / scan JSON
  ?format=cassandra-csv  cqlsh COPY TO CSV
  ?format=neo4j-json     Neo4j APOC JSON export
```

Add `-j`/`--json` or `-y`/`--yaml` for machine-readable rows (see [Sources](sources.md) for the full flag).

!!! note

    `VERSIONS` is the range of backend server versions the bundled client library supports —
    [`go-redis` v9](https://github.com/redis/go-redis) for Redis, the
    [MongoDB Go driver v2](https://www.mongodb.com/docs/drivers/go/current/) for MongoDB, the
    [Apache Cassandra gocql driver v2](https://github.com/apache/cassandra-gocql-driver) for
    Cassandra, the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) for DynamoDB
    (`AWS (managed)` — a managed service with no server version),
    [gohbase](https://github.com/tsuna/gohbase) (native protobuf RPC, no Thrift gateway) for HBase,
    [`kivik` v4](https://github.com/go-kivik/kivik) for CouchDB,
    the [Couchbase Go SDK v2 (`gocb`)](https://github.com/couchbase/gocb) for Couchbase,
    the [Neo4j Go driver v5](https://github.com/neo4j/neo4j-go-driver) for Neo4j,
    the [go-elasticsearch v8](https://github.com/elastic/go-elasticsearch) client for
    Elasticsearch,
    and the [opensearch-go v4](https://github.com/opensearch-project/opensearch-go) client for
    OpenSearch (a fork of go-elasticsearch without the product check that refuses non-Elasticsearch
    servers) —
    not a matrix `iq` tests against.
    The integration tests are pinned to `redis:8`, `mongo:8`, `cassandra:5`,
    `amazon/dynamodb-local:2.5.2`, `harisekhon/hbase:2.1`, `couchdb:3`,
    `couchbase:community-7.6.2`, `neo4j:5`,
    `docker.elastic.co/elasticsearch/elasticsearch:8.17.4`, and
    `opensearchproject/opensearch:2.17.1`.

### What every driver guarantees

The per-driver blocks below differ in encoding and pushdown detail, but every backend honors the
same contract:

- **One URL, native nouns.** The URL scheme picks the driver; the keyspace rides in the URL as the
  backend's own noun (`?collection=`, `?table=`, `?database=`, `?label=`/`?rel=`, `?index=`), and a
  query overrides it per run with the dotted `handle.<keyspace>` suffix (see [Sources](sources.md)).
- **One jq surface.** A bounded filter fetches exactly the named keys — a missing key reads as
  `null`, never an error; a `.[]`-rooted filter streams the keyspace in bounded pages; a holistic
  filter materializes only behind `--unbounded` (see
  [Bounded reads, streaming scans, and materialized scans](architecture.md#bounded-reads-streaming-scans-and-materialized-scans)).
- **Pushdown never changes results.** A pushed predicate is only ever a conservative pre-filter —
  server-side where the backend can filter, or a client-side raw-byte prefilter that drops a provable
  non-match before decode where it cannot (Redis, on RedisJSON values; Elasticsearch/OpenSearch and
  Couchbase, over the residual their server-side query could not narrow). The full jq always re-runs
  client-side, so
  output is identical with or without it, and [`--explain`](output.md#query-plan-explain-v) shows exactly
  what was pushed.
- **Capabilities are explicit.** Filtered scans, count estimates, writes, clear, drop, and per-key
  delete are opt-in ports: a backend implements what its model supports, and a command against a
  missing capability fails with a clear message instead of emulating it (Redis, whose DB index cannot
  be removed, simply has no `drop`; the read-only file dump has no per-key `delete`).
- **Values round-trip.** Every value normalizes to JSON under a frozen per-backend encoding
  contract, and a `--typed` dump restores through `--insert` losslessly (see
  [Moving data](moving-data.md)).
- **Bounded and redacted.** Every backend call is bounded by `--timeout`, and a URL's password is
  redacted from every listing, log line, and error.
- **A native escape hatch.** `iq exec` speaks the backend's own language — verbatim where one exists
  (Redis commands, Mongo command documents, CQL, PartiQL, Cypher, Mango, the Elasticsearch DSL), a small
  fixed verb set where none does (HBase) — see each driver's Raw commands section.

## Redis

*Value encoding and raw commands*

Register a `redis://` source and the same jq interface works against the Redis keyspace, where **a
key maps directly to a Redis key and the value is whatever that key holds**. The database index
comes from the URL path (`/0`); every value is a string, so numeric comparisons need `tonumber`:

```bash
iq add -n cache redis://localhost:6379/0             # register once, then:
iq --src cache '.greeting'                           # fetch key "greeting"
iq --src cache '.[] | select((.year|tonumber) > 2015) | .title'  # streamed
iq --src cache --unbounded 'keys'                    # every key
```

The `--unbounded` / streaming rules match every backend (`.[]`-rooted filters stream in constant
memory; `keys`/`.`/`map` materialize and require the flag). Redis has no server-side filtering, so a
compiled predicate instead drives a **client-side raw-byte prefilter**: on a streaming scan, each
RedisJSON value is tested against the predicate on its raw JSON.GET bytes and, when it provably
cannot match, dropped before the (dominant) decode — every other type is decoded and included
unchanged. The full jq always re-runs client-side, so output is identical with or without it; the
prefilter only skips decoding documents the filter would reject. `--no-compile` turns it off.

### Value encoding

Each fetched Redis value is normalized to JSON by type:

| Redis type | JSON shape |
| --- | --- |
| string | the string verbatim (numeric strings stay strings; use `tonumber`) |
| hash | object `{field: value}` |
| list | array, in list order |
| set | array, sorted lexically (sets have no native order) |
| sorted set | array of `{"member": ..., "score": ...}`, in ascending score order |
| stream | array of `{"id": ..., "fields": {field: value}}`, in entry order |
| RedisJSON | the stored document, parsed as JSON |
| missing key | `null` |

Other module types (time series, bloom, …) have no frozen encoding yet; a named read of one is
refused with a clear message.

### Raw commands

`iq exec` forwards a command to the database verbatim and prints the reply in redis-cli style —
the escape hatch for writes, administration, and seeding the jq read path does not cover:

```bash
./iq exec SET greeting hello   # "OK"
./iq exec GET greeting         # "hello"
./iq exec INCR counter         # (integer) 1
./iq exec GET missing          # (nil)
```

Its output mirrors redis-cli's cooked style: bulk strings quoted, integers as `(integer) N`, a
missing value as `(nil)`, and arrays as a numbered, indented list. The client uses RESP2 so
aggregate replies match redis-cli's classic flat output. Status replies such as `OK` and `PONG`
appear quoted, a limitation of the underlying client, which does not distinguish them from bulk
strings.

### Pushdown

Redis `SCAN 0 MATCH * COUNT n` plus per-key `TYPE`/typed reads (naming the
client-side raw-byte prefilter when a predicate compiles), or pipelined typed
reads for bounded keys.

### Compiled Filter

The predicate Redis's client-side prefilter applies to RedisJSON values (empty
under `--no-compile`, or when no conjunct pushes).

## MongoDB

*Collection keyspace, predicate pushdown, and connection*

The backend is chosen by the source's URL scheme. Register a `mongodb://` source and the same jq
interface works against a collection, where **the collection is the keyspace: a document's `_id`
is the key and the document is the value**. The database comes from the URI path; the collection
from the URL's `?collection=` (the driver's own connection option, overridable per run with a dotted
`handle.collection`):

```bash
iq add -n books 'mongodb://localhost:27017/iq?collection=books' # register once, then:
iq --src books '.["2"]'                              # fetch document _id "2"
iq --src books '.[] | select(.year > 2015) | .title' # streamed
iq --src books --unbounded 'keys'                    # every _id
```

Because Mongo values are natively typed, numeric comparisons like `.year > 2015` need no
`tonumber` — unlike Redis, where everything is a string. Documents normalize to JSON with the
same rules everywhere: an `ObjectID` becomes its hex string, a date becomes an RFC 3339 string,
numbers stay numbers, nested documents and arrays are preserved. A missing `_id` reads as `null`.
The `--unbounded` / streaming rules are identical to Redis (`.[]`-rooted filters stream a cursor
in constant memory; `keys`/`.`/`map` materialize and require the flag).

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality** clauses are translated into a native Mongo
query so the server does the filtering (and can use an index) before the documents ever reach iq:

```bash
iq --src books '.[] | select(.author == "Robert C. Martin") | .title'   # pushed down
iq --src books --no-compile '.[] | select(.author == "Robert C. Martin") | .title'  # forced client-side
```

Pushdown never changes results, only speed: the full jq always re-runs client-side over whatever
comes back, so a pushed filter is only ever a conservative pre-filter. Pass `--no-compile` to skip
it and stream the whole collection, filtering entirely client-side. What it can push:

| `select(...)` clause | Pushed | MongoDB translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{a: x}` | number, string, bool, or null literal |
| `.a == 1 or .a == 2` | ✓ | `{a: {$in: [1, 2]}}` | an `or` of equalities on one field |
| `.a > n`, `>=`, `<`, `<=` | ✓ | native op + `$type` guards (an `$or`) | number/string literal; reproduces jq's cross-type order so the match is never a subset |
| `.a \| test("re")` | ✓ | `{a: {$regex: "re", $options: "is"}}` | portable patterns only (below); jq's `i` and `m` flags, with jq's `m` (dot-matches-newline) mapped to PCRE's `s` |
| `has("a")`, `.a \| has("k")` | ✓ | `{a: {$exists: true}}` | exact — key presence, like jq's `has()` |
| `.a \| length == n` | ✓ | `{$size: n}` + `$type` guards (an `$or`) | jq `length` is polymorphic (array/string/object/number), so guards keep it a superset |
| `.a \| any(cond)` | ✓ | `{a: {$elemMatch: cond}}` (an `$or` with an object guard) | an array element satisfying a pushable element predicate; `cond` may combine the rows above |
| `.a != x` | ✓ | `{$or: [{a: {$ne: x}}, {a: {$type: "array"}}]}` | exact negation of equality (the guard keeps arrays, which jq never equates to a scalar) |
| `has("a") \| not` | ✓ | `{a: {$exists: false}}` | exact negation of existence |
| `.a \| any(.f == v) \| not` | ✓ | `{a: {$not: {$elemMatch: …}}}` | no array element matches — the element condition must be exact equality |
| `E1 and E2`, `E1 or E2` | ✓ | `$and` / `$or` of the above | an `and` may push only its pushable parts and drop the rest |
| negated range/regex/`size` | — | — | their filters are supersets, and a negated superset is a subset (unrecoverable) |
| `.a > true`, `.a < null` | — | — | a range against bool/null has no clean superset |
| non-portable regex | — | — | engine-specific construct (below) |
| anything else | — | — | runs client-side, as under `--no-compile` |

**Portable regex.** iq's jq is [gojq](https://github.com/itchyny/gojq), which compiles a `test()`
pattern with Go's RE2; MongoDB uses PCRE. A pattern is pushed only when every construct it uses
means the same — or a superset — in both: literals, anchors (`^` `$`), `.`, quantifiers
(`* + ? {n,m}`), alternation (`|`), groups, character classes, the ASCII `\d` `\w` `\s` `\D` `\W`
shorthands, and word boundaries (`\b`, `\B`). `\S` is the one shorthand held back: RE2's `\s` omits
the vertical tab that PCRE's `\s` matches, so RE2's `\S` matches a vertical tab PCRE's does not, and
pushing it would drop a document jq keeps (`\s` diverges the other way — a superset the client-side
re-run corrects). Flags follow the same rule: gojq accepts only `i`, `m`, `g`, and iq pushes `i`
(case-insensitive) and `m` — which in jq means "`.` matches newline" (dotall) and so maps to PCRE's
`s`, not PCRE's `m`. A pattern using lookaround (`(?=…)`), backreferences (`\1`), unicode properties
(`\p{…}`), POSIX classes (`[[:…:]]`), or possessive quantifiers is not portable and stays
client-side, so the pushed set always equals jq's.

On Redis, or for any filter with no pushable predicate, pushdown is a harmless no-op.

`iq exec` on MongoDB runs a single JSON command document with `runCommand` and prints the reply as
JSON — the escape hatch for server-side queries, aggregation, and administration:

```bash
iq --src books exec '{"find":"books","filter":{"year":{"$gt":2015}}}'
iq --src books exec '{"aggregate":"books","pipeline":[{"$group":{"_id":null,"avg":{"$avg":"$price"}}}],"cursor":{}}'
```

### Connection

The database is a saved [source](sources.md) — a connection URL whose scheme selects the backend,
`redis://[user:pass@]host:port[/db]` (`rediss://` for TLS) or `mongodb://host:port/db`
(`mongodb+srv://` too). A query resolves its source in this order:

1. the `--src` / `-s` flag
2. the active source (`iq src <name>`)

With no source selected the command errors — there is no ambient URL or environment fallback.
A dotted `--src handle.collection` (or `handle.collection` positional, for `inspect`/`data`/`diff`)
overrides the source URL's MongoDB `?collection=` default for one run (rejected for Redis, which has
no collections); `--timeout` (default `5s`) bounds each query.

### Pushdown

MongoDB `find(<filter>)` (the pushed-down filter, on by default, or `{}` under
`--no-compile`) or `find` by `_id`.

### Compiled Filter

MongoDB's server-side `find` filter.

## Apache Cassandra

*Table keyspace, primary-key mapping, predicate pushdown, and CQL*

Register a `cassandra://` source and the same jq interface works against a table, where **the table
is the keyspace: a row's primary key is the key and the row is the value**. The keyspace comes from
the URL path; the table from the URL's `?table=` (overridable per run with a dotted `handle.table`);
multiple contact points are comma-separated:

```bash
iq add -n books 'cassandra://localhost:9042/iq?table=books' # register once, then:
iq --src books '.["2"]'                              # fetch the row whose primary key is 2
iq --src books '.[] | select(.year > 2015) | .title' # streamed
iq --src books --unbounded 'keys'                    # every primary key
iq add -n cl 'cassandra://user:pass@n1,n2:9042/app?table=orders' # auth + multiple hosts
```

Like MongoDB, Cassandra columns are natively typed, so `.year > 2015` needs no `tonumber`. The
`--unbounded` / streaming rules are identical to every backend. The table's schema is read once at
connect time, so the driver knows the primary-key columns and their types.

### Key encoding

A row's key is its **full primary key** — the partition-key columns followed by the clustering
columns. A single-column primary key renders as its bare value (`42`, a uuid, a text value — like a
Mongo `_id`); a composite primary key renders as a compact JSON array in schema order:

```bash
iq --src sales '.["US"]'                # single-column key, bare
iq --src sales '.["[\"US\",1]"]'        # composite key ((country), id) as a JSON array
```

The array elements are the columns' string forms and are coerced back through the schema on lookup,
so a `bigint`/`varint` key round-trips without precision loss.

### Value encoding

Each column value is normalized to JSON by CQL type:

| CQL type | JSON shape |
| --- | --- |
| `text`, `varchar`, `ascii` | the string verbatim |
| `int`, `bigint`, `smallint`, `tinyint`, `counter` | number |
| `varint` | number when it fits, else its exact decimal string |
| `float`, `double` | number |
| `decimal` | exact string (or a number under `--format.decimal number`) |
| `boolean` | `true` / `false` |
| `uuid`, `timeuuid` | canonical string |
| `timestamp` | RFC 3339 string |
| `blob` | base64 string |
| `inet` | address string |
| `list`, `set` | array (a set is returned sorted) |
| `map` | object (non-text keys stringified) |
| missing / null cell | omitted (reads as `null` in jq) |

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality** clauses are translated into a CQL `WHERE` so
the cluster filters before rows reach iq. Only equality and same-column membership are pushed:

| `select(...)` clause | Pushed | CQL translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `a = ?` | a single top-level column that exists in the table |
| `.a == 1 or .a == 2` | ✓ | `a IN (?, ?)` | an `or` of equalities on one column |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| ranges, regex, `has`, `length`, negations, nested paths | — | — | run client-side; ranges are skipped because jq treats a missing field as the lowest value, which CQL cannot reproduce |

A pushed `WHERE` that does not resolve to the full partition key runs with `ALLOW FILTERING`, so the
coordinator does the scan — an opt-in cost (it is shown in `--explain`). Pushdown never changes
results, only speed: the full jq always re-runs client-side, so a pushed filter is a conservative
pre-filter. Pass `--no-compile` to stream the whole table and filter entirely client-side.

### Raw commands

`iq exec` runs a CQL statement verbatim and prints the rows as JSON — the escape hatch for
server-side queries, DDL, and administration the jq read path does not cover:

```bash
iq --src books exec 'SELECT release_version FROM system.local'
iq --src books exec "SELECT title FROM books WHERE year > 2015 ALLOW FILTERING"
```

`iq inspect` reads the system schema — `local` (cluster/version), `tables` (the keyspace's tables),
and `columns` (a table's columns); `--only` narrows to those subcommands.

## Amazon DynamoDB

*Table keyspace, primary-key mapping, predicate pushdown, and PartiQL*

Register a `dynamodb://` source and the same jq interface works against a table, where **the table
is the keyspace: an item's primary key is the key and the item is the value**. The region is the URL
host; the table rides in the URL's `?table=` (overridable per run with a dotted `handle.table`). An
optional `?endpoint=` points at DynamoDB Local. **Credentials never travel in the URL** — the AWS
default credential chain (environment, `~/.aws`, IAM role) resolves them, so no secret touches the
config or keyring:

```bash
iq add -n books 'dynamodb://us-east-1/?table=books'  # register once (creds from the AWS chain), then:
iq --src books '.["2"]'                               # fetch the item whose partition key is 2
iq --src books '.[] | select(.year > 2015) | .title' # streamed
iq --src books --unbounded 'keys'                     # every primary key
# DynamoDB Local: point at the endpoint; the driver supplies dummy credentials.
iq add -n local 'dynamodb://us-east-1/?table=books&endpoint=http://localhost:8000'
```

DynamoDB attributes are natively typed, so `.year > 2015` needs no `tonumber`. The `--unbounded` /
streaming rules are identical to every backend. The table's key schema is read once at connect time,
so the driver knows the partition and (optional) sort key and their types.

### Key encoding

An item's key is its **full primary key** — the partition key, then the sort key when the table has
one. A partition-key-only table renders the key as its bare value (`42`, a string — like a Mongo
`_id`); a table with a sort key renders a compact JSON array in schema order:

```bash
iq --src sales '.["US"]'                # partition-key-only, bare
iq --src sales '.["[\"US\",1]"]'        # composite key (partition "US", sort 1) as a JSON array
```

The array elements are the key attributes' string forms and are coerced back through the key schema
on lookup, so a numeric (`N`) or binary (`B`) key round-trips faithfully.

### Value encoding

Each attribute value is normalized to JSON by DynamoDB type:

| DynamoDB type | JSON shape |
| --- | --- |
| `S` (string) | the string verbatim |
| `N` (number) | integer as a number (exact string when it overflows int64); decimal as an exact string (or a number under `--format.decimal number`) |
| `BOOL` | `true` / `false` |
| `B` (binary) | base64 string |
| `NULL` | `null` |
| `M` (map) | object |
| `L` (list) | array |
| `SS`, `NS`, `BS` (sets) | array (of strings / numbers / base64 strings) |
| missing attribute | omitted (reads as `null` in jq) |

Note the set types (`SS`/`NS`/`BS`) normalize to a plain array, so a copy **back** into DynamoDB
writes them as a list (`L`), not a set; and a number presented as a string (auto/string decimal
mode) writes back as a string (`S`). Use `--format.decimal number` for a numeric round-trip.

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
a DynamoDB `Scan` `FilterExpression` so the service filters before items reach iq. Every attribute is
referenced through a `#name` placeholder, so a reserved word (`name`, `status`, `size`, `year`, …) is
always safe:

| `select(...)` clause | Pushed | FilterExpression | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `#a = :v` | a single top-level attribute; string, number, or boolean literal |
| `.a \| has` / `has("a")` | ✓ | `attribute_exists(#a)` | key presence, exact |
| `has("a") \| not` | ✓ | `attribute_not_exists(#a)` | key absence, exact |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `OR` of the parts | pushed only when **every** branch is pushable |
| ranges, regex, `length`, `!=`, nested paths | — | — | run client-side; ranges are skipped because jq orders a string above every number, which a typed DynamoDB comparison cannot reproduce |

A `Scan` reads the whole table (there is no `WHERE` on a primary-key membership like a relational
store); the `FilterExpression` only avoids shipping non-matching items over the wire — the cost is
shown in `--explain`. Pushdown never changes results, only speed: the full jq always re-runs
client-side, so a pushed filter is a conservative pre-filter. Pass `--no-compile` to stream the whole
table and filter entirely client-side.

### Raw commands

`iq exec` runs a [PartiQL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.html)
statement verbatim and prints the items as JSON — the escape hatch for server-side queries and
writes the jq read path does not cover:

```bash
iq --src books exec 'SELECT * FROM "books" WHERE id = 2'
iq --src books exec 'SELECT title FROM "books" WHERE "year" > 2015'
```

`iq inspect` reads table metadata — `tables` (the region's tables) and `table` (the selected table's
key schema, item count, size, billing mode, and index names); `--only` narrows to those subcommands.

## Apache HBase

*Table keyspace, row-key mapping, cell encoding, and shell-verb raw path*

Register an `hbase://` source and the same jq interface works against a table, where **the table is
the keyspace: a row key is the key and the row is the value**. The URL host is the ZooKeeper quorum
(comma-separated hosts, default port `2181`); the table rides in the URL's `?table=` (a
`namespace:table`, overridable per run with a dotted `handle.table`). The ZooKeeper znode parent
defaults to `/hbase`, overridable with `?znode=`:

```bash
iq add -n books 'hbase://localhost:2181/?table=iq_books' # register once, then:
iq --src books '.["1"]'                                  # fetch the row whose key is "1"
iq --src books '.[] | select(.cf.author == "Herbert")'   # streamed; nested family.qualifier
iq --src books --unbounded 'keys'                        # every row key
iq add -n zk 'hbase://z1,z2,z3:2181/?table=ns:events&znode=/hbase-unsecure' # quorum + namespace
```

A row is a **nested object**: `{family: {qualifier: value}}`, so a cell is addressed as
`.cf.title` in jq. The row key is the map key, not a field of the row.

### Value encoding

HBase stores **no types** — every cell is raw bytes — so a value is presented **honestly** by
default and **exactly** when you declare its encoding:

| Column | Read as | Written from |
| --- | --- | --- |
| undeclared (default) | valid UTF-8 → the string verbatim; otherwise a base64 string | a string → its UTF-8 bytes |
| `?types=cf:q=text` | the string verbatim | a string → its UTF-8 bytes |
| `?types=cf:q=bytes` | a base64 string | a base64 string → raw bytes (lossless for binary) |
| `?types=cf:q=int` / `long` | a number (4- / 8-byte big-endian, the HBase `Bytes` layout) | a whole number → those bytes |
| `?types=cf:q=double` | a number (8-byte IEEE-754) | a number → those bytes |
| `?types=cf:q=bool` | `true` / `false` (1 byte) | a bool → one byte |

The driver **never guesses** a numeric type from bytes (an 8-byte string is indistinguishable from a
`long`); it either *knows* (you declared it) or is *honest* (text, else base64). Declared columns
round-trip losslessly in both directions. An undeclared column read back as base64 (non-UTF-8 bytes)
does **not** round-trip through a write — declare it `bytes` for that. The row key is likewise
text-or-base64 unless `?keytype=` declares it (`&keytype=long`).

```bash
# declare the numeric columns so they read as numbers and .year > 2015 needs no tonumber:
iq add -n books 'hbase://localhost:2181/?table=iq_books&types=cf:year=long,cf:price=double'
iq --src books '.[] | select(.cf.year > 2015) | .cf.title'
```

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **column-equality** clauses are translated into an HBase
server-side filter (`SingleColumnValueFilter`, combined with `MustPassAll` for an `and`) so the
region servers filter before rows reach iq. The equality literal is encoded through the column's
declared type, so the comparison matches the stored bytes:

| `select(...)` clause | Pushed | HBase filter | Notes |
| --- | :---: | --- | --- |
| `.cf.q == x` | ✓ | `SingleColumnValueFilter(cf, q, =, x)` | a two-segment `family.qualifier` path; the literal must encode to the column's declared type (undeclared → string) |
| `E1 and E2` | ✓ | `FilterList(MustPassAll, …)` | drops any conjunct it cannot push (widening) |
| `.cf.q \| has`, ranges, regex, `length`, `!=`, `or` | — | — | run client-side; existence and `or` are not pushed because HBase has no clean superset-safe filter for them, and ranges cannot reproduce jq's cross-type ordering |

A row-key point read (`.["1"]`) is a direct `Get`, not a scan. Pushdown never changes results, only
speed: the full jq always re-runs client-side, so a pushed filter is a conservative pre-filter. Pass
`--no-compile` to stream the whole table and filter entirely client-side; the cost is shown in
`--explain`.

### Raw commands

HBase has **no query language**, so `iq exec` is a small, safe verb set mapped straight onto RPC —
never a built query string, so it is injection-safe. Each verb names its own table. Reads: `get`,
`scan`, `count`; writes: `put`, `delete` (values encoded through the same declared-type contract):

```bash
iq --src books exec get iq_books 1              # one row as JSON, or null
iq --src books exec scan iq_books 10            # up to 10 rows as {rowkey: row}
iq --src books exec count iq_books              # row count (a key-only scan)
iq --src books exec put iq_books 5 cf:title Dune  # write one cell
iq --src books exec delete iq_books 5           # delete the whole row (or `delete iq_books 5 cf:title` for one cell)
```

Structured writes go through `iq data` (`clear`/`drop`/`delete`, plus `--insert`) with write modes,
stats, and `--explain`; `iq data delete <table> <rowkey>…` is the typed, capability-gated per-key
delete that formalizes the raw `delete` verb below. The raw `put`/`delete` verbs remain the
lower-level escape hatch (a single cell, a column), mirroring the other drivers' raw paths. `iq
inspect` lists the source namespace's tables (`tables`).

## Apache CouchDB

*Database keyspace, _id mapping, Mango pushdown, and raw _find*

Register a `couchdb://` source and the same jq interface works against a database, where **the
database is the keyspace: a document's `_id` is the key and the document is the value**. The host is
the CouchDB server; the database rides in the URL's `?database=` (overridable per run with a dotted
`handle.database`, since one server hosts many databases). Use `couchdbs://` for TLS. **Credentials
travel in the URL userinfo** (HTTP basic auth), so `--store keyring` moves the password to the OS
keyring exactly as for the other backends:

```bash
iq add -n books 'couchdb://admin:password@localhost:5984/?database=iq'  # register once, then:
iq --src books '.["2"]'                                # fetch the document whose _id is 2
iq --src books '.[] | select(.year > 2015) | .title'  # streamed
iq --src books --unbounded 'keys'                      # every _id
iq --src books.other '.[]'                             # query a different database on the same server
```

CouchDB documents are JSON, so values need no type coercion. Integers keep exact precision (large
ones never collapse to a float); `_id` and `_rev` are kept in the document. Design documents
(`_design/…`) are database metadata and are skipped by scans. The `--unbounded` / streaming rules are
identical to every backend.

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality**, **range**, **existence**, byte-safe
**regex**, and **length** clauses are
translated into a Mango `_find` selector so the server filters before documents reach iq:

| `select(...)` clause | Pushed | Mango selector | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"a": x}` | equality; a `null` literal also matches an absent field |
| `.a > x` / `.a <= x` | ✓ | `{"$or": [{"a": {"$gt": x}}, …]}` | range, widened with `$type` clauses so jq's cross-type ordering (null < bool < number < string < array < object, matching CouchDB collation) is reproduced |
| `.a \| test("re")` | ✓ | `{"a": {"$regex": "re"}}` | byte-safe ASCII patterns only (below); a case-insensitive or non-ASCII-safe pattern runs client-side |
| `.a \| has` / `has("a")` | ✓ | `{"a": {"$exists": true}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"a": {"$exists": false}}` | key absence, exact |
| `.a \| length == n` | ✓ | `{"$or": [{"a": {"$size": n}}, {"a": {"$type": …}}, …]}` | jq `length` is polymorphic (array/string/object/number), so the array `$size` is widened with per-type `$type` clauses to a superset; `n == 0` also matches null and a missing field |
| `E1 and E2` | ✓ | `{"$and": […]}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"$or": […]}` | pushed only when **every** branch is pushable |
| `!=`, `any`, nested-array tests, a case-insensitive or non-byte-safe regex | — | — | run client-side: a plain `_all_docs` scan is used, because Mango's semantics for these could wrongly exclude a document jq would keep |

**Byte-safe regex.** CouchDB's Mango `$regex` runs its Erlang engine over the document's raw UTF-8
bytes with no unicode option, and skips a non-string field (an `is_binary` guard, exactly as jq's
`test` over a non-string is false). A pattern is pushed only when it means the same byte-for-byte as
gojq's RE2: pure-ASCII literals, anchors, quantifiers, groups, positive classes, and the `\d \w \s`
shorthands. An unescaped `.`, a negated class (`[^…]`, `\D`, `\W`, `\S`), any non-ASCII byte, or the
`i` flag is declined and runs client-side, because over multi-byte text a byte engine and a rune
engine would diverge. The subject string may be any Unicode — only the pattern is constrained.

Pushdown never changes results, only speed: the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter; `--explain` shows the selector, and `--no-compile` streams the
whole database and filters entirely client-side. A pushed `_find` uses whatever Mango index fits
(create one in CouchDB for large databases); without one CouchDB warns and falls back to its built-in
index.

### Raw commands

`iq exec` runs a raw [Mango `_find`](https://docs.couchdb.org/en/stable/api/database/find.html): the
argument is a JSON `_find` request (`{"selector":{…},"limit":…}`) or a bare selector (wrapped as
`{"selector":…}`), and it prints the matching documents with the paging bookmark:

```bash
iq --src books exec '{"selector": {"year": {"$gt": 2015}}, "limit": 10}'
iq --src books exec '{"author": "Martin Kleppmann"}'   # bare selector
```

`iq inspect` reads server and database metadata — `server` (version and vendor), `databases` (the
server's databases), `dbinfo` (the selected database's document count, sizes, and update sequence),
and `indexes` (its Mango indexes); `--only` narrows to those subcommands.

## Couchbase

*Collection keyspace, document-ID mapping, SQL++ pushdown, and raw SQL++*

Register a `couchbase://` source and the same jq interface works against a collection, where **the
collection is the keyspace: a document's ID is the key and the JSON document is the value**. A
Couchbase cluster nests bucket → scope → collection: the host is the cluster, the bucket rides in the
URL's `?bucket=` (required for keyspace work), and the collection is `?collection=` accepting
`orders` or `sales.orders` (scope defaults to `_default`), overridable per run with a dotted
`handle.[scope.]collection`. Switching buckets is a different source. Use `couchbases://` for TLS.
**Credentials travel in the URL userinfo** (SDK `PasswordAuthenticator`), so `--store keyring` moves
the password to the OS keyring exactly as for the other backends. The SDK's application telemetry is
disabled explicitly, so the tool reports nothing back to the cluster.

```bash
iq add -n books 'couchbase://Administrator:password@localhost/?bucket=iq'  # register once, then:
iq --src books '.["2"]'                                # fetch the document whose ID is 2
iq --src books '.[] | select(.year > 2015) | .title'  # streamed
iq --src books --unbounded 'keys'                      # every document ID
iq --src books.archive '.[]'                           # a different collection in the same bucket
```

Couchbase documents are JSON, so values need no type coercion; integers keep exact precision (large
ones never collapse to a float). The document ID is KV metadata, not part of the value, so it is
never injected into the document. A non-JSON (binary) document is surfaced as a string on a `.["k"]`
lookup and skipped by a scan (the query service returns only JSON). A bounded `.["k"]` lookup is a KV
get; a scan is a **SQL++ keyset walk ordered by `META().id`** (never OFFSET/LIMIT paging), so it
streams with bounded memory. Scans read at `RequestPlus` consistency, so the tool sees its own
just-written documents (read-your-writes).

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality**, **range**, and **existence** clauses are
translated into a SQL++ `WHERE` (with named parameters) so the query service filters before documents
reach iq:

| `select(...)` clause | Pushed | SQL++ predicate | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `` `a` = $p `` | equality; a `null` literal widens to `` (`a` IS NULL OR `a` IS MISSING) `` |
| `.a > x` / `.a <= x` | ✓ | `` (`a` > $p OR ISSTRING(`a`) OR …) `` | range, widened with `ISTYPE()` clauses so jq's cross-type ordering (null < bool < number < string < array < object) is reproduced — SQL++ comparison operators are type-restricted, so higher/lower-ranked types are re-included explicitly |
| `.a \| has` / `has("a")` | ✓ | `` `a` IS NOT MISSING `` | key presence, exact |
| `has("a") \| not` | ✓ | `` `a` IS MISSING `` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable; an all-equality OR over one field collapses to `` `a` IN $p `` |
| `!=`, regex, `length`, `any`, nested-array tests | — | — | run client-side: a plain keyset scan is used, because SQL++ semantics for these could wrongly exclude a document jq would keep |

Every value rides as a named parameter, never concatenated; keyspace and field identifiers are
validated and backtick-quoted, so nothing user-supplied is ever interpolated raw. Pushdown never
changes results, only speed: the full jq always re-runs client-side, so a pushed filter is a
conservative pre-filter; `--explain` shows the `WHERE`, and `--no-compile` streams the whole
collection and filters entirely client-side.

Whatever the `WHERE` leaves behind, a **client-side raw-byte prefilter** runs the full predicate over
each row's raw value before it is decoded, and drops any row it can prove the predicate rejects. So a
fallback scan (a `!=`, a regex) or a partially-pushed scan (a dropped conjunct) skips the dominant
`UseNumber` decode of the documents the query service could not exclude — the same trick as the Redis
and Elasticsearch prefilters, on the bytes the keyset scan already returned. It is byte-level and
never changes results (the full jq still re-runs client-side), so it is bypassed in the one case where
it would be wasted: when the `WHERE` already captured the predicate exactly (the query service
returned only matches). A Couchbase document's ID is KV metadata, never injected into the value, so —
unlike the Elasticsearch prefilter — there is no injected-field case to disable it.

**Index requirement.** A SQL++ scan needs an index on the collection. On Server 7.6+ a sequential
scan answers index-free queries automatically; on 7.0–7.5, or for large collections, create one:
`CREATE PRIMARY INDEX ON \`bucket\`.\`scope\`.\`collection\``. A "no index available" error (code
4000) is surfaced with exactly that hint.

### Raw commands

`iq exec` runs a raw [SQL++](https://docs.couchbase.com/server/current/n1ql/n1ql-language-reference/index.html)
statement: the first argument is the statement and an optional second argument is a JSON object of
named parameters (bound end-to-end, never string-built):

```bash
iq --src books exec 'SELECT META(t).id, t.* FROM `iq` t WHERE t.year > $min' '{"min": 2015}'
iq --src books exec 'SELECT COUNT(*) AS n FROM `iq`'
```

`iq inspect` reads cluster and bucket metadata — `cluster` (nodes and services), `buckets` (the
cluster's buckets), `collections` (the selected bucket's scopes and collections), and `indexes` (the
query indexes); `--only` narrows to those subcommands.

## Neo4j

*Node-label and relationship-type keyspaces, key mapping, Cypher pushdown, and raw Cypher*

Register a `neo4j://` source and the same jq interface works against a node label, where **the node
label is the keyspace: a node's key is the key and the node is the value**. Neo4j has no single
keyspace, so a label is the addressable collection (like a Mongo collection or a Cassandra table):
the host is the bolt server, the label rides in the URL's `?label=` (overridable per run with a
dotted `handle.label`), and the database — Neo4j is multi-database — is `?database=` (default
`neo4j`). Use `neo4j+s://` (or `bolt://` for a single instance, `+s`/`+ssc` for TLS). **Credentials
travel in the URL userinfo** (bolt basic auth), so `--store keyring` moves the password to the OS
keyring exactly as for the other backends.

**The key is the elementId by default, or a property you name with `?key=`.** `elementId(n)` is
always present and unique but opaque and not stable across database reloads, so a `?key=` property
(a stable, human-meaningful id) reads better; the value carries `_id` (the elementId) and `_labels`
alongside the node's properties, so identity survives whichever key you choose.

```bash
iq add -n graph 'neo4j://neo4j:password@localhost:7687/?label=Person&key=id'  # register once, then:
iq --src graph '.["1"]'                                # fetch the Person whose id is 1
iq --src graph '.[] | select(.age > 40) | .name'       # streamed
iq --src graph --unbounded 'keys'                       # every key in the label
iq --src graph.Book '.[]'                               # query a different label on the same database
```

Neo4j values map to JSON directly: integers keep exact precision, bytes become base64, and temporal
and spatial values become their canonical ISO strings and `{x,y,srid}` objects. A scan pages the
label with keyset pagination ordered by `elementId(n)`. Because a `?key=` property is not guaranteed unique
(unlike a primary key), a scan falls back to a node's elementId whenever the key would collide
within a page, so no node is ever silently dropped; a bounded `.["v"]` lookup that matches more than
one node is an error rather than an arbitrary pick.

### Relationship collections

A **relationship type** is an addressable collection too, so you can query a graph's edges the same
way. Name it with `?rel=KNOWS` on the source, or address one per run with the `:` marker
(`handle.:KNOWS`) — a leading colon can never be a valid label, so it unambiguously selects a
relationship type. A source names either a label or a relationship type, not both.

```bash
iq add -n edges 'neo4j://neo4j:password@localhost:7687/?rel=WROTE'  # a relationship-type source
iq --src edges '.[] | select(.year > 2015)'   # stream WROTE edges, filtered (pushdown on the edge)
iq --src graph.:WROTE '.[]'                    # or address the type per run from any source
```

Each relationship's value is its properties plus a self-describing envelope: `_type` (the type),
`_id` (its elementId), and `_start` / `_end` (the endpoint node elementIds). The scan, key, count,
and `select(...)` pushdown rules are identical to nodes (the predicate is pushed onto the edge
variable). **Relationship collections are read-only for now**: creating an edge needs endpoint
resolution — which nodes to connect and by which key — which is a further follow-up, so a copy or
`iq data` write into a relationship source is refused with a clear message. Write nodes with
`?label=`.

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
a Cypher `WHERE` clause (using dynamic `n[$prop]` access, so the property name is a parameter, never
string-built) so the server filters before nodes reach iq:

| `select(...)` clause | Pushed | Cypher | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `n[$p] = $v` | equality; a `null` literal becomes `n[$p] IS NULL` (a missing property) |
| `.a \| has` / `has("a")` | ✓ | `n[$p] IS NOT NULL` | key presence, exact |
| `has("a") \| not` | ✓ | `n[$p] IS NULL` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable |
| `.a > x` / `.a <= x`, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side: Cypher compares mismatched types as null rather than by jq's cross-type ordering, and a nested path has no flat Neo4j property, so pushing these could wrongly exclude a node jq would keep |

Pushdown never changes results, only speed: the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter; `--explain` shows the `WHERE` clause, and `--no-compile`
streams the whole label and filters entirely client-side.

### Writing

A copy into a Neo4j label upserts each node with `MERGE (n:Label {key}) SET n += props`, so a re-run
converges. **Writing needs a `?key=` property** (a MERGE key must be stable, and the elementId is
server-assigned) **and a uniqueness constraint on it** (`CREATE CONSTRAINT ... REQUIRE n.<key> IS
UNIQUE`) — without the constraint a MERGE could match and overwrite several nodes at once, so the
write is refused up front rather than fanning out. `iq data clear` detach-deletes every node in the
label (and the relationships they hold); a label is not a droppable container, so `iq data drop` is
unsupported. Writes set node properties only — relationships are a follow-up.

### Raw commands

`iq exec` runs raw, parameterized [Cypher](https://neo4j.com/docs/cypher-manual/current/): the first
argument is the statement and an optional second argument is a JSON object of parameters (passed as
parameters, never string-built into the statement). It prints the result rows as JSON:

```bash
iq --src graph exec 'MATCH (n:Person) WHERE n.age > $min RETURN n.name, n.age' '{"min": 40}'
iq --src graph exec 'MATCH (n) RETURN count(n) AS nodes'
```

`iq inspect` reads deployment and schema metadata — `server` (components and version), `databases`
(the deployment's databases), `labels` (the addressable node labels), `reltypes` (relationship
types), and `constraints` (which shows the uniqueness constraint a `?key=` write needs); `--only`
narrows to those subcommands.

## Elasticsearch & OpenSearch

*Index keyspace, _id mapping, Query-DSL pushdown, and raw _search*

Register an `elasticsearch://` (or `opensearch://`) source and the same jq interface works against an
index, where **the
index is the keyspace: a document's `_id` is the key and its `_source` is the value**. The host is
the server; the index rides in the URL's `?index=` (overridable per run with a dotted
`handle.index`, since one server hosts many indices). Use `elasticsearch+s://` / `opensearch+s://` for
TLS. **Credentials,
when the cluster needs them, travel in the URL userinfo** (HTTP basic auth), so `--store keyring`
moves the password to the OS keyring exactly as for the other backends. **OpenSearch is the same
driver** behind the scheme — everything below applies to both; the only differences are internal (its
point-in-time endpoint and, since it predates Elasticsearch's `_shard_doc`, an `_id` keyset sort):

```bash
iq add -n books 'elasticsearch://localhost:9200/?index=books'  # register once, then:
iq --src books '.["2"]'                                # fetch the document whose _id is 2
iq --src books '.[] | select(.year > 2015) | .title'  # streamed
iq --src books --unbounded 'keys'                      # every _id
iq --src books.authors '.[]'                           # query a different index on the same server
iq add -n logs 'opensearch://localhost:9201/?index=books'      # an OpenSearch source, identical surface
```

Elasticsearch documents are JSON, so values need no type coercion; integers keep exact precision
(large ones never collapse to a float). Each document's `_id` (Elasticsearch metadata, stored
outside `_source`) is injected into the value as `_id`, so a plain `.[]` stream is self-describing
and restorable — like a Mongo document, `--typed` is not needed for a lossless backup. The
`--unbounded` / streaming rules are identical to every backend; a scan pages the index with a
point-in-time and `search_after` (keyset pagination, sorted by `_shard_doc`), so it never re-reads
from an offset.

### Server-side pre-filtering (predicate pushdown)

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
an Elasticsearch `bool` query so the cluster filters before documents reach iq. The index mapping is
read once at connect, so an equality is pushed only onto a field whose type matches it exactly —
never onto analyzed `text`, where a term could wrongly exclude a match:

| `select(...)` clause | Pushed | Elasticsearch query | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"term": {"a": x}}` | a single top-level field mapped `keyword`/numeric/`boolean`/`ip` (or a `text` field's `.keyword` sub-field); the literal's type must match the field |
| `.a \| has` / `has("a")` | ✓ | `{"exists": {"field": "a"}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"bool": {"must_not": {"exists": …}}}` | key absence, exact |
| `E1 and E2` | ✓ | `{"bool": {"must": […]}}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"bool": {"should": […], "minimum_should_match": 1}}` | pushed only when **every** branch is pushable |
| `.a == null`, ranges, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side: a range excludes a missing field and orders types unlike jq's cross-type ordering, `== null` matches absent-or-null (no single term does), and an analyzed-text or unmapped field has no exact term |

Pushdown never changes results, only speed: the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter; `--explain` shows the query, and `--no-compile` streams the
whole index and filters entirely client-side.

Whatever the `bool` query leaves behind, a **client-side raw-byte prefilter** runs the full predicate
over each hit's raw `_source` before it is decoded, and drops any hit it can prove the predicate
rejects. So a fallback scan (a range, an equality on an unmapped or analyzed field) or a
partially-pushed scan skips the dominant `UseNumber` decode of the documents the cluster could not
exclude — the same trick as the Redis prefilter, on the bytes `_search` already returned. It is
byte-level and never changes results (the full jq still re-runs client-side), so it is bypassed in two
cases where it would be wasted or wrong: when the `term` query already captured the predicate exactly
(the cluster returned only matches), and when the predicate references the injected `_id` field, which
the raw `_source` does not carry.

### Raw commands

`iq exec` runs a raw [`_search`](https://www.elastic.co/guide/en/elasticsearch/reference/current/search-search.html):
the argument is a JSON search body (`{"query":{…},"size":…,"aggs":…}`) or a bare query object
(`{"match":{"title":"dune"}}`, wrapped as `{"query":…}`), and it prints the whole reply — hits,
aggregations, and all — as JSON:

```bash
iq --src books exec '{"query": {"range": {"year": {"gt": 2015}}}}'
iq --src books exec '{"match": {"author": "Kleppmann"}}'   # bare query, wrapped
```

`iq inspect` reads server and index metadata — `server` (node, cluster, and version), `indices` (the
server's indices), `mapping` (the selected index's field mapping, which shows what a term pushdown
can use), and `aliases` (the server's aliases); `--only` narrows to those subcommands.

> **OpenSearch** is a supported, integration-tested target on the same driver: register an
> `opensearch://` source and everything above works identically. It uses the
> [opensearch-go](https://github.com/opensearch-project/opensearch-go) client (Elasticsearch's own
> client refuses to talk to non-Elasticsearch servers), OpenSearch's `_search/point_in_time` endpoint,
> and — since OpenSearch forked before Elasticsearch's `_shard_doc` sort — an `_id` keyset sort for
> scans. All of that is internal; the jq surface, pushdown, writes, `exec`, and `inspect` are the same.

## File dumps

*Query a snapshot offline (read-only)*

A `file://` source reads a database dump straight from disk, so a snapshot is queried, inspected
for shape, diffed, and restored with the same jq interface — **no running server**. It is
read-only: a `file://` endpoint is never a copy *destination*, and `iq exec`/`iq inspect` (which
need a live server) do not apply.

```bash
iq add file:///backups/prod.rdb -n snap      # register a dump like any source
iq --src snap '.["session:42"]'              # bounded read of one key
iq --src snap '.[] | select(.active)'        # streamed scan
iq --src snap 'keys' --unbounded             # whole-dataset filters obey --unbounded
iq --src snap --insert prod                  # restore the dump into a live source
iq diff snap prod --data                     # diff a dump against a live source
```

The format is detected from the file's content (or forced with a `?format=` query, e.g.
`file:///d.bin?format=bson`). A gzipped dump is unwrapped transparently; a gzipped dump must
pass `?format=` since its content is not sniffable through the compression.

| Format | Produced by | Notes |
| --- | --- | --- |
| Typed JSONL | `iq --src <s> --typed -o <file>` | iq's own dump; lossless round-trip |
| Redis RDB | `redis-cli --rdb`, `SAVE` | values match a live scan; RDB ≤ v12 (Redis ≤ 7.2) |
| Mongo BSON | `mongodump` | single `.bson` file |
| Mongo Extended JSON | `mongoexport` | one document per line, or a `--jsonArray` array |
| DynamoDB JSON | S3 `export-table-to-point-in-time`, `aws dynamodb scan` | needs `?format=dynamodb-json` and a `?keys=pk[:S][,sk[:N]]` key schema (a dump carries items but not the table's key schema); export files are gzipped NDJSON |
| Cassandra CSV | `cqlsh COPY … TO 'f.csv'` | needs `?format=cassandra-csv`, `?keys=col1[,col2]` naming the primary-key columns, and `?types=col=cqltype,…` for the non-text columns (COPY writes every value as text); column names come from a `WITH HEADER=TRUE` row, else `?columns=`; scalar columns only |
| Neo4j APOC JSON | `CALL apoc.export.json.all('g.json',{})` | needs `?format=neo4j` and a keyspace selector — `?label=<Label>` for its nodes or `?rel=<Type>` for its relationships (a dump holds the whole graph); JSON Lines or `ARRAY_JSON`; same `_id`/`_labels`/`_type`/`_start`/`_end` envelope as the live driver, `?key=<prop>` to key by a property; `_id` is APOC's numeric export id, not the live elementId; the binary `neo4j-admin database dump` and APOC's `useTypes`/`JSON_ID_AS_KEYS` variants are not supported |

The whole dump streams; a `file://` source never holds all values in memory (whole-dataset
materialization is the core's, gated by `--unbounded`, exactly as for a live backend). **Restore
fidelity** is the record round-trip: values and native types reconstruct, but TTLs, exact
encodings, stream consumer groups, RDB module types, and Mongo indexes do not carry.

**Neo4j record ids.** A dump's `_id` — and a relationship's `_start`/`_end` — is APOC's numeric
export id, not the live driver's `elementId`, because APOC's default export does not write
elementIds. Within one dump the ids are self-consistent: a relationship's `_start`/`_end` reference
the same ids its nodes carry as `_id`, so `?rel=` endpoints resolve against the default-keyed
`?label=` nodes exactly as they do live. Two things the numeric id cannot do, both by nature — it
does not match the `_id` of the same node read from the live source (different id schemes), and it
is not stable across re-exports (Neo4j reuses a deleted node's id, the reason `id()` is deprecated
in favor of `elementId()`). For an identifier that is stable and identical across a live source and
its dump, key on a business property with `?key=<prop>`: it reads the same value everywhere.

**Decode cache.** Re-querying the same large dump re-parses it every time, so iq caches the
decoded, normalized records of a scanned dump above 4 MiB and reads them back on later queries,
skipping the RDB/BSON/JSON decode (a warm scan of an 8 MiB dump runs several times faster). The
cache lives under `<user cache dir>/iq/dumps`, keys on the dump's path, size, and mtime — so
editing the dump invalidates it automatically — and is transparent: a stale or absent cache just
means a full decode, never a wrong or failed query. Only full scans populate it (a bounded
key read does not), and stdin is never cached.

Alongside the records, a scan writes a **per-page key index** (a Bloom filter per page), so a
later bounded read (`iq --src snap '.["id"]'`) decodes only the pages that may hold a wanted key
instead of streaming the whole cache — a point lookup or a missing-key check stays fast even on a
huge dump. The index is on by default and distribution-agnostic (it hashes keys, so random
ids/UUIDs are fine). Skip it with `--no-cache-index` (the flat cache is still written; a bounded
read just streams it) when a very large keyspace makes the index build memory unwelcome.

Manage the cache with `iq cache`, bypass it for one run with `--no-cache`, or set a default with
`iq config set no-cache true` / `iq config set no-cache-index true`.

- `iq cache location` — print the cache directory path.
- `iq cache stat [-j/--json | -y/--yaml]` — list cached dumps with their sizes.
- `iq cache clear [<source>|<path>]` — remove all cached dumps, or just one source's/path's.

**Prefilter.** A file source pushes no filter to a server (there is none), but on a streaming
scan of an **uncached typed-JSONL** dump a compiled predicate drives a **client-side raw-byte
prefilter**: each record's raw `value` bytes are tested against the predicate and a provable
non-match is dropped before it is decoded, so the dominant JSON decode is skipped for records the
filter would reject. Every other format (YAML, RDB, BSON, Extended JSON, DynamoDB JSON, Cassandra
CSV, Neo4j APOC), and any scan served from a fresh decode cache (whose bytes are already-decoded
CBOR, and already fast to stream), decodes in full and lets the client filter. The full jq always
re-runs client-side, so output is identical with or without the prefilter — it only skips decoding
dropped records; a prefiltered scan deliberately does not populate the decode cache (that would
require decoding everything). `--no-compile` turns it off.
