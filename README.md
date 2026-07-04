# iq

A Go command-line tool that runs [jq](https://jqlang.github.io/jq/) filters against NoSQL
databases. Redis and MongoDB are supported; the backend is chosen by the URL scheme, and the
query core is driver-agnostic so further backends slot in behind the same port.

The filter is both the transform and the key selector: its top-level paths name the keys to
fetch, so the store only ever reads a bounded set of keys — never a full keyspace scan, unless
you ask for one explicitly. Fetched values are normalized to JSON and the filter then runs
entirely client-side, so its semantics are identical for every backend.

## Requirements

- Go 1.25+
- Docker (optional, for the local Redis used by integration tests)

## Build

```bash
go build -o iq .
```

## Usage

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as JSON:

```bash
./iq '.greeting'                          # fetch key "greeting"
./iq '.["book:1"]'                        # a key containing a colon needs bracket-quoting
./iq '.["book:1"].title'                  # fetch book:1, extract one field
./iq '[ .["book:1"].title, .["book:2"].title ]'   # fetch both keys, project a field from each
./iq '.["book:2"].price | tonumber + 5'   # values are strings; convert before arithmetic
```

Always wrap the filter in single quotes. jq syntax is full of characters the shell would
otherwise expand or split — brackets (`[ ]`), whitespace, `|`, `*`, `$` — and bracket-quoting a
colon key like `.["book:1"]` reads as a glob to zsh (`no matches found`) or bash unless quoted.

### Bounded reads, streaming scans, and materialized scans

`iq` fetches exactly the keys your filter names, so a normal query's cost is bounded by the keys
you asked for, never by the size of the database. A filter that needs the whole keyspace is a
**scan**, and scans come in two kinds:

- **Streaming** — a filter rooted at `.[]` (`.[]`, `.[] | select(...)`, `.[].title`) processes
  each value independently, so `iq` walks the keyspace in pages and runs the filter page by page,
  emitting as it goes. Memory stays constant and results appear progressively (interrupt with
  Ctrl-C or bound with `--timeout`). These run **without a flag**:

  ```bash
  ./iq '.[] | objects | select((.year|tonumber) > 2015) | .title'   # streamed discovery
  ```

- **Materialized** — a filter that collapses the collection into one value (`.`, `keys`, `length`,
  `map(...)`, `group_by`, `sort_by`, aggregates) must load the whole dataset into memory. It runs
  only with `--unbounded`:

  ```bash
  ./iq 'keys'                 # error: requires materializing the whole dataset
  ./iq --unbounded 'keys'     # list every key
  ./iq --unbounded '.'        # the whole dataset as one JSON object
  ```

`--unbounded` means "permit loading the whole dataset into memory." Passing it on a streaming
filter is allowed too: it switches that filter from batched streaming to a single materialized
pass, giving key-sorted output and a consistent snapshot instead of scan order.

Streamed output is **best-effort**: values arrive in scan order (not key-sorted), and an element
may repeat if the keyspace is resized mid-scan — the price of never holding more than one page.
Use `--unbounded` when you need sorted, exactly-once output.

The flag names the cost property (loading everything), not any one store's mechanism, so it will
mean the same thing for future backends (a Cassandra full scan, a CouchDB `_all_docs`).

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

`iq raw` forwards a command to the database verbatim and prints the reply in redis-cli style —
the escape hatch for writes, administration, and seeding the jq read path does not cover:

```bash
./iq raw SET greeting hello   # "OK"
./iq raw GET greeting         # "hello"
./iq raw INCR counter         # (integer) 1
./iq raw GET missing          # (nil)
```

Its output mirrors redis-cli's cooked style: bulk strings quoted, integers as `(integer) N`, a
missing value as `(nil)`, and arrays as a numbered, indented list. The client uses RESP2 so
aggregate replies match redis-cli's classic flat output. Status replies such as `OK` and `PONG`
appear quoted, a limitation of the underlying client, which does not distinguish them from bulk
strings.

## MongoDB

The backend is chosen by the URL scheme. Point `--url` at a `mongodb://` server and the same jq
interface works against a collection, where **the collection is the keyspace: a document's `_id`
is the key and the document is the value**. The database comes from the URI path; the collection
from `--collection`/`-c`:

```bash
iq -u mongodb://localhost:27017/iq -c books '.["2"]'                 # fetch document _id "2"
iq -u mongodb://localhost:27017/iq -c books '.[] | select(.year > 2015) | .title'   # streamed
iq -u mongodb://localhost:27017/iq -c books --unbounded 'keys'       # every _id
```

Because Mongo values are natively typed, numeric comparisons like `.year > 2015` need no
`tonumber` — unlike Redis, where everything is a string. Documents normalize to JSON with the
same rules everywhere: an `ObjectID` becomes its hex string, a date becomes an RFC 3339 string,
numbers stay numbers, nested documents and arrays are preserved. A missing `_id` reads as `null`.
The `--unbounded` / streaming rules are identical to Redis (`.[]`-rooted filters stream a cursor
in constant memory; `keys`/`.`/`map` materialize and require the flag).

### Server-side pre-filtering with `--compile`

By default a `.[] | select(...)` filter streams the whole collection and filters client-side. With
`--compile`, the `select` predicate's **equality** clauses are translated into a native Mongo query
so the server does the filtering (and can use an index):

```bash
iq -u mongodb://localhost:27017/iq -c books --compile '.[] | select(.author == "Robert C. Martin") | .title'
```

`--compile` never changes results, only speed: the full jq always re-runs client-side over whatever
comes back, so a pushed filter is only ever a conservative pre-filter. What it can push:

| `select(...)` clause | Pushed | MongoDB translation | Notes |
| --- | :---: | --- | --- |
| `.a == x` | ✓ | `{a: x}` | number, string, bool, or null literal |
| `.a == 1 or .a == 2` | ✓ | `{a: {$in: [1, 2]}}` | an `or` of equalities on one field |
| `.a > n`, `>=`, `<`, `<=` | ✓ | native op + `$type` guards (an `$or`) | number/string literal; reproduces jq's cross-type order so the match is never a subset |
| `.a \| test("re")` | ✓ | `{a: {$regex: "re", $options: "ims"}}` | portable patterns only (below); `i`/`m`/`s` flags |
| `has("a")`, `.a \| has("k")` | ✓ | `{a: {$exists: true}}` | exact — key presence, like jq's `has()` |
| `.a \| length == n` | ✓ | `{$size: n}` + `$type` guards (an `$or`) | jq `length` is polymorphic (array/string/object/number), so guards keep it a superset |
| `E1 and E2`, `E1 or E2` | ✓ | `$and` / `$or` of the above | an `and` may push only its pushable parts and drop the rest |
| `.a != x` | — | — | jq and Mongo `$ne` differ on arrays/types |
| `.a > true`, `.a < null` | — | — | a range against bool/null has no clean superset |
| non-portable regex | — | — | engine-specific construct (below) |
| anything else | — | — | runs client-side, as without `--compile` |

**Portable regex.** jq uses the Oniguruma engine, MongoDB uses PCRE. A pattern is pushed only when
every construct it uses means the same in both: literals, anchors (`^` `$`), `.`, quantifiers
(`* + ? {n,m}`), alternation (`|`), groups, character classes, and the ASCII `\d` `\w` `\s`
shorthands (and their negations, `\b`, `\B`). A pattern using lookaround (`(?=…)`), backreferences
(`\1`), unicode properties (`\p{…}`), POSIX classes (`[[:…:]]`), or possessive quantifiers is not
portable and stays client-side, so the pushed set always equals jq's.

On Redis, or for any filter with no pushable predicate, `--compile` is a harmless no-op.

`iq raw` on MongoDB runs a single JSON command document with `runCommand` and prints the reply as
JSON — the escape hatch for server-side queries, aggregation, and administration:

```bash
iq -u mongodb://localhost:27017/iq raw '{"find":"books","filter":{"year":{"$gt":2015}}}'
iq -u mongodb://localhost:27017/iq raw '{"aggregate":"books","pipeline":[{"$group":{"_id":null,"avg":{"$avg":"$price"}}}],"cursor":{}}'
```

### Connection

The database is addressed with a connection URL whose scheme selects the backend —
`redis://[user:pass@]host:port[/db]` (`rediss://` for TLS) or `mongodb://host:port/db`
(`mongodb+srv://` too) — resolved in this order:

1. the `--url` / `-u` flag
2. the `IQ_URL` environment variable (or `IQ_REDIS_URL`, kept for back-compat)
3. the default `redis://localhost:6379/0`

`--collection` / `-c` selects the MongoDB collection (ignored for Redis). `--timeout` (default
`5s`) bounds each query.

## Architecture

The query core is driver-agnostic and lives behind two ports a backend adapter implements:

- `internal/selector` — pure static analysis. `Keys` walks a parsed jq AST and classifies the
  filter: a **bounded** set of named keys, or a **scan** — and, for a scan, whether it is
  **streamable** (`.[]`-rooted, distributes over the keyspace page by page) or holistic. It
  depends only on the jq library, never on a driver.
- `internal/query` — the use cases. `JQEngine` parses and classifies the filter, then routes:
  bounded → `Get` the named keys; streamable scan → run the filter over each `ScanBatches` page and
  emit; holistic scan → merge the pages and run once (only when the caller permits it). `Runner`
  is the raw-command use case behind the `Store` port.
- `internal/redis`, `internal/mongo` — the adapters. Each has one `*Store` satisfying both ports:
  `Query` (raw) and `Get`/`ScanBatches` (jq), with a type-to-JSON normalization frozen as that
  backend's encoding contract (Redis types; BSON → `ObjectID`-hex, dates, nested docs). Redis maps
  a key to a Redis key; Mongo maps a key to a document `_id` within `--collection`.
- `cmd` — the CLI adapter and composition root. It picks the adapter by URL scheme (`openStore`),
  runs the jq action or the `raw` escape hatch, and formats output (JSON for jq; per-backend for
  raw — redis-cli style for Redis, JSON for Mongo), keeping the core free of any output format.

A bounded filter runs client-side over just the named keys, so its cost is `O(keys requested)`; a
streamable scan runs in `O(page)` memory. The jq semantics are identical for any future backend
behind `KVStore`. jq is provided by
[gojq](https://github.com/itchyny/gojq) (pure Go, no cgo), which keeps `iq` a single static
binary and exposes the AST the key selector walks.

## Common commands

```bash
go build -o iq .          # build the binary
go test -short ./...      # fast unit tests, no external services
docker compose up -d --wait   # start local Redis + MongoDB (:6379, :27017)
bash scripts/seed.sh      # load example data into the running Redis
bash scripts/seed-mongo.sh    # load example documents into the running MongoDB
go test ./...             # full suite, including Redis + MongoDB integration tests
docker compose down       # stop the local services
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate (run with services up; fails on any survivor/timeout)
```

Integration tests skip under `go test -short`; the full `go test ./...` needs Redis and MongoDB
up (via `docker compose up`) and connects to `IQ_REDIS_URL` / `IQ_MONGO_URL` or the local defaults.
