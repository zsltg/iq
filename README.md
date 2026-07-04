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
- Docker (for the integration tests, which start ephemeral Redis + MongoDB containers; not needed for `go test -short`)

## Build

```bash
go build -o iq .          # plain build
make build                # build with version metadata embedded
```

`make build` injects the version, commit, and build date via ldflags; a plain `go build` still
reports a version recovered from Go's embedded build info. Check it with `iq version` or
`iq --version`.

## Releasing

Versioning is driven by [Conventional Commits](https://www.conventionalcommits.org/): the release
tooling reads the commit log, computes the next [semantic version](https://semver.org/), and
regenerates `CHANGELOG.md`. It is all-Go and local — no CI service or GitHub required.

```bash
make tools                        # one-time: install svu + git-chglog into GOPATH/bin
bash scripts/release.sh --dry-run # preview the next version and CHANGELOG.md diff, no changes
make release                      # bump, regenerate CHANGELOG.md, commit, and tag
git push --follow-tags            # publish the tag (release.sh never pushes for you)
```

`make release` must run on a clean `main`. `svu` picks the bump from the commit types since the
last tag (`feat` → minor, `fix` → patch, a `!`/`BREAKING CHANGE` → major); with no tags yet the
first release comes out as `v0.1.0`.

## Sources

`iq` connects only through **saved sources**: a named connection you register once, then select
by name or as the default. Register one with `iq add`, then make it active:

```bash
iq add cache redis://localhost:6379/0                # register a Redis source
iq add books mongodb://localhost:27017/iq -c books   # a Mongo source; -c names the collection
iq src cache                                          # make "cache" the active source
iq ls                                                 # list sources (the active one marked *)
```

Once a source is active, every query runs against it. Select a different source for a single
command with `--src`/`-s`, without changing the active one:

```bash
iq --src books '.["2"]'      # run this one query against "books"
```

- `iq add <name> <url> [-c <collection>] [--store keyring]` — register a source. The backend is
  inferred from the URL scheme (`redis://`, `rediss://`, `mongodb://`, `mongodb+srv://`). `-c`
  stores a MongoDB collection with the source. `--store keyring` moves the URL's password into
  the OS keyring and strips it from the stored URL (default `--store inline` keeps it in the
  config file).
- `iq ls` — list saved sources; the active one is marked `*`. Passwords in URLs are redacted.
- `iq src [<name>]` — show the active source, or set it.
- `iq rm <name>...` — remove one or more sources, or whole groups (a group name removes every
  source under it). Atomic: if any name is unknown, nothing is removed.
- `iq mv <old> <new>` — rename a source, or move it into a group with a group-qualified target
  (`iq mv books prod/books`). When `<old>` is a group, every member is re-prefixed
  (`iq mv prod staging`). The active source and group follow the move.
- `iq group [<name>] [--clear]` — show, set, or clear the active **group**.

**Groups.** A `/` in a name groups sources (`prod/books`, `dev/books`). Set an active group with
`iq group prod`, and an unqualified name resolves inside it — `iq src books` then selects
`prod/books`, falling back to a top-level `books` if the group has none.

Sources live in a TOML file at `<os user config dir>/iq/iq.toml` (e.g. `~/.config/iq/iq.toml`),
written `0600` because a URL may carry a password. Override the path with `IQ_CONFIG`. A source
added with `--store keyring` keeps no password in this file — it lives in the OS keyring (Secret
Service on Linux, Keychain on macOS, Credential Manager on Windows) and is spliced back into the
URL only when connecting.

> `iq add` shadows jq's built-in `add` filter at the top level. To sum with jq, write it inside a
> larger expression, e.g. `iq '[ .a, .b ] | add'`.

## Usage

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as pretty JSON by default (see [Output formats](#output-formats) to change it; these run
against the active source — see [Sources](#sources)):

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

### Output formats

Results print as pretty JSON by default. `--format`/`-o` selects another rendering; it applies
to the jq read path and to `--from`/`--combine`, not to `exec` (which prints the backend's
native reply):

| `-o` value | output |
| --- | --- |
| `json` (default) | pretty JSON, one value per result |
| `jsonl` | compact JSON, one value per line (JSON Lines) |
| `json-array` | every result wrapped in one `[ ... ]` document |
| `values` | scalars unquoted, one per line; objects and arrays fall back to compact JSON |
| `yaml` | YAML documents, separated by `---` |

```bash
./iq '.[].title' -o values     # bare titles, one per line, for shell substitution
./iq '.[]' -o jsonl            # one compact document per line
./iq '.[]' -o json-array       # a single JSON array of every result
./iq '.[]' -o yaml             # YAML, easier to read for deeply nested documents
```

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

## MongoDB

The backend is chosen by the source's URL scheme. Register a `mongodb://` source and the same jq
interface works against a collection, where **the collection is the keyspace: a document's `_id`
is the key and the document is the value**. The database comes from the URI path; the collection
from the source's `-c` (overridable per run with `--collection`/`-c`):

```bash
iq add books mongodb://localhost:27017/iq -c books   # register once, then:
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

### Server-side pre-filtering with `--compile`

By default a `.[] | select(...)` filter streams the whole collection and filters client-side. With
`--compile`, the `select` predicate's **equality** clauses are translated into a native Mongo query
so the server does the filtering (and can use an index):

```bash
iq --src books --compile '.[] | select(.author == "Robert C. Martin") | .title'
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
| `.a \| any(cond)` | ✓ | `{a: {$elemMatch: cond}}` (an `$or` with an object guard) | an array element satisfying a pushable element predicate; `cond` may combine the rows above |
| `.a != x` | ✓ | `{$or: [{a: {$ne: x}}, {a: {$type: "array"}}]}` | exact negation of equality (the guard keeps arrays, which jq never equates to a scalar) |
| `has("a") \| not` | ✓ | `{a: {$exists: false}}` | exact negation of existence |
| `.a \| any(.f == v) \| not` | ✓ | `{a: {$not: {$elemMatch: …}}}` | no array element matches — the element condition must be exact equality |
| `E1 and E2`, `E1 or E2` | ✓ | `$and` / `$or` of the above | an `and` may push only its pushable parts and drop the rest |
| negated range/regex/`size` | — | — | their filters are supersets, and a negated superset is a subset (unrecoverable) |
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

`iq exec` on MongoDB runs a single JSON command document with `runCommand` and prints the reply as
JSON — the escape hatch for server-side queries, aggregation, and administration:

```bash
iq --src books exec '{"find":"books","filter":{"year":{"$gt":2015}}}'
iq --src books exec '{"aggregate":"books","pipeline":[{"$group":{"_id":null,"avg":{"$avg":"$price"}}}],"cursor":{}}'
```

### Connection

The database is a saved [source](#sources) — a connection URL whose scheme selects the backend,
`redis://[user:pass@]host:port[/db]` (`rediss://` for TLS) or `mongodb://host:port/db`
(`mongodb+srv://` too). A query resolves its source in this order:

1. the `--src` / `-s` flag
2. the active source (`iq src <name>`)

With no source selected the command errors — there is no ambient URL or environment fallback.
`--collection` / `-c` overrides the source's MongoDB collection for one run (ignored for Redis);
`--timeout` (default `5s`) bounds each query.

## Cross-source queries

`--from` and `--combine` run one query across several sources and stitch the results together.
Each `--from name='<jq>'` reduces a source *at the source* — bounded reads, streaming scans, and
`--compile` pushdown all still apply — and binds its result set to `$name`; `--combine '<jq>'` then
runs over those variables. Nothing copies whole datasets: each source returns only what its jq keeps.

```bash
# join users with orders on a shared id, across two sources
iq --from users='.[] | {id, name}' \
   --from orders='.[] | select(.total > 99)' \
   --combine '($users | INDEX(.id)) as $u | $orders[] | . + {name: $u[.userId].name}'
```

Each `--from` names a saved source (the same handles as `iq ls`, group-namespaced), so it resolves
through the registry exactly like `--src`. The bound variable is the source name with `/`, `.`, or
`-` replaced by `_`, so `prod/books` binds `$prod_books`. `--combine` is a plain jq program, so it
can join, union (`$a + $b`), aggregate, or fan across any number of sources.

**Reduce, then combine.** Each `--from` stage is evaluated independently and its (already reduced)
result is held in memory before `--combine` runs — so keep a stage's output small with
`select`/projection/aggregation. A stage that must materialize its whole source (`keys`, `.`,
`map(...)`) still needs `--unbounded`, exactly like a single-source query; a `.[]`-rooted stage
streams without it. Stages do not see each other's data, so a lookup whose keys depend on another
source's rows is not expressible here — reduce both sources and join them in `--combine`.

### Composing in one filter with `source()`

When a lookup's keys depend on another source's rows — or you just want to compose several sources
in one expression — call `source()` directly inside the filter:

```bash
# join users and orders in a single filter (no active source needed)
iq 'INDEX(source("users"; ".[]"); .id) as $u
    | source("orders"; ".[] | select(.total > 99)")
    | {name: $u[.userId].name, total}'
```

`source("name"; "<jq>")` runs `<jq>` against source `name` (reduced, streamed, and `--compile`-pushed
like any query) and **yields its results as a stream**; a one-argument `source("name")` yields the
whole source. Both arguments are **strings**, so the sub-filter is quoted — inside the single-quoted
outer filter that means double quotes, `source("orders"; ".[] | select(.x)")`. Because `source()`
yields a stream, collect it before indexing: `INDEX(source(…); .id)` or `[source(…)]`, not
`source(…) | INDEX(.id)`.

A filter that calls `source()` runs over a **null input**: every read is an explicit `source()` call
and there is no implicit primary source, so it needs no active source. Names resolve through the
registry like `--src`, active-group namespacing included.

**Correlated lookups re-run.** A `source()` opened inside a stream runs its sub-filter once per
element (the connection is reused, but the sub-filter re-executes). Hoist a constant lookup into a
binding — `INDEX(source("users"; ".[]"); .id) as $u | …` — and index `$u` per element instead.

### Choosing `--from`/`--combine` vs `source()`

Both reduce per source then combine; pick by what the query needs.

| | `--from` / `--combine` | in-filter `source()` |
| --- | --- | --- |
| Shape | explicit flags: a stage per source, then one combine | a single jq filter |
| Correlated reads (B keyed by A's rows) | ✗ stages are independent | ✓ nest `source()` |
| Quoting | each stage is its own flag value | sub-filter is a quoted string inside the filter |
| Memory | each reduced result held until combine | same, plus a correlated `source()` re-runs per row |

Reach for `--from`/`--combine` for a straightforward join, union, or aggregate across a few sources;
reach for `source()` when a read depends on another source's values, or to keep everything in one
composable filter.

## Architecture

The query core is driver-agnostic and lives behind two ports a backend adapter implements:

- `internal/selector` — pure static analysis. `Keys` walks a parsed jq AST and classifies the
  filter: a **bounded** set of named keys, or a **scan** — and, for a scan, whether it is
  **streamable** (`.[]`-rooted, distributes over the keyspace page by page) or holistic. It
  depends only on the jq library, never on a driver.
- `internal/query` — the use cases. `JQEngine` parses and classifies the filter, then routes:
  bounded → `Get` the named keys; streamable scan → run the filter over each `ScanBatches` page and
  emit; holistic scan → merge the pages and run once (only when the caller permits it). `Runner`
  is the native-command use case behind the `Store` port (surfaced as `iq exec`); `Combiner` runs a cross-source `--combine`
  program over the reduced per-source results bound as variables, and `CrossEngine` runs a
  `source()`-driven filter over a null input — both reach other sources through the `SourceOpener`
  port and hold no primary store.
- `internal/redis`, `internal/mongo` — the adapters. Each has one `*Store` satisfying both ports:
  `Query` (exec) and `Get`/`ScanBatches` (jq), with a type-to-JSON normalization frozen as that
  backend's encoding contract (Redis types; BSON → `ObjectID`-hex, dates, nested docs). Redis maps
  a key to a Redis key; Mongo maps a key to a document `_id` within `--collection`.
- `internal/config` — the saved sources. A small TOML store (named connections keyed by handle,
  plus the active source and group) the CLI reads to resolve a query's connection. It stays
  driver-agnostic: the backend is inferred from a source's URL scheme, validated in `cmd`. It
  records only that a source is keyring-backed; the password itself never enters this store.
- `internal/secret` — the credential port. A `Keyring` interface over the OS secret store, so a
  keyring-backed source keeps its password out of the config file; `cmd` splices it back into the
  URL at connect time.
- `cmd` — the CLI adapter and composition root. It resolves the selected source (`--src` or the
  active source) to a URL and collection, picks the adapter by URL scheme (`openStore`), runs the
  jq action (routing a `source()`-driven filter to the cross-source engine), the `exec` escape hatch,
  a source command (`add`/`ls`/`rm`/`src`/`group`), or a `--from`/`--combine` cross-source query —
  resolving every source name through the same registry — and formats output (a `--format`-selected
  renderer for the jq path — json, jsonl, json-array, values, or yaml; per-backend for `exec` —
  redis-cli style for Redis, JSON for Mongo), keeping the core free of any output format.

A bounded filter runs client-side over just the named keys, so its cost is `O(keys requested)`; a
streamable scan runs in `O(page)` memory. The jq semantics are identical for any future backend
behind `KVStore`. jq is provided by
[gojq](https://github.com/itchyny/gojq) (pure Go, no cgo), which keeps `iq` a single static
binary and exposes the AST the key selector walks.

## Common commands

```bash
go build -o iq .          # build the binary
make build                # build with version metadata embedded
iq version                # print version, commit, build date, and Go version
go test -short ./...      # fast unit tests, no external services
go test ./...             # full suite; starts ephemeral Redis + MongoDB via testcontainers-go
docker compose up -d --wait   # optional: local Redis + MongoDB for manual exploration (:6379, :27017)
bash scripts/seed.sh      # load example data into the running Redis
bash scripts/seed-mongo.sh    # load example documents into the running MongoDB
docker compose down       # stop the local services
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate (fails on any survivor/timeout); set IQ_*_URL to a pre-started stack
make tools                # install release tools (svu, git-chglog) into GOPATH/bin
make version              # print the version the next release would take
bash scripts/release.sh --dry-run   # preview the next release without changing anything
make release              # bump version, regenerate CHANGELOG.md, commit, and tag
```

Integration tests skip under `go test -short`. The full `go test ./...` needs Docker: it starts
an ephemeral Redis and MongoDB via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) on random ports and
tears them down afterwards — no manual `docker compose up`. Set `IQ_REDIS_URL` / `IQ_MONGO_URL`
to point at an already-running server (for example the `docker compose` stack) to skip container
startup; the mutation gate, which reruns the suite per mutant, wants this to avoid churn.
