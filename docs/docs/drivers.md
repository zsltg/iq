---
icon: material/engine-outline
---

# Drivers

`iq` picks the backend from a source's URI scheme and the query core is
driver-agnostic, so further backends slot in behind the same port.

Each driver below documents its keyspace mapping, value encoding, predicate
pushdown and raw commands.

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

- **One URI, native nouns.** The URI scheme picks the driver, the keyspace rides in the URI as the
  backend's own noun (`?collection=`, `?table=`, `?database=`, `?label=`/`?rel=`, `?index=`) and a
  query overrides it per run with the dotted `handle.<keyspace>` suffix (see [Sources](sources.md)).
- **One jq interface.** A bounded filter fetches exactly the named keys, a missing key reads as
  `null`, never an error, a `.[]`-rooted filter streams the keyspace in bounded pages, a filter
  that collapses the keyspace into one value materializes only behind `--unbounded` (see
  [Read strategies](how-it-works.md#read-strategies)).
- **Pushdown never changes results.** A pushed predicate is only ever a conservative pre-filter,
  server-side where the backend can filter, or a client-side raw-byte prefilter that drops a provable
  non-match before decode where it cannot (Redis, on RedisJSON values, Elasticsearch/OpenSearch and
  Couchbase, over the residual their server-side query cannot narrow). The full jq always re-runs
  client-side, so
  output is identical with or without it and [`--explain`](query-plan.md) shows exactly
  what was pushed.
- **Capabilities are explicit.** Filtered scans, count estimates, writes, clear, drop and per-key
  delete are opt-in ports, a backend implements what its model supports and a command against a
  missing capability fails with a clear message instead of emulating it (Redis, whose DB index cannot
  be removed, has no `drop`, the read-only file dump has no per-key `delete`).
- **Values round-trip.** Every value normalizes to JSON under a frozen per-backend encoding
  contract and a `--typed` dump restores through `--insert` losslessly (see
  [Write data](write-data.md)).
- **Bounded and redacted.** `--timeout` bounds every backend call and a URI's password is
  redacted from every listing, log line and error.
- **Native commands.** `iq exec` speaks the backend's own language, verbatim where one exists
  (Redis commands, Mongo command documents, CQL, PartiQL, Cypher, Mango, the Elasticsearch DSL), a small
  fixed verb set where none does (HBase), see each driver's Raw commands section. Every `iq` flag
  must come before `exec`, everything after it is forwarded to the backend untouched.

### Capabilities

Which opt-in ports each driver implements. A `—` is not a gap in the docs, the command fails
with a clear message rather than emulating what the backend cannot do.

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

`data clear` empties a keyspace and keeps it, `data drop` removes the keyspace itself, and
`data delete` removes named keys, see [Write data](write-data.md#delete-data-delete).

The count estimate is a cheap metadata total, never a second scan, so an unfiltered scan can
show its progress against a rough total. It is a hint that can drift as the keyspace changes,
and it is read only for an unfiltered scan, never for a pushed-down filtered one.


## Cassandra

[Apache Cassandra](https://cassandra.apache.org) is a distributed wide-column
store built for high write throughput across many nodes.

Register a `cassandra://` source and the same jq interface works against a table, where **the table
is the keyspace, a row's primary key is the key and the row is the value**. The keyspace comes from
the URI path, the table from the URI's `?table=` (overridable per run with a dotted `handle.table`),
multiple contact points are comma-separated and `?consistency=` sets the read/write consistency
level (default `QUORUM`).

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
`--unbounded` / streaming rules are identical to every backend. The table's schema is read once at
connect time, so the driver knows the primary-key columns and their types.

### Key encoding

A row's key is its **full primary key**, the partition-key columns followed by the clustering
columns. A single-column primary key renders as its bare value (`42`, a uuid, a text value, like a
Mongo `_id`), a composite primary key renders as a compact JSON array in schema order:

```sh { title='Fetch by a single-column key, bare' }
iq --src sales '.["US"]'
```
```sh { title='Fetch by a composite key ((country), id), a JSON array' }
iq --src sales '.["[\"US\",1]"]'
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

### Pushdown

By default a `.[] | select(...)` filter's **equality** clauses are translated into a CQL `WHERE` so
the cluster filters before rows reach iq. Only equality and same-column membership are pushed:

| `select(...)` clause | Pushed | CQL translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `a = ?` | a single top-level column that exists in the table |
| `.a == 1 or .a == 2` | ✓ | `a IN (?, ?)` | an `or` of equalities on one column |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| ranges, regex, `has`, `length`, negations, nested paths | — | — | run client-side, ranges are skipped because jq treats a missing field as the lowest value, which CQL cannot reproduce |

A pushed `WHERE` that does not resolve to the full partition key runs with `ALLOW FILTERING`, so the
coordinator does the scan, an opt-in cost (it is shown in `--explain`). Pushdown never changes
results, only speed, the full jq always re-runs client-side, so a pushed filter is a conservative
pre-filter. Pass `--no-compile` to stream the whole table and filter entirely client-side.

### Raw commands

`iq exec` runs a CQL statement verbatim and prints the rows as JSON, the raw path for
server-side queries, DDL and administration the jq read path does not cover:

```sh { title='Read the cluster version' }
iq --src books exec 'SELECT release_version FROM system.local'
```
```sh { title='Run a filtered CQL query' }
iq --src books exec "SELECT title FROM books WHERE year > 2015 ALLOW FILTERING"
```

`iq inspect` reads the system schema, `local` (cluster/version), `tables` (the keyspace's tables),
and `columns` (a table's columns), `--only` narrows to those subcommands.

## Couchbase

[Couchbase](https://www.couchbase.com) is a distributed document database
combining a key-value engine with SQL++ queries.

Register a `couchbase://` source and the same jq interface works against a
collection, where **the collection is the keyspace, a document's ID is the key
and the JSON document is the value**.

A Couchbase cluster nests bucket → scope → collection, the host is the cluster,
the bucket rides in the URI's `?bucket=` (required for keyspace work) and the
collection is `?collection=` accepting `orders` or `sales.orders` (scope
defaults to `_default`), overridable per run with a dotted
`handle.[scope.]collection`. Switching buckets is a different source. Use
`couchbases://` for TLS.

**Credentials travel in the URI userinfo** (SDK `PasswordAuthenticator`), so
`--store keyring` moves the password to the OS keyring exactly as for the other
backends.

The SDK's application telemetry is disabled explicitly, so the tool reports
nothing back to the cluster.

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

Couchbase documents are JSON, so values need no type coercion, integers keep
exact precision (large ones never collapse to a float).

Couchbase has no per-key `iq data delete` yet (`clear` and `drop` work), it is
a v1 follow-up.

The document ID is KV metadata, not part of the value, so it is never injected
into the document.

A non-JSON (binary) document is returned as a string on a `.["k"]` lookup and
skipped by a scan (the query service returns only JSON).

A bounded `.["k"]` lookup is a KV get, a scan is a **SQL++ keyset walk ordered
by `META().id`** (never OFFSET/LIMIT paging), so it streams with bounded
memory.

Scans read at `RequestPlus` consistency, so the tool sees its own just-written
documents (read-your-writes).

### Pushdown

By default a `.[] | select(...)` filter's **equality**, **range** and **existence** clauses are
translated into a SQL++ `WHERE` (with named parameters) so the query service filters before documents
reach iq:

| `select(...)` clause | Pushed | SQL++ predicate | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `` `a` = $p `` | equality, a `null` literal widens to `` (`a` IS NULL OR `a` IS MISSING) `` |
| `.a > x` / `.a <= x` | ✓ | `` (`a` > $p OR ISSTRING(`a`) OR …) `` | range, widened with `ISTYPE()` clauses so jq's cross-type ordering (null < bool < number < string < array < object) is reproduced. SQL++ comparison operators are type-restricted, so higher/lower-ranked types are re-included explicitly |
| `.a \| has` / `has("a")` | ✓ | `` `a` IS NOT MISSING `` | key presence, exact |
| `has("a") \| not` | ✓ | `` `a` IS MISSING `` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable, an all-equality OR over one field collapses to `` `a` IN $p `` |
| `!=`, regex, `length`, `any`, nested-array tests | — | — | run client-side: a plain keyset scan is used, because SQL++ semantics for these could wrongly exclude a document jq would keep |

Every value rides as a named parameter, never concatenated, keyspace and field
identifiers are validated and backtick-quoted, so nothing user-supplied is ever
interpolated raw.

Pushdown never changes results, only speed, the full jq always re-runs
client-side, so a pushed filter is a conservative pre-filter, `--explain` shows
the `WHERE` and `--no-compile` streams the whole collection and filters
entirely client-side.

Whatever the `WHERE` leaves behind, a **client-side raw-byte prefilter** runs
the full predicate over each row's raw value before it is decoded and drops any
row it can prove the predicate rejects.

So a fallback scan (a `!=`, a regex) or a partially-pushed scan (a dropped
conjunct) skips the dominant `UseNumber` decode of the documents the query
service cannot exclude, the same trick as the Redis and Elasticsearch
prefilters, on the bytes the keyset scan already returned.

It is byte-level and never changes results (the full jq still re-runs
client-side), so it is bypassed in the one case where it is useless, when
the `WHERE` already captured the predicate exactly (the query service returned
only matches).

A Couchbase document's ID is KV metadata, never injected into the value, so,
unlike the Elasticsearch prefilter, there is no injected-field case to disable
it.

**Index requirement.** A SQL++ scan needs an index on the collection.
On Server 7.6+ a sequential scan answers index-free queries automatically, on
7.0–7.5, or for large collections, create one,
`CREATE PRIMARY INDEX ON \`bucket\`.\`scope\`.\`collection\``. A "no index
available" error (code 4000) is reported with exactly that hint.

### Raw commands

`iq exec` runs a raw [SQL++](https://docs.couchbase.com/server/current/n1ql/n1ql-language-reference/index.html)
statement, the first argument is the statement and an optional second argument is a JSON object of
named parameters (bound end-to-end, never string-built):

```sh { title='Run a parameterized SQL++ query' }
iq --src books exec 'SELECT META(t).id, t.* FROM `iq` t WHERE t.year > $min' '{"min": 2015}'
```
```sh { title='Count the documents' }
iq --src books exec 'SELECT COUNT(*) AS n FROM `iq`'
```

`iq inspect` reads cluster and bucket metadata, `cluster` (nodes and services), `buckets` (the
cluster's buckets), `collections` (the selected bucket's scopes and collections) and `indexes` (the
query indexes), `--only` narrows to those subcommands.

## CouchDB

[Apache CouchDB](https://couchdb.apache.org) is a document database that
speaks HTTP and JSON, built around multi-master replication.

Register a `couchdb://` source and the same jq interface works against a
database, where **the database is the keyspace, a document's `_id` is the key
and the document is the value**.

The host is the CouchDB server, the database rides in the URI's `?database=`
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
exact precision (large ones never collapse to a float), `_id` and `_rev` are
kept in the document. Design documents (`_design/…`) are database metadata and
are skipped by scans. The `--unbounded` / streaming rules are identical to
every backend.

### Pushdown

By default a `.[] | select(...)` filter's **equality**, **range**, **existence**, byte-safe
**regex** and **length** clauses are
translated into a Mango `_find` selector so the server filters before documents reach iq:

| `select(...)` clause | Pushed | Mango selector | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"a": x}` | equality, a `null` literal also matches an absent field |
| `.a > x` / `.a <= x` | ✓ | `{"$or": [{"a": {"$gt": x}}, …]}` | range, widened with `$type` clauses so jq's cross-type ordering (null < bool < number < string < array < object, matching CouchDB collation) is reproduced |
| `.a \| test("re")` | ✓ | `{"a": {"$regex": "re"}}` | byte-safe ASCII patterns only (below), a case-insensitive or non-ASCII-safe pattern runs client-side |
| `.a \| has` / `has("a")` | ✓ | `{"a": {"$exists": true}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"a": {"$exists": false}}` | key absence, exact |
| `.a \| length == n` | ✓ | `{"$or": [{"a": {"$size": n}}, {"a": {"$type": …}}, …]}` | jq `length` is polymorphic (array/string/object/number), so the array `$size` is widened with per-type `$type` clauses to a superset, `n == 0` also matches null and a missing field |
| `E1 and E2` | ✓ | `{"$and": […]}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"$or": […]}` | pushed only when **every** branch is pushable |
| `!=`, `any`, nested-array tests, a case-insensitive or non-byte-safe regex | — | — | run client-side: a plain `_all_docs` scan is used, because Mango's semantics for these could wrongly exclude a document jq would keep |

**Byte-safe regex.** CouchDB's Mango `$regex` runs its Erlang engine over the document's raw UTF-8
bytes with no unicode option and skips a non-string field (an `is_binary` guard, exactly as jq's
`test` over a non-string is false). A pattern is pushed only when it means the same byte-for-byte as
gojq's RE2, pure-ASCII literals, anchors, quantifiers, groups, positive classes and the `\d \w \s`
shorthands.

An unescaped `.`, a negated class (`[^…]`, `\D`, `\W`, `\S`), any non-ASCII byte, or the
`i` flag is declined and runs client-side, because over multi-byte text a byte engine and a rune
engine diverge. The subject string can be any Unicode, only the pattern is constrained.

Pushdown never changes results, only speed, the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter, `--explain` shows the selector and `--no-compile` streams the
whole database and filters entirely client-side. A pushed `_find` uses whatever Mango index fits
(create one in CouchDB for large databases), without one CouchDB warns and falls back to its built-in
index.

### Raw commands

`iq exec` runs a raw [Mango `_find`](https://docs.couchdb.org/en/stable/api/database/find.html), the
argument is a JSON `_find` request (`{"selector":{…},"limit":…}`) or a bare selector (wrapped as
`{"selector":…}`) and it prints the matching documents with the paging bookmark:

```sh { title='Run a Mango _find request' }
iq --src books exec '{"selector": {"year": {"$gt": 2015}}, "limit": 10}'
```
```sh { title='Run a bare selector, wrapped automatically' }
iq --src books exec '{"author": "Martin Kleppmann"}'
```

`iq inspect` reads server and database metadata, `server` (version and vendor), `databases` (the
server's databases), `dbinfo` (the selected database's document count, sizes and update sequence),
and `indexes` (its Mango indexes), `--only` narrows to those subcommands.

## DynamoDB

[Amazon DynamoDB](https://aws.amazon.com/dynamodb/) is AWS's managed
serverless key-value and document database.

Register a `dynamodb://` source and the same jq interface works against a table, where **the table
is the keyspace, an item's primary key is the key and the item is the value**. The region is the URI
host, the table rides in the URI's `?table=` (overridable per run with a dotted `handle.table`). An
optional `?endpoint=` points at DynamoDB Local. **Credentials never travel in the URI**, the AWS
default credential chain (environment, `~/.aws`, IAM role) resolves them, so no secret touches the
config or keyring.

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
streaming rules are identical to every backend. The table's key schema is read once at connect time,
so the driver knows the partition and (optional) sort key and their types.

### Key encoding

An item's key is its **full primary key**, the partition key, then the sort key when the table has
one. A partition-key-only table renders the key as its bare value (`42`, a string, like a Mongo
`_id`), a table with a sort key renders a compact JSON array in schema order:

```sh { title='Fetch by a partition-key-only key, bare' }
iq --src sales '.["US"]'
```
```sh { title='Fetch by a composite key (partition "US", sort 1), a JSON array' }
iq --src sales '.["[\"US\",1]"]'
```

The array elements are the key attributes' string forms and are coerced back through the key schema
on lookup, so a numeric (`N`) or binary (`B`) key round-trips faithfully.

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

Note the set types (`SS`/`NS`/`BS`) normalize to a plain array, so a copy **back** into DynamoDB
writes them as a list (`L`), not a set and a number presented as a string (auto/string decimal
mode) writes back as a string (`S`). Use `--format.decimal number` for a numeric round-trip.

### Pushdown

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
a DynamoDB `Scan` `FilterExpression` so the service filters before items reach iq. Every attribute is
referenced through a `#name` placeholder, so a reserved word (`name`, `status`, `size`, `year`, …) is
always safe:

| `select(...)` clause | Pushed | FilterExpression | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `#a = :v` | a single top-level attribute, string, number, or boolean literal |
| `.a \| has` / `has("a")` | ✓ | `attribute_exists(#a)` | key presence, exact |
| `has("a") \| not` | ✓ | `attribute_not_exists(#a)` | key absence, exact |
| `E1 and E2` | ✓ | `AND` of the pushable parts | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `OR` of the parts | pushed only when **every** branch is pushable |
| ranges, regex, `length`, `!=`, nested paths | — | — | run client-side, ranges are skipped because jq orders a string above every number, which a typed DynamoDB comparison cannot reproduce |

A `Scan` reads the whole table (there is no `WHERE` on a primary-key membership like a relational
store), the `FilterExpression` only avoids shipping non-matching items over the wire, the cost is
shown in `--explain`. Pushdown never changes results, only speed, the full jq always re-runs
client-side, so a pushed filter is a conservative pre-filter. Pass `--no-compile` to stream the whole
table and filter entirely client-side.

### Raw commands

`iq exec` runs a [PartiQL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ql-reference.html)
statement verbatim and prints the items as JSON, the raw path for server-side queries and
writes the jq read path does not cover:

```sh { title='Fetch one item by key with PartiQL' }
iq --src books exec 'SELECT * FROM "books" WHERE id = 2'
```
```sh { title='Run a filtered PartiQL query' }
iq --src books exec 'SELECT title FROM "books" WHERE "year" > 2015'
```

`iq inspect` reads table metadata, `tables` (the region's tables) and `table` (the selected table's
key schema, item count, size, billing mode and index names), `--only` narrows to those subcommands.

## Elasticsearch & OpenSearch

[Elasticsearch](https://www.elastic.co/elasticsearch) and its fork
[OpenSearch](https://opensearch.org) are document search and analytics
engines built on Lucene, queried over HTTP with JSON.

Register an `elasticsearch://` (or `opensearch://`) source and the same jq interface works against an
index, where **the
index is the keyspace, a document's `_id` is the key and its `_source` is the value**. The host is
the server, the index rides in the URI's `?index=` (overridable per run with a dotted
`handle.index`, because one server hosts many indices). Use `elasticsearch+s://` / `opensearch+s://` for
TLS.

**Credentials,
when the cluster needs them, travel in the URI userinfo** (HTTP basic auth), so `--store keyring`
moves the password to the OS keyring exactly as for the other backends.

**OpenSearch is the same
driver** behind the scheme (two `iq driver ls` entries, with their own supported version ranges,
sharing one implementation), everything below applies to both, the only differences are internal (the
[opensearch-go](https://github.com/opensearch-project/opensearch-go) client, because Elasticsearch's
own client refuses non-Elasticsearch servers, OpenSearch's point-in-time endpoint and, because it
predates Elasticsearch's `_shard_doc`, an `_id` keyset sort for scans).

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

Elasticsearch documents are JSON, so values need no type coercion, integers keep exact precision
(large ones never collapse to a float). Each document's `_id` (Elasticsearch metadata, stored
outside `_source`) is injected into the value as `_id`, so a plain `.[]` stream is self-describing
and restorable, like a Mongo document, `--typed` is not needed for a lossless backup.

The
`--unbounded` / streaming rules are identical to every backend, a scan pages the index with a
point-in-time and `search_after` (keyset pagination, sorted by `_shard_doc`), so it never re-reads
from an offset.

### Pushdown

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
an Elasticsearch `bool` query so the cluster filters before documents reach iq. The index mapping is
read once at connect, so an equality is pushed only onto a field whose type matches it exactly,
never onto analyzed `text`, where a term can wrongly exclude a match:

| `select(...)` clause | Pushed | Elasticsearch query | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{"term": {"a": x}}` | a single top-level field mapped `keyword`/numeric/`boolean`/`ip` (or a `text` field's `.keyword` sub-field), the literal's type must match the field |
| `.a \| has` / `has("a")` | ✓ | `{"exists": {"field": "a"}}` | key presence, exact |
| `has("a") \| not` | ✓ | `{"bool": {"must_not": {"exists": …}}}` | key absence, exact |
| `E1 and E2` | ✓ | `{"bool": {"must": […]}}` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `{"bool": {"should": […], "minimum_should_match": 1}}` | pushed only when **every** branch is pushable |
| `.a == null`, ranges, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side: a range excludes a missing field and orders types unlike jq's cross-type ordering, `== null` matches absent-or-null (no single term does) and an analyzed-text or unmapped field has no exact term |

Pushdown never changes results, only speed, the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter, `--explain` shows the query and `--no-compile` streams the
whole index and filters entirely client-side.

Whatever the `bool` query leaves behind, a **client-side raw-byte prefilter** runs the full predicate
over each hit's raw `_source` before it is decoded and drops any hit it can prove the predicate
rejects. So a fallback scan (a range, an equality on an unmapped or analyzed field) or a
partially-pushed scan skips the dominant `UseNumber` decode of the documents the cluster cannot
exclude, the same trick as the Redis prefilter, on the bytes `_search` already returned.

It is
byte-level and never changes results (the full jq still re-runs client-side), so it is bypassed in two
cases where it is useless or wrong, when the `term` query already captured the predicate exactly
(the cluster returned only matches) and when the predicate references the injected `_id` field, which
the raw `_source` does not carry.

### Raw commands

`iq exec` runs a raw [`_search`](https://www.elastic.co/guide/en/elasticsearch/reference/current/search-search.html):
the argument is a JSON search body (`{"query":{…},"size":…,"aggs":…}`) or a bare query object
(`{"match":{"title":"dune"}}`, wrapped as `{"query":…}`) and it prints the whole reply, hits,
aggregations and all, as JSON:

```sh { title='Run a raw range query' }
iq --src books exec '{"query": {"range": {"year": {"gt": 2015}}}}'
```
```sh { title='Run a bare match query, wrapped automatically' }
iq --src books exec '{"match": {"author": "Kleppmann"}}'
```

`iq inspect` reads server and index metadata, `server` (node, cluster and version), `indices` (the
server's indices), `mapping` (the selected index's field mapping, which shows what a term pushdown
can use) and `aliases` (the server's aliases), `--only` narrows to those subcommands.

## HBase

[Apache HBase](https://hbase.apache.org) is a distributed wide-column store
on Hadoop, modeled on Google Bigtable.

Register an `hbase://` source and the same jq interface works against a table, where **the table is
the keyspace, a row key is the key and the row is the value**. The URI host is the ZooKeeper quorum
(comma-separated hosts, default port `2181`), the table rides in the URI's `?table=` (a
`namespace:table`, overridable per run with a dotted `handle.table`). The ZooKeeper znode parent
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

HBase stores **no types**, every cell is raw bytes, so a value is presented **without a guess** by
default and **exactly** when you declare its encoding:

| Column | Read as | Written from |
| --- | --- | --- |
| undeclared (default) | valid UTF-8 → the string verbatim, otherwise a base64 string | a string → its UTF-8 bytes |
| `?types=cf:q=text` | the string verbatim | a string → its UTF-8 bytes |
| `?types=cf:q=bytes` | a base64 string | a base64 string → raw bytes (lossless for binary) |
| `?types=cf:q=int` / `long` | a number (4- / 8-byte big-endian, the HBase `Bytes` layout) | a whole number → those bytes |
| `?types=cf:q=double` | a number (8-byte IEEE-754) | a number → those bytes |
| `?types=cf:q=bool` | `true` / `false` (1 byte) | a bool → one byte |

The driver **never guesses** a numeric type from bytes (an 8-byte string is indistinguishable from a
`long`), it either *knows* (you declared it) or *does not guess* (text, else base64). Declared columns
round-trip losslessly in both directions. An undeclared column read back as base64 (non-UTF-8 bytes)
does **not** round-trip through a write, declare it `bytes` for that.

```sh { title='Declare the numeric columns so they read as numbers' }
iq add -n books 'hbase://localhost:2181/?table=iq_books&types=cf:year=long,cf:price=double'
```
```sh { title='Filter on a declared column, no tonumber needed' }
iq --src books '.[] | select(.cf.year > 2015) | .cf.title'
```

### Key encoding

The row key follows the same contract as a cell, text when it is valid UTF-8,
base64 otherwise, unless `?keytype=` declares its encoding (`&keytype=long`
reads and writes the 8-byte `Bytes` layout), so a declared key round-trips
losslessly.

### Pushdown

By default a `.[] | select(...)` filter's **column-equality** clauses are translated into an HBase
server-side filter (`SingleColumnValueFilter`, combined with `MustPassAll` for an `and`) so the
region servers filter before rows reach iq. The equality literal is encoded through the column's
declared type, so the comparison matches the stored bytes:

| `select(...)` clause | Pushed | HBase filter | Notes |
| --- | :---: | --- | --- |
| `.cf.q == x` | ✓ | `SingleColumnValueFilter(cf, q, =, x)` | a two-segment `family.qualifier` path, the literal must encode to the column's declared type (undeclared → string) |
| `E1 and E2` | ✓ | `FilterList(MustPassAll, …)` | drops any conjunct it cannot push (widening) |
| `.cf.q \| has`, ranges, regex, `length`, `!=`, `or` | — | — | run client-side, existence and `or` are not pushed because HBase has no clean superset-safe filter for them and ranges cannot reproduce jq's cross-type ordering |

A row-key point read (`.["1"]`) is a direct `Get`, not a scan. Pushdown never changes results, only
speed, the full jq always re-runs client-side, so a pushed filter is a conservative pre-filter. Pass
`--no-compile` to stream the whole table and filter entirely client-side, the cost is shown in
`--explain`.

### Raw commands

HBase has **no query language**, so `iq exec` is a small, safe verb set mapped straight onto RPC,
never a built query string, so it is injection-safe. Each verb names its own table. Reads: `get`,
`scan`, `count`, writes: `put`, `delete` (values encoded through the same declared-type contract):

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
stats and `--explain`, `iq data delete <table> <rowkey>…` is the typed, capability-gated per-key
delete that formalizes the raw `delete` verb above. The raw `put`/`delete` verbs remain the
lower-level raw path (a single cell, a column), mirroring the other drivers' raw paths. `iq
inspect` lists the source namespace's tables (`tables`).

## MongoDB

[MongoDB](https://www.mongodb.com) is a document database that stores
JSON-like documents in collections.

Register a `mongodb://` source (`mongodb+srv://` for SRV discovery) and the same jq
interface works against a collection, where **the collection is the keyspace, a document's `_id`
is the key and the document is the value**. The database comes from the URI path, the collection
from the URI's `?collection=` (the driver's own connection option, overridable per run with a dotted
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
`tonumber`, unlike Redis, where everything is a string. Documents normalize to JSON with the
same rules everywhere, an `ObjectID` becomes its hex string, a date becomes an RFC 3339 string,
numbers stay numbers, nested documents and arrays are preserved. A missing `_id` reads as `null`.
The `--unbounded` / streaming rules are identical to every backend (`.[]`-rooted filters stream a cursor
in constant memory, `keys`/`.`/`map` materialize and require the flag).

### Pushdown

By default a `.[] | select(...)` filter's **equality** clauses are translated into a native Mongo
query so the server does the filtering (and can use an index) before the documents ever reach iq:

```sh { title='Pushed down: the server filters by author' }
iq --src books '.[] | select(.author == "Robert C. Martin") | .title'
```
```sh { title='Forced client-side with --no-compile' }
iq --src books --no-compile '.[] | select(.author == "Robert C. Martin") | .title'
```

Pushdown never changes results, only speed, the full jq always re-runs client-side over whatever
comes back, so a pushed filter is only ever a conservative pre-filter. Pass `--no-compile` to skip
it and stream the whole collection, filtering entirely client-side. What it can push:

| `select(...)` clause | Pushed | MongoDB translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{a: x}` | number, string, bool, or null literal |
| `.a == 1 or .a == 2` | ✓ | `{a: {$in: [1, 2]}}` | an `or` of equalities on one field |
| `.a > n`, `>=`, `<`, `<=` | ✓ | native op + `$type` guards (an `$or`) | number/string literal, reproduces jq's cross-type order so the match is never a subset |
| `.a \| test("re")` | ✓ | `{a: {$regex: "re", $options: "is"}}` | portable patterns only (below), jq's `i` and `m` flags, with jq's `m` (dot-matches-newline) mapped to PCRE's `s` |
| `has("a")`, `.a \| has("k")` | ✓ | `{a: {$exists: true}}` | exact, key presence, like jq's `has()` |
| `.a \| length == n` | ✓ | `{$size: n}` + `$type` guards (an `$or`) | jq `length` is polymorphic (array/string/object/number), so guards keep it a superset |
| `.a \| any(cond)` | ✓ | `{a: {$elemMatch: cond}}` (an `$or` with an object guard) | an array element satisfying a pushable element predicate, `cond` can combine the rows above |
| `.a != x` | ✓ | `{$or: [{a: {$ne: x}}, {a: {$type: "array"}}]}` | exact negation of equality (the guard keeps arrays, which jq never equates to a scalar) |
| `has("a") \| not` | ✓ | `{a: {$exists: false}}` | exact negation of existence |
| `.a \| any(.f == v) \| not` | ✓ | `{a: {$not: {$elemMatch: …}}}` | no array element matches, the element condition must be exact equality |
| `E1 and E2`, `E1 or E2` | ✓ | `$and` / `$or` of the above | an `and` can push only its pushable parts and drop the rest |
| negated range/regex/`size` | — | — | their filters are supersets and a negated superset is a subset (unrecoverable) |
| `.a > true`, `.a < null` | — | — | a range against bool/null has no clean superset |
| non-portable regex | — | — | engine-specific construct (below) |
| anything else | — | — | runs client-side, as under `--no-compile` |

**Portable regex.** iq's jq is [gojq](https://github.com/itchyny/gojq), which compiles a `test()`
pattern with Go's RE2, MongoDB uses PCRE. A pattern is pushed only when every construct it uses
means the same, or a superset, in both, literals, anchors (`^` `$`), `.`, quantifiers
(`* + ? {n,m}`), alternation (`|`), groups, character classes, the ASCII `\d` `\w` `\s` `\D` `\W`
shorthands and word boundaries (`\b`, `\B`).

`\S` is the one shorthand held back: RE2's `\s` omits
the vertical tab that PCRE's `\s` matches, so RE2's `\S` matches a vertical tab PCRE's does not.
If pushed, it drops a document jq keeps (`\s` diverges the other way, a superset the client-side
re-run corrects).

Flags follow the same rule, gojq accepts only `i`, `m`, `g` and iq pushes `i`
(case-insensitive) and `m`, which in jq means "`.` matches newline" (dotall) and so maps to PCRE's
`s`, not PCRE's `m`.

A pattern using lookaround (`(?=…)`), backreferences (`\1`), unicode properties
(`\p{…}`), POSIX classes (`[[:…:]]`), or possessive quantifiers is not portable and stays
client-side, so the pushed set always equals jq's.

### Raw commands

`iq exec` runs a single JSON command document with `runCommand` and prints the reply as
JSON, the raw path for server-side queries, aggregation and administration:

```sh { title='Run a native find command' }
iq --src books exec '{"find":"books","filter":{"year":{"$gt":2015}}}'
```
```sh { title='Run an aggregation pipeline' }
iq --src books exec '{"aggregate":"books","pipeline":[{"$group":{"_id":null,"avg":{"$avg":"$price"}}}],"cursor":{}}'
```

`iq inspect` runs diagnostic database commands, `dbStats`, `serverStatus`, `listCollections`,
`collStats` (needs a collection, address it as `source.collection` or set `?collection=` on the
source URI), `buildInfo` and `hostInfo`, `--only` narrows to those subcommands.

## Neo4j

[Neo4j](https://neo4j.com) is a graph database of nodes and relationships,
queried with Cypher.

Register a `neo4j://` source and the same jq interface works against a node label, where **the node
label is the keyspace, a node's key is the key and the node is the value**. Neo4j has no single
keyspace, so a label is the addressable collection (like a Mongo collection or a Cassandra table):
the host is the bolt server, the label rides in the URI's `?label=` (overridable per run with a
dotted `handle.label`) and the database, Neo4j is multi-database, is `?database=` (default
`neo4j`). Use `neo4j+s://` (or `bolt://` for a single instance, `+s`/`+ssc` for TLS).

**Credentials
travel in the URI userinfo** (bolt basic auth), so `--store keyring` moves the password to the OS
keyring exactly as for the other backends.

**The key is the elementId by default, or a property you name with `?key=`.** `elementId(n)` is
always present and unique but opaque and not stable across database reloads, so a `?key=` property
(a stable, human-meaningful id) reads better, the value carries `_id` (the elementId) and `_labels`
alongside the node's properties, so identity survives whichever key you choose.

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

Neo4j values map to JSON directly, integers keep exact precision, bytes become base64 and temporal
and spatial values become their canonical ISO strings and `{x,y,srid}` objects. A scan pages the
label with keyset pagination ordered by `elementId(n)`. Because a `?key=` property is not guaranteed unique
(unlike a primary key), a scan falls back to a node's elementId whenever the key collides
within a page, so no node is ever silently dropped, a bounded `.["v"]` lookup that matches more than
one node is an error rather than an arbitrary pick.

### Relationship collections

A **relationship type** is an addressable collection too, so you can query a graph's edges the same
way. Name it with `?rel=KNOWS` on the source, or address one per run with the `:` marker
(`handle.:KNOWS`), a leading colon can never be a valid label, so it unambiguously selects a
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

**Relationship collections are read-only for now**: creating an edge needs endpoint
resolution, which nodes to connect and by which key, which is a further follow-up, so a copy or
`iq data` write into a relationship source is refused with a clear message. Write nodes with
`?label=`.

### Pushdown

By default a `.[] | select(...)` filter's **equality** and **existence** clauses are translated into
a Cypher `WHERE` clause (using dynamic `n[$prop]` access, so the property name is a parameter, never
string-built) so the server filters before nodes reach iq:

| `select(...)` clause | Pushed | Cypher | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `n[$p] = $v` | equality, a `null` literal becomes `n[$p] IS NULL` (a missing property) |
| `.a \| has` / `has("a")` | ✓ | `n[$p] IS NOT NULL` | key presence, exact |
| `has("a") \| not` | ✓ | `n[$p] IS NULL` | key absence, exact |
| `E1 and E2` | ✓ | `(… AND …)` | drops any conjunct it cannot push (widening) |
| `E1 or E2` | ✓ | `(… OR …)` | pushed only when **every** branch is pushable |
| `.a > x` / `.a <= x`, `!=`, `length`, regex, `any`, nested paths | — | — | run client-side: Cypher compares mismatched types as null rather than by jq's cross-type ordering and a nested path has no flat Neo4j property, so pushing these could wrongly exclude a node jq would keep |

Pushdown never changes results, only speed, the full jq always re-runs client-side, so a pushed
filter is a conservative pre-filter, `--explain` shows the `WHERE` clause and `--no-compile`
streams the whole label and filters entirely client-side.

### Writing

A copy into a Neo4j label upserts each node with `MERGE (n:Label {key}) SET n += props`, so a re-run
converges. **Writing needs a `?key=` property** (a MERGE key must be stable and the elementId is
server-assigned) **and a uniqueness constraint on it** (`CREATE CONSTRAINT ... REQUIRE n.<key> IS
UNIQUE`), without the constraint a MERGE can match and overwrite several nodes at once, so the
write is refused up front rather than fanning out.

`iq data clear` detach-deletes every node in the
label (and the relationships they hold), a label is not a droppable container, so `iq data drop` is
unsupported. Writes set node properties only, relationships are a follow-up.

### Raw commands

`iq exec` runs raw, parameterized [Cypher](https://neo4j.com/docs/cypher-manual/current/), the first
argument is the statement and an optional second argument is a JSON object of parameters (passed as
parameters, never string-built into the statement). It prints the result rows as JSON:

```sh { title='Run parameterized Cypher' }
iq --src graph exec 'MATCH (n:Person) WHERE n.age > $min RETURN n.name, n.age' '{"min": 40}'
```
```sh { title='Count the nodes' }
iq --src graph exec 'MATCH (n) RETURN count(n) AS nodes'
```

`iq inspect` reads deployment and schema metadata, `server` (components and version), `databases`
(the deployment's databases), `labels` (the addressable node labels), `reltypes` (relationship
types) and `constraints` (which shows the uniqueness constraint a `?key=` write needs), `--only`
narrows to those subcommands.

## Redis

[Redis](https://redis.io) is an in-memory key-value store used as a cache,
database and message broker.

Register a `redis://` source (`rediss://` for TLS) and the same jq interface works against the
Redis keyspace, where **a key maps directly to a Redis key and the value is whatever that key
holds**. The database index comes from the URI path (`/0`), every value is a string, so numeric
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

Other module types (time series, bloom, …) have no frozen encoding yet, a named read of one is
refused with a clear message.

### Pushdown

Redis has no server-side filtering, so a compiled predicate instead drives a **client-side
raw-byte prefilter**: on a streaming scan, each RedisJSON value is tested against the predicate on
its raw JSON.GET bytes and, when it provably cannot match, dropped before the (dominant) decode,
every other type is decoded and included unchanged. The full jq always re-runs client-side, so
output is identical with or without it, the prefilter only skips decoding documents the filter
rejects. `--no-compile` turns it off.

### Raw commands

`iq exec` forwards a command to the database verbatim and prints the reply in redis-cli style,
the raw path for writes, administration and seeding the jq read path does not cover:

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

Its output mirrors redis-cli's cooked style, bulk strings quoted, integers as `(integer) N`, a
missing value as `(nil)` and arrays as a numbered, indented list. The client uses RESP2 so
aggregate replies match redis-cli's classic flat output. Status replies such as `OK` and `PONG`
appear quoted, a limitation of the underlying client, which does not distinguish them from bulk
strings.

`iq inspect` runs `INFO`, `--only` narrows it to sections (`server`, `clients`, `memory`,
`persistence`, `stats`, `replication`, `cpu`, `keyspace`) and none runs the full `INFO`.

## File dumps

A `file://` source reads a database dump straight from disk, so a snapshot is queried, inspected
for shape, diffed and restored with the same jq interface, **no running server**. It is
read-only, a `file://` endpoint is never a copy *destination* and `iq exec`/`iq inspect` (which
need a live server) do not apply.

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
`file:///d.bin?format=bson`). A gzipped dump is unwrapped automatically, a gzipped dump must
pass `?format=` because its content is not sniffable through the compression. On Windows a drive
path takes the `file:///C:/path/to/dump.json` form (forward slashes, three slashes before the
drive letter).

| Format | Produced by | Notes |
| --- | --- | --- |
| Typed JSONL | `iq --src <s> --typed -o <file>` | iq's own dump, lossless round-trip |
| Typed YAML | `iq --src <s> --typed -y -o <file>` | the same records as YAML documents, auto-detected by a `.yaml`/`.yml` name, else `?format=yaml` |
| Redis RDB | `redis-cli --rdb`, `SAVE` | values match a live scan, RDB ≤ v12 (Redis ≤ 7.2) |
| Mongo BSON | `mongodump` | single `.bson` file |
| Mongo Extended JSON | `mongoexport` | one document per line, or a `--jsonArray` array |
| DynamoDB JSON | S3 `export-table-to-point-in-time`, `aws dynamodb scan` | needs `?format=dynamodb-json` and a `?keys=pk[:S][,sk[:N]]` key schema (a dump carries items but not the table's key schema), export files are gzipped NDJSON |
| Cassandra CSV | `cqlsh COPY … TO 'f.csv'` | needs `?format=cassandra-csv`, `?keys=col1[,col2]` naming the primary-key columns and `?types=col=cqltype,…` for the non-text columns (COPY writes every value as text), column names come from a `WITH HEADER=TRUE` row, else `?columns=`, scalar columns only |
| Neo4j APOC JSON | `CALL apoc.export.json.all('g.json',{})` | needs `?format=neo4j-json` and a keyspace selector, either `?label=<Label>` for its nodes or `?rel=<Type>` for its relationships (a dump holds the whole graph), JSON Lines or `ARRAY_JSON`, same `_id`/`_labels`/`_type`/`_start`/`_end` envelope as the live driver, `?key=<prop>` to key by a property, `_id` is APOC's numeric export id, not the live elementId, the binary `neo4j-admin database dump` and APOC's `useTypes`/`JSON_ID_AS_KEYS` variants are not supported |

The whole dump streams, a `file://` source never holds all values in memory (whole-dataset
materialization is the core's, gated by `--unbounded`, exactly as for a live backend). **Restore
fidelity** is the record round-trip, values and native types reconstruct, but TTLs, exact
encodings, stream consumer groups, RDB module types and Mongo indexes do not carry.

**Neo4j record ids.** A dump's `_id` and a relationship's `_start`/`_end`, is APOC's numeric
export id, not the live driver's `elementId`, because APOC's default export does not write
elementIds. Within one dump the ids are self-consistent, a relationship's `_start`/`_end` reference
the same ids its nodes carry as `_id`, so `?rel=` endpoints resolve against the default-keyed
`?label=` nodes exactly as they do live.

Two things the numeric id cannot do, both by nature, it
does not match the `_id` of the same node read from the live source (different id schemes) and it
is not stable across re-exports (Neo4j reuses a deleted node's id, the reason `id()` is deprecated
in favor of `elementId()`).

For an identifier that is stable and identical across a live source and
its dump, key on a business property with `?key=<prop>`, it reads the same value everywhere.

**Decode cache.** Re-querying the same large dump re-parses it every time, so iq caches the
decoded, normalized records of a scanned dump above 4 MiB and reads them back on later queries,
skipping the RDB/BSON/JSON decode (a warm scan of an 8 MiB dump runs several times faster).

The
cache lives under `<user cache dir>/iq/dumps`, keys on the dump's path, size and mtime, so
editing the dump invalidates it automatically and needs no action from you, a stale or absent cache only
means a full decode, never a wrong or failed query.

Only full scans populate it (a bounded
key read does not) and stdin is never cached.

Alongside the records, a scan writes a **per-page key index** (a Bloom filter per page), so a
later bounded read (`iq --src snap '.["id"]'`) decodes only the pages that can hold a wanted key
instead of streaming the whole cache, a point lookup or a missing-key check stays fast even on a
huge dump. The index is on by default and distribution-agnostic (it hashes keys, so random
ids/UUIDs are fine). Skip it with `--no-cache-index` (the flat cache is still written, a bounded
read only streams it) when a very large keyspace makes the index build memory unwelcome.

Manage the cache with `iq cache`, bypass it for one run with `--no-cache` :material-earth:{ title="Global flag" }, or set a default
with `iq config set no-cache true` / `iq config set no-cache-index true` (`--no-cache-index` :material-earth:{ title="Global flag" }
too).

- `iq cache location`: print the cache directory path.
- `iq cache stat [-j/--json | -y/--yaml]`: list cached dumps with their sizes.
- `iq cache clear [<source>|<path>]`: remove all cached dumps, or only one source's/path's.

**Prefilter.** A file source pushes no filter to a server (there is none), but on a streaming
scan of an **uncached typed-JSONL** dump a compiled predicate drives a **client-side raw-byte
prefilter**: each record's raw `value` bytes are tested against the predicate and a provable
non-match is dropped before it is decoded, so the dominant JSON decode is skipped for records the
filter rejects.

Every other format (YAML, RDB, BSON, Extended JSON, DynamoDB JSON, Cassandra
CSV, Neo4j APOC) and any scan served from a fresh decode cache (whose bytes are already-decoded
CBOR and already fast to stream), decodes in full and lets the client filter.

The full jq always
re-runs client-side, so output is identical with or without the prefilter, it only skips decoding
dropped records, a prefiltered scan deliberately does not populate the decode cache (to populate
it, the scan must decode everything). `--no-compile` turns it off.
