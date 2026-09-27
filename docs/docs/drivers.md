---
icon: material/engine-outline
---

# Drivers

`iq` picks the backend from a source's URI scheme. The query core is
driver-agnostic, so further backends slot in behind the same port.

Each driver below documents these topics:

- Keyspace mapping
- Value encoding
- Predicate pushdown
- Raw commands.

## Driver list `driver ls`

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| `-j` | `--json` | ✗ | emit machine-readable JSON |
| `-y` | `--yaml` | ✗ | emit machine-readable YAML |
| `-v` | `--verbose` | ✗ | list the readable file dump formats as well |

```sh { title='List registered backends' }
iq driver ls
```

```sh { title='List registered backends with all the readable file dump formats' }
iq driver ls -v
```

!!! note "Supported versions"

    The `VERSIONS` column lists the range of backend server versions the
    bundled client library supports.

### Guarantees

The per-driver blocks below differ in encoding and pushdown detail, but every backend honors the
same contract:

- **One URI, native nouns.** The URI scheme picks the driver. The keyspace rides in the URI as the
  backend's own noun (`?collection=`, `?table=`, `?database=`, `?label=`/`?rel=`, `?index=`). A
  query overrides it per run with the dotted `handle.<keyspace>` suffix (see [Sources](sources.md)).
- **One jq interface.** A bounded filter fetches exactly the named keys. A missing key reads as
  `null`, never an error. A `.[]`-rooted filter streams the keyspace in bounded pages. A filter
  that collapses the keyspace into one value materializes only behind `--unbounded` (see
  [Read strategies](how-it-works.md#read-strategies)).
- **Pushdown never changes results.** A pushed predicate is only ever a conservative pre-filter.
  Where the backend can filter, the pre-filter is server-side. Where the backend cannot, the
  pre-filter is a client-side raw-byte prefilter that drops a provable non-match before decode
  (Redis, on RedisJSON values, Elasticsearch/OpenSearch and Couchbase, over the residual their
  server-side query cannot narrow). The full jq always re-runs client-side. As a result, output is
  identical with or without the pushed predicate, and [`--explain`](query-plan.md) shows exactly what was pushed.
- **Capabilities are explicit.** Filtered scans, count estimates, writes, clear, drop and per-key
  delete are opt-in ports. A backend implements what its model supports. A command against a
  missing capability fails with a clear message instead of emulating it. For example, Redis, whose
  DB index cannot be removed, has no `drop`. The read-only file dump has no per-key `delete`.
- **Values round-trip.** Every value normalizes to JSON under a frozen per-backend encoding
  contract. A `--typed` dump restores through `--insert` losslessly (see
  [Write data](write-data.md)).
- **Bounded and redacted.** `--timeout` bounds every backend call. `iq` redacts a URI's password
  from every listing, log line and error.
- **Native commands.** `iq exec` speaks the backend's own language, verbatim where one exists
  (Redis commands, Mongo command documents, CQL, PartiQL, Cypher, Mango, the Elasticsearch DSL).
  Where none exists, `iq exec` speaks a small fixed verb set (HBase). See each driver's Raw
  commands section. Every `iq` flag must come before `exec`. `iq` forwards everything
  after `exec` to the backend untouched.

### Capabilities

Which opt-in ports each driver implements. A `—` is not a gap in the docs. For that port, the
command fails with a clear message rather than emulating what the backend cannot do.

| Driver | `data clear` | `data drop` | `data delete` | count estimate |
| --- | :---: | :---: | :---: | :---: |
| Cassandra | ✓ | ✓ | ✓ | — |
| Couchbase | ✓ | ✓ | — | — |
| CouchDB | ✓ | ✓ | ✓ | ✓ |
| DynamoDB | ✓ | ✓ | ✓ | ✓ |
| Elasticsearch / OpenSearch | ✓ | ✓ | ✓ | ✓ |
| HBase | ✓ | ✓ | ✓ | — |
| MongoDB | ✓ | ✓ | ✓ | ✓ |
| Neo4j | ✓ | — | ✓ | ✓ |
| Redis | ✓ | — | ✓ | ✓ |
| File dumps | — | — | — | — |

`data clear` empties a keyspace and keeps it. `data drop` removes the keyspace itself.
`data delete` removes named keys (see [Write data](write-data.md#delete-data-delete)).

The count estimate is a cheap metadata total, never a second scan. As a result, an unfiltered
scan can show its progress against a rough total. The estimate is a hint that can drift as the
keyspace changes. `iq` reads it only for an unfiltered scan, never for a pushed-down filtered one.


## Cassandra

[Apache Cassandra](https://cassandra.apache.org) is a distributed wide-column
store built for high write throughput across many nodes.

After you register a `cassandra://` source, the same jq interface works against a table. **The table is the keyspace, a row's primary key is the key and the row is the value**.

The keyspace comes from the URI path. The table comes from the URI's `?table=` (overridable per
run with a dotted `handle.table`). Multiple contact points are comma-separated. `?consistency=`
sets the read/write consistency level (default `QUORUM`).

```sh { title='Register a Cassandra source' }
iq add -n books 'cassandra://localhost:9042/iq?table=books'
```
```sh { title='Fetch the row whose primary key is 2' }
iq --src books '.["2"]'
```
```sh { title='Stream the table, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every primary key' }
iq --src books --unbounded 'keys'
```
```sh { title='Register with auth and multiple contact points' }
iq add -n cl 'cassandra://user:pass@n1,n2:9042/app?table=orders'
```

Cassandra columns are natively typed, so `.year > 2015` needs no `tonumber`. The
`--unbounded` / streaming rules are identical to every backend. The driver reads the table's schema once
at connect time, so it knows the primary-key columns and their types.

### Key encoding

A row's key is its **full primary key**, the partition-key columns followed by the clustering
columns. A single-column primary key renders as its bare value (`42`, a uuid, a text value, like a
Mongo `_id`). A composite primary key renders as a compact JSON array in schema order:

```sh { title='Fetch by a single-column key, bare' }
iq --src sales '.["US"]'
```
```sh { title='Fetch by a composite key ((country), id), a JSON array' }
iq --src sales '.["[\"US\",1]"]'
```

The array elements are the columns' string forms. On lookup, the driver coerces them back through
the schema, so a `bigint`/`varint` key round-trips without precision loss.

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

### Pushdown

By default, the driver translates the **equality** clauses of a `.[] | select(...)` filter into a
CQL `WHERE`. As a result, the cluster filters before rows reach iq. Only equality and same-column membership are pushed:

| `select(...)` clause | Pushed | CQL translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `a = ?` | a single top-level column that exists in the table |
| `.a == 1 or .a == 2` | ✓ | `a IN (?, ?)` | an `or` of equalities on one column |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| ranges, regex, `has`, `length`, negations, nested paths | — | — | run client-side. Ranges are skipped because jq treats a missing field as the lowest value, which CQL cannot reproduce |

A pushed `WHERE` that does not resolve to the full partition key runs with `ALLOW FILTERING`. As a
result, the coordinator does the scan, an opt-in cost (it is shown in `--explain`). Pushdown never
changes results, only speed. The full jq always re-runs client-side, so a pushed filter is a
conservative pre-filter. Pass `--no-compile` to stream the whole table and filter entirely client-side.

### Raw commands

`iq exec` runs a CQL statement verbatim and prints the rows as JSON. It is the raw path for
server-side queries, DDL and administration that the jq read path does not cover:

```sh { title='Read the cluster version' }
iq --src books exec 'SELECT release_version FROM system.local'
```
```sh { title='Run a filtered CQL query' }
iq --src books exec "SELECT title FROM books WHERE year > 2015 ALLOW FILTERING"
```

`iq inspect` reads the system schema through these subcommands: `local` (cluster/version),
`tables` (the keyspace's tables), and `columns` (a table's columns). `--only` narrows to those
subcommands.

## Couchbase

[Couchbase](https://www.couchbase.com) is a distributed document database
combining a key-value engine with SQL++ queries.

After you register a `couchbase://` source, the same jq interface works against a
collection. **The collection is the keyspace, a document's ID is the key and the JSON document is the value**.

A Couchbase cluster nests bucket → scope → collection. The host is the cluster.
The bucket rides in the URI's `?bucket=` (required for keyspace work). The
collection is `?collection=`, which accepts `orders` or `sales.orders` (scope
defaults to `_default`). You can override it per run with a dotted
`handle.[scope.]collection`.

Switching buckets is a different source. Use `couchbases://` for TLS.

**Credentials travel in the URI userinfo** (SDK `PasswordAuthenticator`), so
`--store keyring` moves the password to the OS keyring exactly as for the other
backends.

The driver disables the SDK's application telemetry explicitly, so the tool
reports nothing back to the cluster.

```sh { title='Register a Couchbase source' }
iq add -n books 'couchbase://Administrator:password@localhost/?bucket=iq'
```
```sh { title='Fetch the document whose ID is "2"' }
iq --src books '.["2"]'
```
```sh { title='Stream the collection, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every document ID' }
iq --src books --unbounded 'keys'
```
```sh { title='Query a different collection in the same bucket' }
iq --src books.archive '.[]'
```

Couchbase documents are JSON, so values need no type coercion. Integers keep
exact precision (large ones never collapse to a float).

Couchbase has no per-key `iq data delete` yet (`clear` and `drop` work). Per-key
delete is a v1 follow-up.

The document ID is KV metadata, not part of the value, so it is never injected
into the document.

A non-JSON (binary) document is returned as a string on a `.["k"]` lookup and
skipped by a scan (the query service returns only JSON).

A bounded `.["k"]` lookup is a KV get. A scan is a **SQL++ keyset walk ordered
by `META().id`** (never OFFSET/LIMIT paging), so it streams with bounded
memory.

Scans read at `RequestPlus` consistency, so the tool sees its own just-written
documents (read-your-writes).

### Pushdown

By default, the driver translates the **equality**, **range** and **existence** clauses of a
`.[] | select(...)` filter into a SQL++ `WHERE` (with named parameters). As a result, the query
service filters before documents reach iq:

| `select(...)` clause | Pushed | SQL++ predicate | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `` `a` = $p `` | equality. A `null` literal widens to `` (`a` IS NULL OR `a` IS MISSING) `` |
| `.a > x` / `.a <= x` | ✓ | `` (`a` > $p OR ISSTRING(`a`) OR …) `` | range. `ISTYPE()` clauses widen it so that jq's cross-type ordering (null < bool < number < string < array < object) is reproduced. SQL++ comparison operators are type-restricted, so higher/lower-ranked types are re-included explicitly |
| `.a \| has` / `has("a")` | ✓ | `` `a` IS NOT MISSING `` | key presence, exact |
| `has("a") \| not` | ✓ | `` `a` IS MISSING `` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable. An all-equality OR over one field collapses to `` `a` IN $p `` |
| `!=`, regex, `length`, `any`, nested-array tests | — | — | run client-side. The driver uses a plain keyset scan, because SQL++ semantics for these can wrongly exclude a document that jq keeps |

Every value rides as a named parameter, never concatenated. The driver validates
and backtick-quotes keyspace and field identifiers. As a result, nothing
user-supplied is ever interpolated raw.

Pushdown never changes results, only speed. The full jq always re-runs
client-side, so a pushed filter is a conservative pre-filter. `--explain` shows
the `WHERE`. `--no-compile` streams the whole collection and filters entirely
client-side.

Whatever the `WHERE` leaves behind, a **client-side raw-byte prefilter** runs
the full predicate over each row's raw value before it is decoded. The
prefilter drops any row that it can prove the predicate rejects.

So a fallback scan (a `!=`, a regex) or a partially-pushed scan (a dropped
conjunct) skips the dominant `UseNumber` decode of the documents that the query
service cannot exclude. The prefilter works on the bytes that the keyset scan
already returned. This is the same trick as the Redis and Elasticsearch
prefilters.

The prefilter is byte-level and never changes results (the full jq still
re-runs client-side). As a result, it is bypassed in the one case where it
gives no benefit. That case is when the `WHERE` already captured the predicate
exactly (the query service returned only matches).

A Couchbase document's ID is KV metadata, never injected into the value, so,
unlike the Elasticsearch prefilter, there is no injected-field case to disable
it.

**Index requirement.** A SQL++ scan needs an index on the collection.
On Server 7.6+, a sequential scan answers index-free queries automatically. On
7.0–7.5, or for large collections, create one:
`CREATE PRIMARY INDEX ON \`bucket\`.\`scope\`.\`collection\``. `iq` reports a "no index
available" error (code 4000) with exactly that hint.

### Raw commands

`iq exec` runs a raw [SQL++](https://docs.couchbase.com/server/current/n1ql/n1ql-language-reference/index.html)
statement. The first argument is the statement. An optional second argument is a JSON object of
named parameters (bound end-to-end, never string-built):

```sh { title='Run a parameterized SQL++ query' }
iq --src books exec 'SELECT META(t).id, t.* FROM `iq` t WHERE t.year > $min' '{"min": 2015}'
```
```sh { title='Count the documents' }
iq --src books exec 'SELECT COUNT(*) AS n FROM `iq`'
```

`iq inspect` reads cluster and bucket metadata through these subcommands:

- `cluster` (nodes and services)
- `buckets` (the cluster's buckets)
- `collections` (the selected bucket's scopes and collections)
- `indexes` (the query indexes).

`--only` narrows to those subcommands.

## CouchDB

[Apache CouchDB](https://couchdb.apache.org) is a document database that
speaks HTTP and JSON, built around multi-master replication.

After you register a `couchdb://` source, the same jq interface works against a
database. **The database is the keyspace, a document's `_id` is the key and the document is the value**.

The host is the CouchDB server. The database rides in the URI's `?database=`
(overridable per run with a dotted `handle.database`, because one server hosts
many databases).

Use `couchdbs://` for TLS. **Credentials travel in the URI userinfo** (HTTP
basic auth), so `--store keyring` moves the password to the OS keyring exactly
as for the other backends.

```sh { title='Register a CouchDB source' }
iq add -n books 'couchdb://admin:password@localhost:5984/?database=iq'
```
```sh { title='Fetch the document whose _id is "2"' }
iq --src books '.["2"]'
```
```sh { title='Stream the database, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every _id' }
iq --src books --unbounded 'keys'
```
```sh { title='Query a different database on the same server' }
iq --src books.other '.[]'
```

CouchDB documents are JSON, so values need no type coercion. Integers keep
exact precision (large ones never collapse to a float). `_id` and `_rev` are
kept in the document. Design documents (`_design/…`) are database metadata, and
scans skip them. The `--unbounded` / streaming rules are identical to
every backend.

### Pushdown

By default, the driver translates the **equality**, **range**, **existence**, byte-safe
**regex** and **length** clauses of a `.[] | select(...)` filter into a Mango `_find` selector.
As a result, the server filters before documents reach iq:

| `select(...)` clause | Pushed | Mango selector | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"a": x}` | equality. A `null` literal also matches an absent field |
| `.a > x` / `.a <= x` | ✓ | `{"$or": [{"a": {"$gt": x}}, …]}` | range. `$type` clauses widen it so that jq's cross-type ordering (null < bool < number < string < array < object, matching CouchDB collation) is reproduced |
| `.a \| test("re")` | ✓ | `{"a": {"$regex": "re"}}` | byte-safe ASCII patterns only (below). A case-insensitive or non-ASCII-safe pattern runs client-side |
| `.a \| has` / `has("a")` | ✓ | `{"a": {"$exists": true}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"a": {"$exists": false}}` | key absence, exact |
| `.a \| length == n` | ✓ | `{"$or": [{"a": {"$size": n}}, {"a": {"$type": …}}, …]}` | jq `length` is polymorphic (array/string/object/number). As a result, per-type `$type` clauses widen the array `$size` to a superset. `n == 0` also matches null and a missing field |
| `E1 and E2` | ✓ | `{"$and": […]}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"$or": […]}` | pushed only when **every** branch is pushable |
| `!=`, `any`, nested-array tests, a case-insensitive or non-byte-safe regex | — | — | run client-side. The driver uses a plain `_all_docs` scan, because Mango's semantics for these can wrongly exclude a document that jq keeps |

**Byte-safe regex.** CouchDB's Mango `$regex` runs its Erlang engine over the document's raw UTF-8
bytes with no unicode option. It skips a non-string field (an `is_binary` guard, exactly as jq's
`test` over a non-string is false). A pattern is pushed only when it means the same byte-for-byte as
gojq's RE2. The pattern can use only these constructs:

- Pure-ASCII literals
- Anchors
- Quantifiers
- Groups
- Positive classes
- The `\d \w \s` shorthands.

An unescaped `.`, a negated class (`[^…]`, `\D`, `\W`, `\S`), any non-ASCII byte, or the
`i` flag is declined and runs client-side. The reason is that over multi-byte text, a byte engine
and a rune engine diverge. The subject string can be any Unicode. Only the pattern is constrained.

Pushdown never changes results, only speed. The full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter. `--explain` shows the selector. `--no-compile` streams the
whole database and filters entirely client-side.

A pushed `_find` uses whatever Mango index fits (create one in CouchDB for large databases).
Without one, CouchDB warns and falls back to its built-in index.

### Raw commands

`iq exec` runs a raw [Mango `_find`](https://docs.couchdb.org/en/stable/api/database/find.html). The
argument is a JSON `_find` request (`{"selector":{…},"limit":…}`) or a bare selector (wrapped as
`{"selector":…}`). It prints the matching documents with the paging bookmark:

```sh { title='Run a Mango _find request' }
iq --src books exec '{"selector": {"year": {"$gt": 2015}}, "limit": 10}'
```
```sh { title='Run a bare selector, wrapped automatically' }
iq --src books exec '{"author": "Martin Kleppmann"}'
```

`iq inspect` reads server and database metadata through these subcommands:

- `server` (version and vendor)
- `databases` (the server's databases)
- `dbinfo` (the selected database's document count, sizes and update sequence)
- `indexes` (its Mango indexes).

`--only` narrows to those subcommands.

## DynamoDB

[Amazon DynamoDB](https://aws.amazon.com/dynamodb/) is AWS's managed
serverless key-value and document database.

After you register a `dynamodb://` source, the same jq interface works against a table. **The table is the keyspace, an item's primary key is the key and the item is the value**.

The region is the URI host. The table rides in the URI's `?table=` (overridable per run with a
dotted `handle.table`). An optional `?endpoint=` points at DynamoDB Local.

**Credentials never travel in the URI.** The AWS default credential chain (environment, `~/.aws`,
IAM role) resolves them, so no secret touches the config or keyring.

```sh { title='Register a DynamoDB source, credentials from the AWS chain' }
iq add -n books 'dynamodb://us-east-1/?table=books'
```
```sh { title='Fetch the item whose partition key is 2' }
iq --src books '.["2"]'
```
```sh { title='Stream the table, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every primary key' }
iq --src books --unbounded 'keys'
```
```sh { title='Register DynamoDB Local via ?endpoint=, the driver supplies dummy credentials' }
iq add -n local 'dynamodb://us-east-1/?table=books&endpoint=http://localhost:8000'
```

DynamoDB attributes are natively typed, so `.year > 2015` needs no `tonumber`. The `--unbounded` /
streaming rules are identical to every backend. The driver reads the table's key schema once at connect
time, so it knows the partition and (optional) sort key and their types.

### Key encoding

An item's key is its **full primary key**, the partition key, then the sort key when the table has
one. A partition-key-only table renders the key as its bare value (`42`, a string, like a Mongo
`_id`). A table with a sort key renders a compact JSON array in schema order:

```sh { title='Fetch by a partition-key-only key, bare' }
iq --src sales '.["US"]'
```
```sh { title='Fetch by a composite key (partition "US", sort 1), a JSON array' }
iq --src sales '.["[\"US\",1]"]'
```

The array elements are the key attributes' string forms. On lookup, the driver coerces them back
through the key schema, so a numeric (`N`) or binary (`B`) key round-trips faithfully.

### Value encoding

Each attribute value is normalized to JSON by DynamoDB type:

| DynamoDB type | JSON shape |
| --- | --- |
| `S` (string) | the string verbatim |
| `N` (number) | integer as a number (exact string when it overflows int64), decimal as an exact string (or a number under `--format.decimal number`) |
| `BOOL` | `true` / `false` |
| `B` (binary) | base64 string |
| `NULL` | `null` |
| `M` (map) | object |
| `L` (list) | array |
| `SS`, `NS`, `BS` (sets) | array (of strings / numbers / base64 strings) |
| missing attribute | omitted (reads as `null` in jq) |

Note that the set types (`SS`/`NS`/`BS`) normalize to a plain array. As a result, a copy **back**
into DynamoDB writes them as a list (`L`), not a set. A number presented as a string (auto/string
decimal mode) writes back as a string (`S`). For a numeric round-trip, use
`--format.decimal number`.

### Pushdown

By default, the driver translates the **equality** and **existence** clauses of a
`.[] | select(...)` filter into a DynamoDB `Scan` `FilterExpression`. As a result, the service
filters before items reach iq. The driver references every attribute through a `#name`
placeholder, so a reserved word (`name`, `status`, `size`, `year`, …) is always safe:

| `select(...)` clause | Pushed | FilterExpression | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `#a = :v` | a single top-level attribute, string, number, or boolean literal |
| `.a \| has` / `has("a")` | ✓ | `attribute_exists(#a)` | key presence, exact |
| `has("a") \| not` | ✓ | `attribute_not_exists(#a)` | key absence, exact |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `OR` of the parts | pushed only when **every** branch is pushable |
| ranges, regex, `length`, `!=`, nested paths | — | — | run client-side. Ranges are skipped because jq orders a string above every number, which a typed DynamoDB comparison cannot reproduce |

A `Scan` reads the whole table (there is no `WHERE` on a primary-key membership like a relational
store). The `FilterExpression` only avoids shipping non-matching items over the wire. `--explain`
shows the cost. Pushdown never changes results, only speed. The full jq always re-runs
client-side, so a pushed filter is a conservative pre-filter. Pass `--no-compile` to stream the whole
table and filter entirely client-side.

### Raw commands

`iq exec` runs a [PartiQL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.html)
statement verbatim and prints the items as JSON. It is the raw path for server-side queries and
writes that the jq read path does not cover:

```sh { title='Fetch one item by key with PartiQL' }
iq --src books exec 'SELECT * FROM "books" WHERE id = 2'
```
```sh { title='Run a filtered PartiQL query' }
iq --src books exec 'SELECT title FROM "books" WHERE "year" > 2015'
```

`iq inspect` reads table metadata through these subcommands: `tables` (the region's tables) and
`table` (the selected table's key schema, item count, size, billing mode and index names). `--only`
narrows to those subcommands.

## Elasticsearch & OpenSearch

[Elasticsearch](https://www.elastic.co/elasticsearch) and its fork
[OpenSearch](https://opensearch.org) are document search and analytics
engines built on Lucene, queried over HTTP with JSON.

After you register an `elasticsearch://` (or `opensearch://`) source, the same jq interface works against an
index. **The index is the keyspace, a document's `_id` is the key and its `_source` is the value**.

The host is the server. The index rides in the URI's `?index=` (overridable per run with a dotted
`handle.index`, because one server hosts many indices). Use `elasticsearch+s://` / `opensearch+s://` for
TLS.

**Credentials,
when the cluster needs them, travel in the URI userinfo** (HTTP basic auth). As a result,
`--store keyring` moves the password to the OS keyring exactly as for the other backends.

**OpenSearch is the same
driver** behind the scheme (two `iq driver ls` entries, with their own supported version ranges,
sharing one implementation). Everything below applies to both. The only differences are internal:

- The [opensearch-go](https://github.com/opensearch-project/opensearch-go) client, because
  Elasticsearch's own client refuses non-Elasticsearch servers
- OpenSearch's point-in-time endpoint
- An `_id` keyset sort for scans, because OpenSearch predates Elasticsearch's `_shard_doc`.

```sh { title='Register an Elasticsearch source' }
iq add -n books 'elasticsearch://localhost:9200/?index=books'
```
```sh { title='Fetch the document whose _id is "2"' }
iq --src books '.["2"]'
```
```sh { title='Stream the index, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every _id' }
iq --src books --unbounded 'keys'
```
```sh { title='Query a different index on the same server' }
iq --src books.authors '.[]'
```
```sh { title='Register an OpenSearch source, identical surface' }
iq add -n logs 'opensearch://localhost:9201/?index=books'
```

Elasticsearch documents are JSON, so values need no type coercion. Integers keep exact precision
(large ones never collapse to a float). The driver injects each document's `_id` (Elasticsearch
metadata, stored outside `_source`) into the value as `_id`. As a result, a plain `.[]` stream is
self-describing and restorable, like a Mongo document. `--typed` is not needed for a lossless
backup.

The
`--unbounded` / streaming rules are identical to every backend. A scan pages the index with a
point-in-time and `search_after` (keyset pagination, sorted by `_shard_doc`), so it never re-reads
from an offset.

### Pushdown

By default, the driver translates the **equality** and **existence** clauses of a
`.[] | select(...)` filter into an Elasticsearch `bool` query. As a result, the cluster filters
before documents reach iq. The driver reads the index mapping once at connect, so an equality is
pushed only onto a field whose type matches it exactly. It is never pushed onto analyzed `text`,
where a term can wrongly exclude a match:

| `select(...)` clause | Pushed | Elasticsearch query | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"term": {"a": x}}` | a single top-level field mapped `keyword`/numeric/`boolean`/`ip` (or a `text` field's `.keyword` sub-field). The literal's type must match the field |
| `.a \| has` / `has("a")` | ✓ | `{"exists": {"field": "a"}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"bool": {"must_not": {"exists": …}}}` | key absence, exact |
| `E1 and E2` | ✓ | `{"bool": {"must": […]}}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"bool": {"should": […], "minimum_should_match": 1}}` | pushed only when **every** branch is pushable |
| `.a == null`, ranges, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side. A range excludes a missing field and orders types unlike jq's cross-type ordering. `== null` matches absent-or-null (no single term does). An analyzed-text or unmapped field has no exact term |

Pushdown never changes results, only speed. The full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter. `--explain` shows the query. `--no-compile` streams the
whole index and filters entirely client-side.

Whatever the `bool` query leaves behind, a **client-side raw-byte prefilter** runs the full predicate
over each hit's raw `_source` before it is decoded. The prefilter drops any hit that it can prove
the predicate rejects. So a fallback scan (a range, an equality on an unmapped or analyzed field) or
a partially-pushed scan skips the dominant `UseNumber` decode of the documents that the cluster
cannot exclude. The prefilter works on the bytes that `_search` already returned. This is the same
trick as the Redis prefilter.

The prefilter is byte-level and never changes results (the full jq still re-runs client-side). As a
result, it is bypassed in two cases where it gives no benefit or is wrong:

- The `term` query already captured the predicate exactly (the cluster returned only matches).
- The predicate references the injected `_id` field, which the raw `_source` does not carry.

### Raw commands

`iq exec` runs a raw [`_search`](https://www.elastic.co/guide/en/elasticsearch/reference/current/search-search.html):
the argument is a JSON search body (`{"query":{…},"size":…,"aggs":…}`) or a bare query object
(`{"match":{"title":"dune"}}`, wrapped as `{"query":…}`). It prints the whole reply, hits,
aggregations and all, as JSON:

```sh { title='Run a raw range query' }
iq --src books exec '{"query": {"range": {"year": {"gt": 2015}}}}'
```
```sh { title='Run a bare match query, wrapped automatically' }
iq --src books exec '{"match": {"author": "Kleppmann"}}'
```

`iq inspect` reads server and index metadata through these subcommands:

- `server` (node, cluster and version)
- `indices` (the server's indices)
- `mapping` (the selected index's field mapping, which shows what a term pushdown can use)
- `aliases` (the server's aliases).

`--only` narrows to those subcommands.

## HBase

[Apache HBase](https://hbase.apache.org) is a distributed wide-column store
on Hadoop, modeled on Google Bigtable.

After you register an `hbase://` source, the same jq interface works against a table. **The table is the keyspace, a row key is the key and the row is the value**.

The URI host is the ZooKeeper quorum (comma-separated hosts, default port `2181`). The table rides
in the URI's `?table=` (a `namespace:table`, overridable per run with a dotted `handle.table`). The ZooKeeper znode parent
defaults to `/hbase`, overridable with `?znode=`.

```sh { title='Register an HBase source' }
iq add -n books 'hbase://localhost:2181/?table=iq_books'
```
```sh { title='Fetch the row whose key is "1"' }
iq --src books '.["1"]'
```
```sh { title='Stream the table, filtered on a nested family.qualifier' }
iq --src books '.[] | select(.cf.author == "Herbert")'
```
```sh { title='Materialize every row key' }
iq --src books --unbounded 'keys'
```
```sh { title='Register with a multi-host quorum, a namespaced table, and a znode parent' }
iq add -n zk 'hbase://z1,z2,z3:2181/?table=ns:events&znode=/hbase-unsecure'
```

A row is a **nested object**: `{family: {qualifier: value}}`, so a cell is addressed as
`.cf.title` in jq. The row key is the map key, not a field of the row.

### Value encoding

HBase stores **no types**. Every cell is raw bytes. As a result, the driver presents a value
**without a guess** by default and **exactly** when you declare its encoding:

| Column | Read as | Written from |
| --- | --- | --- |
| undeclared (default) | valid UTF-8 → the string verbatim, otherwise a base64 string | a string → its UTF-8 bytes |
| `?types=cf:q=text` | the string verbatim | a string → its UTF-8 bytes |
| `?types=cf:q=bytes` | a base64 string | a base64 string → raw bytes (lossless for binary) |
| `?types=cf:q=int` / `long` | a number (4- / 8-byte big-endian, the HBase `Bytes` layout) | a whole number → those bytes |
| `?types=cf:q=double` | a number (8-byte IEEE-754) | a number → those bytes |
| `?types=cf:q=bool` | `true` / `false` (1 byte) | a bool → one byte |

The driver **never guesses** a numeric type from bytes (an 8-byte string is indistinguishable from a
`long`). It either *knows* (you declared it) or *does not guess* (text, else base64). Declared columns
round-trip losslessly in both directions. An undeclared column read back as base64 (non-UTF-8 bytes)
does **not** round-trip through a write. For that round-trip, declare it `bytes`.

```sh { title='Declare the numeric columns so they read as numbers' }
iq add -n books 'hbase://localhost:2181/?table=iq_books&types=cf:year=long,cf:price=double'
```
```sh { title='Filter on a declared column, no tonumber needed' }
iq --src books '.[] | select(.cf.year > 2015) | .cf.title'
```

### Key encoding

The row key follows the same contract as a cell: text when it is valid UTF-8,
base64 otherwise. If `?keytype=` declares its encoding, the declared encoding
applies (`&keytype=long` reads and writes the 8-byte `Bytes` layout). As a
result, a declared key round-trips losslessly.

### Pushdown

By default, the driver translates the **column-equality** clauses of a `.[] | select(...)` filter
into an HBase server-side filter (`SingleColumnValueFilter`, combined with `MustPassAll` for an
`and`). As a result, the region servers filter before rows reach iq. The driver encodes the
equality literal through the column's declared type, so the comparison matches the stored bytes:

| `select(...)` clause | Pushed | HBase filter | Notes |
| --- | :---: | --- | --- |
| `.cf.q == x` | ✓ | `SingleColumnValueFilter(cf, q, =, x)` | a two-segment `family.qualifier` path. The literal must encode to the column's declared type (undeclared → string) |
| `E1 and E2` | ✓ | `FilterList(MustPassAll, …)` | drops any conjunct it cannot push (widening) |
| `.cf.q \| has`, ranges, regex, `length`, `!=`, `or` | — | — | run client-side. Existence and `or` are not pushed because HBase has no clean superset-safe filter for them. Ranges cannot reproduce jq's cross-type ordering |

A row-key point read (`.["1"]`) is a direct `Get`, not a scan. Pushdown never changes results, only
speed. The full jq always re-runs client-side, so a pushed filter is a conservative pre-filter. Pass
`--no-compile` to stream the whole table and filter entirely client-side. `--explain` shows the
cost.

### Raw commands

HBase has **no query language**. As a result, `iq exec` is a small, safe verb set mapped straight
onto RPC, never a built query string. This makes it injection-safe. Each verb names its own table.
The read verbs are `get`, `scan` and `count`. The write verbs are `put` and `delete` (values encoded
through the same declared-type contract):

```sh { title='Get one row as JSON, or null' }
iq --src books exec get iq_books 1
```
```sh { title='Scan up to 10 rows as {rowkey: row}' }
iq --src books exec scan iq_books 10
```
```sh { title='Count the rows (a key-only scan)' }
iq --src books exec count iq_books
```
```sh { title='Write one cell' }
iq --src books exec put iq_books 5 cf:title Dune
```
```sh { title='Delete the whole row (add cf:title for one cell)' }
iq --src books exec delete iq_books 5
```

Structured writes go through `iq data` (`clear`/`drop`/`delete`, plus `--insert`) with write modes,
stats and `--explain`. `iq data delete <table> <rowkey>…` is the typed, capability-gated per-key
delete that formalizes the raw `delete` verb above. The raw `put`/`delete` verbs remain the
lower-level raw path (a single cell, a column), mirroring the other drivers' raw paths. `iq
inspect` lists the source namespace's tables (`tables`).

## MongoDB

[MongoDB](https://www.mongodb.com) is a document database that stores
JSON-like documents in collections.

After you register a `mongodb://` source (`mongodb+srv://` for SRV discovery), the same jq interface works against a collection. **The collection is the keyspace, a document's `_id` is the key and the document is the value**.

The database comes from the URI path. The collection comes from the URI's `?collection=` (the driver's own connection option, overridable per run with a dotted
`handle.collection`).

```sh { title='Register a MongoDB source' }
iq add -n books 'mongodb://localhost:27017/iq?collection=books'
```
```sh { title='Fetch the document whose _id is "2"' }
iq --src books '.["2"]'
```
```sh { title='Stream the collection, filtered' }
iq --src books '.[] | select(.year > 2015) | .title'
```
```sh { title='Materialize every _id' }
iq --src books --unbounded 'keys'
```

Because Mongo values are natively typed, numeric comparisons like `.year > 2015` need no
`tonumber`. This is unlike Redis, where everything is a string. Documents normalize to JSON with
the same rules everywhere:

- An `ObjectID` becomes its hex string.
- A date becomes an RFC 3339 string.
- Numbers stay numbers.
- Nested documents and arrays are preserved.

A missing `_id` reads as `null`.
The `--unbounded` / streaming rules are identical to every backend (`.[]`-rooted filters stream a cursor
in constant memory, `keys`/`.`/`map` materialize and require the flag).

### Pushdown

By default, the driver translates the **equality**, **range**, **regex**, **existence**, **length** and
**array** clauses of a `.[] | select(...)` filter into a native Mongo query. As a result, the server does the filtering (and can use an index) before the
documents ever reach iq:

```sh { title='Pushed down: the server filters by author' }
iq --src books '.[] | select(.author == "Robert C. Martin") | .title'
```
```sh { title='Forced client-side with --no-compile' }
iq --src books --no-compile '.[] | select(.author == "Robert C. Martin") | .title'
```

Pushdown never changes results, only speed. The full jq always re-runs client-side over whatever
comes back, so a pushed filter is only ever a conservative pre-filter. Pass `--no-compile` to skip
it and stream the whole collection, filtering entirely client-side. What it can push:

| `select(...)` clause | Pushed | MongoDB translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{a: x}` | number, string, bool, or null literal |
| `.a == 1 or .a == 2` | ✓ | `{a: {$in: [1, 2]}}` | an `or` of equalities on one field |
| `.a > n`, `>=`, `<`, `<=` | ✓ | native op + `$type` guards (an `$or`) | number/string literal. The translation reproduces jq's cross-type order, so the match is never a subset |
| `.a \| test("re")` | ✓ | `{a: {$regex: "re", $options: "is"}}` | portable patterns only (below), and jq's `i` and `m` flags. jq's `m` (dot-matches-newline) maps to PCRE's `s` |
| `has("a")`, `.a \| has("k")` | ✓ | `{a: {$exists: true}}` | exact, key presence, like jq's `has()` |
| `.a \| length == n` | ✓ | `{$size: n}` + `$type` guards (an `$or`) | jq `length` is polymorphic (array/string/object/number), so guards keep it a superset |
| `.a \| any(cond)` | ✓ | `{a: {$elemMatch: cond}}` (an `$or` with an object guard) | an array element satisfying a pushable element predicate. `cond` can combine the rows above |
| `.a != x` | ✓ | `{$or: [{a: {$ne: x}}, {a: {$type: "array"}}]}` | exact negation of equality (the guard keeps arrays, which jq never equates to a scalar) |
| `has("a") \| not` | ✓ | `{a: {$exists: false}}` | exact negation of existence |
| `.a \| any(.f == v) \| not` | ✓ | `{a: {$not: {$elemMatch: …}}}` | no array element matches. The element condition must be exact equality |
| `E1 and E2`, `E1 or E2` | ✓ | `$and` / `$or` of the above | an `and` can push only its pushable parts and drop the rest |
| negated range/regex/`size` | — | — | their filters are supersets and a negated superset is a subset (unrecoverable) |
| `.a > true`, `.a < null` | — | — | a range against bool/null has no clean superset |
| non-portable regex | — | — | engine-specific construct (below) |
| anything else | — | — | runs client-side, as under `--no-compile` |

**Portable regex.** iq's jq is [gojq](https://github.com/itchyny/gojq), which compiles a `test()`
pattern with Go's RE2. MongoDB uses PCRE. A pattern is pushed only when every construct it uses
means the same, or a superset, in both. These constructs qualify:

- Literals
- Anchors (`^` `$`)
- `.`
- Quantifiers (`* + ? {n,m}`)
- Alternation (`|`)
- Groups
- Character classes
- The ASCII `\d` `\w` `\s` `\D` `\W` shorthands
- Word boundaries (`\b`, `\B`).

`\S` is the one shorthand held back. RE2's `\s` omits the vertical tab that PCRE's `\s` matches.
As a result, RE2's `\S` matches a vertical tab that PCRE's does not.
If pushed, it drops a document jq keeps (`\s` diverges the other way, a superset the client-side
re-run corrects).

Flags follow the same rule. gojq accepts only `i`, `m`, `g`, and iq pushes `i`
(case-insensitive) and `m`. In jq, `m` means "`.` matches newline" (dotall), so it maps to PCRE's
`s`, not PCRE's `m`.

A pattern that uses one of these constructs is not portable and stays client-side:

- Lookaround (`(?=…)`)
- Backreferences (`\1`)
- Unicode properties (`\p{…}`)
- POSIX classes (`[[:…:]]`)
- Possessive quantifiers.

As a result, the pushed set always equals jq's.

### Raw commands

`iq exec` runs a single JSON command document with `runCommand` and prints the reply as
JSON. It is the raw path for server-side queries, aggregation and administration:

```sh { title='Run a native find command' }
iq --src books exec '{"find":"books","filter":{"year":{"$gt":2015}}}'
```
```sh { title='Run an aggregation pipeline' }
iq --src books exec '{"aggregate":"books","pipeline":[{"$group":{"_id":null,"avg":{"$avg":"$price"}}}],"cursor":{}}'
```

`iq inspect` runs these diagnostic database commands:

- `dbStats`
- `serverStatus`
- `listCollections`
- `collStats` (needs a collection, address it as `source.collection` or set `?collection=` on the
  source URI)
- `buildInfo`
- `hostInfo`.

`--only` narrows to those subcommands.

## Neo4j

[Neo4j](https://neo4j.com) is a graph database of nodes and relationships,
queried with Cypher.

After you register a `neo4j://` source, the same jq interface works against a node label. **The node label is the keyspace, a node's key is the key and the node is the value**.

Neo4j has no single keyspace, so a label is the addressable collection (like a Mongo collection or a
Cassandra table). The host is the bolt server. The label rides in the URI's `?label=` (overridable
per run with a dotted `handle.label`). Neo4j is multi-database, and the database is `?database=`
(default `neo4j`). Use `neo4j+s://` (or `bolt://` for a single instance, `+s`/`+ssc` for TLS).

**Credentials
travel in the URI userinfo** (bolt basic auth), so `--store keyring` moves the password to the OS
keyring exactly as for the other backends.

**The key is the elementId by default, or a property you name with `?key=`.** `elementId(n)` is
always present and unique, but opaque and not stable across database reloads. As a result, a
`?key=` property (a stable, human-meaningful id) reads better. The value carries `_id` (the
elementId) and `_labels` alongside the node's properties, so identity survives whichever key you
choose.

```sh { title='Register a Neo4j source keyed by the "id" property' }
iq add -n graph 'neo4j://neo4j:password@localhost:7687/?label=Person&key=id'
```
```sh { title='Fetch the Person whose id is 1' }
iq --src graph '.["1"]'
```
```sh { title='Stream the label, filtered' }
iq --src graph '.[] | select(.age > 40) | .name'
```
```sh { title='Materialize every key in the label' }
iq --src graph --unbounded 'keys'
```
```sh { title='Query a different label on the same database' }
iq --src graph.Book '.[]'
```

Neo4j values map to JSON directly. Integers keep exact precision. Bytes become base64. Temporal
and spatial values become their canonical ISO strings and `{x,y,srid}` objects.

A scan pages the label with keyset pagination ordered by `elementId(n)`. A `?key=` property is not
guaranteed unique (unlike a primary key). For this reason, a scan falls back to a node's elementId
whenever the key collides within a page, so no node is ever silently dropped. A bounded `.["v"]`
lookup that matches more than one node is an error rather than an arbitrary pick.

### Relationship collections

A **relationship type** is an addressable collection too, so you can query a graph's edges the same
way. Name it with `?rel=KNOWS` on the source, or address one per run with the `:` marker
(`handle.:KNOWS`). A leading colon can never be a valid label, so it unambiguously selects a
relationship type. A source names either a label or a relationship type, not both.

```sh { title='Register a relationship-type source' }
iq add -n edges 'neo4j://neo4j:password@localhost:7687/?rel=WROTE'
```
```sh { title='Stream WROTE edges, filtered (pushdown on the edge)' }
iq --src edges '.[] | select(.year > 2015)'
```
```sh { title='Address the type per run from any source' }
iq --src graph.:WROTE '.[]'
```

Each relationship's value is its properties plus a self-describing envelope: `_type` (the type),
`_id` (its elementId) and `_start` / `_end` (the endpoint node elementIds). The scan, key, count,
and `select(...)` pushdown rules are identical to nodes (the predicate is pushed onto the edge
variable).

**Relationship collections are read-only for now.** Creating an edge needs endpoint
resolution (which nodes to connect and by which key), and that is a further follow-up. As a
result, `iq` refuses a copy or `iq data` write into a relationship source with a clear message.
Write nodes with `?label=`.

### Pushdown

By default, the driver translates the **equality** and **existence** clauses of a
`.[] | select(...)` filter into a Cypher `WHERE` clause. As a result, the server filters before
nodes reach iq. The clause uses dynamic `n[$prop]` access, so the property name is a parameter,
never string-built:

| `select(...)` clause | Pushed | Cypher | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `n[$p] = $v` | equality. A `null` literal becomes `n[$p] IS NULL` (a missing property) |
| `.a \| has` / `has("a")` | ✓ | `n[$p] IS NOT NULL` | key presence, exact |
| `has("a") \| not` | ✓ | `n[$p] IS NULL` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable |
| `.a > x` / `.a <= x`, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side. Cypher compares mismatched types as null rather than by jq's cross-type ordering. A nested path has no flat Neo4j property. As a result, pushing these can wrongly exclude a node that jq keeps |

Pushdown never changes results, only speed. The full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter. `--explain` shows the `WHERE` clause. `--no-compile`
streams the whole label and filters entirely client-side.

### Writing

A copy into a Neo4j label upserts each node with `MERGE (n:Label {key}) SET n += props`, so a re-run
converges. **Writing needs a `?key=` property** (a MERGE key must be stable and the elementId is
server-assigned) **and a uniqueness constraint on it** (`CREATE CONSTRAINT ... REQUIRE n.<key> IS
UNIQUE`). Without the constraint, a MERGE can match and overwrite several nodes at once. For this
reason, the write is refused up front rather than fanning out.

`iq data clear` detach-deletes every node in the
label (and the relationships they hold). A label is not a droppable container, so `iq data drop` is
unsupported. Writes set node properties only. Relationships are a follow-up.

### Raw commands

`iq exec` runs raw, parameterized [Cypher](https://neo4j.com/docs/cypher-manual/current/). The first
argument is the statement. An optional second argument is a JSON object of parameters (passed as
parameters, never string-built into the statement). It prints the result rows as JSON:

```sh { title='Run parameterized Cypher' }
iq --src graph exec 'MATCH (n:Person) WHERE n.age > $min RETURN n.name, n.age' '{"min": 40}'
```
```sh { title='Count the nodes' }
iq --src graph exec 'MATCH (n) RETURN count(n) AS nodes'
```

`iq inspect` reads deployment and schema metadata through these subcommands:

- `server` (components and version)
- `databases` (the deployment's databases)
- `labels` (the addressable node labels)
- `reltypes` (relationship types)
- `constraints` (which shows the uniqueness constraint a `?key=` write needs).

`--only` narrows to those subcommands.

## Redis

[Redis](https://redis.io) is an in-memory key-value store used as a cache,
database and message broker.

After you register a `redis://` source (`rediss://` for TLS), the same jq interface works against the
Redis keyspace. **A key maps directly to a Redis key and the value is whatever that key holds**.

The database index comes from the URI path (`/0`). Every value is a string, so numeric
comparisons need `tonumber`.

```sh { title='Register a Redis source' }
iq add -n cache redis://localhost:6379/0
```
```sh { title='Fetch the key "greeting"' }
iq --src cache '.greeting'
```
```sh { title='Stream the keyspace, filtered (string values need tonumber)' }
iq --src cache '.[] | select((.year|tonumber) > 2015) | .title'
```
```sh { title='Materialize every key' }
iq --src cache --unbounded 'keys'
```

The `--unbounded` / streaming rules match every backend (`.[]`-rooted filters stream in constant
memory, `keys`/`.`/`map` materialize and require the flag).

### Value encoding

Each fetched Redis value is normalized to JSON by type:

| Redis type | JSON shape |
| --- | --- |
| string | the string verbatim (numeric strings stay strings, use `tonumber`) |
| hash | object `{field: value}` |
| list | array, in list order |
| set | array, sorted lexically (sets have no native order) |
| sorted set | array of `{"member": ..., "score": ...}`, in ascending score order |
| stream | array of `{"id": ..., "fields": {field: value}}`, in entry order |
| RedisJSON | the stored document, parsed as JSON |
| missing key | `null` |

Other module types (time series, bloom, …) have no frozen encoding yet. `iq` refuses a named
read of one with a clear message.

### Pushdown

Redis has no server-side filtering. As a result, a compiled predicate drives a **client-side
raw-byte prefilter** instead. On a streaming scan, the prefilter tests each RedisJSON value against
the predicate on its raw JSON.GET bytes. When the value provably cannot match, the prefilter drops
it before the (dominant) decode. Every other type is decoded and included unchanged.

The full jq always re-runs client-side, so output is identical with or without the prefilter. The
prefilter only skips decoding documents that the filter rejects. `--no-compile` turns it off.

### Raw commands

`iq exec` forwards a command to the database verbatim and prints the reply in redis-cli style.
It is the raw path for writes, administration and seeding that the jq read path does not cover:

```sh { title='Set a key, replies "OK"' }
iq --src cache exec SET greeting hello
```
```sh { title='Read it back, replies "hello"' }
iq --src cache exec GET greeting
```
```sh { title='Increment a counter, replies (integer) 1' }
iq --src cache exec INCR counter
```
```sh { title='Read a missing key, replies (nil)' }
iq --src cache exec GET missing
```

Its output mirrors redis-cli's cooked style:

- Bulk strings are quoted.
- Integers appear as `(integer) N`.
- A missing value appears as `(nil)`.
- Arrays appear as a numbered, indented list.

The client uses RESP2, so aggregate replies match redis-cli's classic flat output. Status replies
such as `OK` and `PONG` appear quoted. This is a limitation of the underlying client, which does not
distinguish them from bulk strings.

`iq inspect` runs `INFO`. `--only` narrows it to sections (`server`, `clients`, `memory`,
`persistence`, `stats`, `replication`, `cpu`, `keyspace`). With no section, it runs the full `INFO`.

## File dumps

A `file://` source reads a database dump straight from disk. As a result, you can do these
operations on a snapshot with the same jq interface, with **no running server**:

- Query it
- Inspect it for shape
- Diff it
- Restore it.

A `file://` source is read-only. A `file://` endpoint is never a copy *destination*.
`iq exec`/`iq inspect` (which need a live server) do not apply.

```sh { title='Register a dump like any source' }
iq add file:///backups/prod.rdb -n snap
```
```sh { title='Bounded read of one key' }
iq --src snap '.["session:42"]'
```
```sh { title='Streamed scan' }
iq --src snap '.[] | select(.active)'
```
```sh { title='Whole-dataset filters obey --unbounded' }
iq --src snap 'keys' --unbounded
```
```sh { title='Restore the dump into a live source' }
iq --src snap --insert prod
```
```sh { title='Diff a dump against a live source' }
iq diff snap prod --data
```

The format is detected from the file's content (or forced with a `?format=` query, for example
`file:///d.bin?format=bson`). A gzipped dump is unwrapped automatically. A gzipped dump must
pass `?format=`, because its content is not sniffable through the compression. On Windows, a drive
path takes the `file:///C:/path/to/dump.json` form (forward slashes, three slashes before the
drive letter).

| Format | Produced by | Notes |
| --- | --- | --- |
| Typed JSONL | `iq --src <s> --typed -o <file>` | iq's own dump, lossless round-trip |
| Typed YAML | `iq --src <s> --typed -y -o <file>` | the same records as YAML documents, auto-detected by a `.yaml`/`.yml` name, else `?format=yaml` |
| Redis RDB | `redis-cli --rdb`, `SAVE` | values match a live scan, RDB ≤ v12 (Redis ≤ 7.2) |
| Mongo BSON | `mongodump` | single `.bson` file |
| Mongo Extended JSON | `mongoexport` | one document per line, or a `--jsonArray` array |
| DynamoDB JSON | S3 `export-table-to-point-in-time`, `aws dynamodb scan` | needs `?format=dynamodb-json` and a `?keys=pk[:S][,sk[:N]]` key schema (a dump carries items but not the table's key schema). Export files are gzipped NDJSON |
| Cassandra CSV | `cqlsh COPY … TO 'f.csv'` | needs `?format=cassandra-csv`, `?keys=col1[,col2]` naming the primary-key columns, and `?types=col=cqltype,…` for the non-text columns (COPY writes every value as text). Column names come from a `WITH HEADER=TRUE` row, else `?columns=`. Scalar columns only |
| Neo4j APOC JSON | `CALL apoc.export.json.all('g.json',{})` | needs `?format=neo4j-json` and a keyspace selector: either `?label=<Label>` for its nodes or `?rel=<Type>` for its relationships (a dump holds the whole graph). JSON Lines or `ARRAY_JSON`. Same `_id`/`_labels`/`_type`/`_start`/`_end` envelope as the live driver. `?key=<prop>` keys by a property. `_id` is APOC's numeric export id, not the live elementId. The binary `neo4j-admin database dump` and APOC's `useTypes`/`JSON_ID_AS_KEYS` variants are not supported |

The whole dump streams. A `file://` source never holds all values in memory (whole-dataset
materialization is the core's, gated by `--unbounded`, exactly as for a live backend).

**Restore fidelity** is the record round-trip. Values and native types reconstruct, but these do
not carry:

- TTLs
- Exact encodings
- Stream consumer groups
- RDB module types
- Mongo indexes.

**Neo4j record ids.** A dump's `_id` and a relationship's `_start`/`_end`, is APOC's numeric
export id, not the live driver's `elementId`. The reason is that APOC's default export does not
write elementIds. Within one dump, the ids are self-consistent. A relationship's `_start`/`_end`
reference the same ids that its nodes carry as `_id`. As a result, `?rel=` endpoints resolve against
the default-keyed `?label=` nodes exactly as they do live.

The numeric id cannot do two things, both by nature:

- It does not match the `_id` of the same node read from the live source (different id schemes).
- It is not stable across re-exports (Neo4j reuses a deleted node's id, the reason `id()` is
  deprecated in favor of `elementId()`).

Key on a business property with `?key=<prop>`. The result is an identifier that is stable and
identical across a live source and its dump. That property reads the same value everywhere.

**Decode cache.** Re-querying the same large dump re-parses it every time. For this reason, iq
caches the decoded, normalized records of a scanned dump above 4 MiB. It reads them back on later
queries and skips the RDB/BSON/JSON decode (a warm scan of an 8 MiB dump runs several times faster).

The cache lives under `<user cache dir>/iq/dumps`. It keys on the dump's path, size and mtime. As a
result, editing the dump invalidates the cache automatically and needs no action from you. A stale
or absent cache only means a full decode, never a wrong or failed query.

Only full scans populate it (a bounded
key read does not) and stdin is never cached.

Alongside the records, a scan writes a **per-page key index** (a Bloom filter per page). As a
result, a later bounded read (`iq --src snap '.["id"]'`) decodes only the pages that can hold a
wanted key, instead of streaming the whole cache. A point lookup or a missing-key check stays fast
even on a huge dump. The index is on by default and distribution-agnostic (it hashes keys, so
random ids/UUIDs are fine). If a very large keyspace makes the index build memory unwelcome, skip it
with `--no-cache-index` (the flat cache is still written, a bounded read only streams it).

Manage the cache with `iq cache`. Bypass it for one run with `--no-cache` :material-earth:{ title="Global flag" }. Set a default
with `iq config set no-cache true` / `iq config set no-cache-index true` (`--no-cache-index` :material-earth:{ title="Global flag" }
too).

- `iq cache location`: print the cache directory path.
- `iq cache stat [-j/--json | -y/--yaml]`: list cached dumps with their sizes.
- `iq cache clear [<source>|<path>]`: remove all cached dumps, or only one source's/path's.

**Prefilter.** A file source pushes no filter to a server (there is none). But on a streaming
scan of an **uncached typed-JSONL** dump, a compiled predicate drives a **client-side raw-byte
prefilter**. The prefilter tests the raw `value` bytes of each record against the predicate. It
drops a provable non-match before it is decoded. As a result, the dominant JSON decode is skipped
for records that the filter rejects.

Every other format (YAML, RDB, BSON, Extended JSON, DynamoDB JSON, Cassandra
CSV, Neo4j APOC) and any scan served from a fresh decode cache (whose bytes are already-decoded
CBOR and already fast to stream), decodes in full and lets the client filter.

The full jq always
re-runs client-side, so output is identical with or without the prefilter. The prefilter only skips
decoding dropped records. A prefiltered scan deliberately does not populate the decode cache (to
populate it, the scan must decode everything). `--no-compile` turns the prefilter off.
