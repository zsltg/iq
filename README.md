# iq

A Go command-line tool that runs [jq](https://jqlang.github.io/jq/) filters against NoSQL
databases. Redis, MongoDB, Apache Cassandra, Amazon DynamoDB, Apache HBase, Apache CouchDB,
Couchbase, Neo4j, Elasticsearch, and OpenSearch are supported; the
backend is chosen by the URL scheme, and the query core is driver-agnostic so further backends slot
in behind the same port.

The filter is both the transform and the key selector: its top-level paths name the keys to
fetch, so the store only ever reads a bounded set of keys — never a full keyspace scan, unless
you ask for one explicitly. Fetched values are normalized to JSON and the filter then runs
entirely client-side, so its semantics are identical for every backend.

`iq` is inspired by [sq](https://github.com/neilotoole/sq): much of its command surface — the
`<source>.<collection>` addressing along with many subcommands and flags — deliberately follows
sq's so the tool feels familiar.

> [!WARNING]
> **Not production-ready.** `iq` has potential rough edges — don't rely on it for critical work
> yet.

## Requirements

- Go 1.26+
- Docker (for the integration tests, which start ephemeral Redis + MongoDB + Cassandra + DynamoDB Local + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch containers; not needed for `go test -short`. HBase integration tests run only against a `docker compose` cluster named by `IQ_HBASE_URL`)

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
iq add -n cache redis://localhost:6379/0                    # register a Redis source as "cache"
iq add -a 'mongodb://localhost:27017/books?collection=items' # a Mongo source; handle "books" from the db, made active
iq add -n orders 'cassandra://localhost:9042/shop?table=orders' # a Cassandra source
iq add -n books 'hbase://localhost:2181/?table=iq_books'     # an HBase source (host = ZooKeeper quorum)
iq add -n docs 'couchdb://admin:pass@localhost:5984/?database=iq' # a CouchDB source (host = server)
iq add -n cb 'couchbase://Administrator:pass@localhost/?bucket=iq' # a Couchbase source (host = cluster)
iq add -n graph 'neo4j://neo4j:pass@localhost:7687/?label=Person&key=id' # a Neo4j source (label = keyspace)
iq add -n docs 'elasticsearch://localhost:9200/?index=books' # an Elasticsearch source (index = keyspace)
iq add -n logs 'opensearch://localhost:9201/?index=books'   # an OpenSearch source (same driver)
iq src cache                                                 # make "cache" the active source
iq ls                                                        # list sources: handle driver url (active marked *); -v adds format + options
```

Once a source is active, every query runs against it. Select a different source for a single
command with `--src`/`-s`, without changing the active one; address a MongoDB collection or a
Cassandra table with a dotted `handle.collection` / `handle.table` suffix:

```bash
iq --src books '.["2"]'          # run this one query against "books" (its default collection)
iq --src books.authors '.[]'     # the same connection, a different collection
```

- `iq add <url> [-n <handle>] [-a] [-p] [-d <driver>] [--skip-verify] [--store keyring]`
  — register a source, mirroring `sq add`. The URL is the sole positional argument; the backend is
  inferred from its scheme (`redis://`, `rediss://`, `mongodb://`, `mongodb+srv://`, `cassandra://`,
  `dynamodb://`, `hbase://`, `couchdb://`, `couchdbs://`, `couchbase://`, `couchbases://`,
  `neo4j://`, `neo4j+s://`, `neo4j+ssc://`,
  `bolt://`, `bolt+s://`, `bolt+ssc://`, `elasticsearch://`, `elasticsearch+s://`, `opensearch://`,
  `opensearch+s://`).
  `-n`/`--handle` names the source; when omitted a handle is derived from the URL (the MongoDB
  database or Cassandra keyspace name, else the driver, disambiguated with a numeric suffix on
  collision). A MongoDB default collection rides in the URL as `?collection=`
  (`mongodb://host/db?collection=items`), a Cassandra default table as `?table=`
  (`cassandra://host/keyspace?table=orders`), a DynamoDB default table as `?table=`
  (`dynamodb://us-east-1/?table=orders`, the region as the host; credentials come from the AWS
  default chain, never the URL), an HBase default table as `?table=`
  (`hbase://host:2181/?table=books`, the host as the ZooKeeper quorum; cell encodings optionally
  declared with `?types=cf:age=long`), a CouchDB default database as `?database=`
  (`couchdb://host:5984/?database=books`, the host as the server), a Couchbase bucket as `?bucket=`
  with an optional `[scope.]collection` as `?collection=`
  (`couchbase://host/?bucket=iq&collection=sales.orders`, the host as the cluster), a Neo4j default node label as
  `?label=` (`neo4j://host:7687/?label=Person&key=id`, the host as the bolt server; the node key is
  the `?key=` property, else the elementId; the database is `?database=`, default `neo4j`) or a
  relationship type as `?rel=` (`neo4j://host:7687/?rel=WROTE`, read-only), an Elasticsearch or
  OpenSearch default
  index as `?index=` (`elasticsearch://host:9200/?index=books`, `opensearch://host:9201/?index=books`,
  the host as the server;
  `elasticsearch+s://` / `opensearch+s://` for TLS; credentials, when set, ride in the URL userinfo as HTTP basic auth), the
  driver's own connection option — like a `file://`
  source's `?format=`; a query addresses another collection/table/database/label/index with a dotted
  `handle.collection` / `handle.table` / `handle.database` / `handle.label` / `handle.index` (Neo4j also `handle.:TYPE`
  for a relationship type). `-a`/`--active` makes the new source active. `-p`/`--password`
  prompts for the URL password (or reads it from stdin) instead of embedding it in the URL.
  `-d`/`--driver` asserts the expected driver (`mongo`, `redis`, `cassandra`, `dynamodb`, `hbase`, `couchdb`, `couchbase`, `neo4j`, `elasticsearch`, `opensearch`) and errors if it
  disagrees with the scheme. The source is
  pinged before it is saved unless `--skip-verify` is set, so a failed add leaves no trace. `--store
  keyring` moves the URL's password into the OS keyring and strips it from the stored URL (default
  `--store inline` keeps it in the config file).
- `iq ls [group]` — list saved sources as `handle  driver  location`; the active one is marked
  `*`. Passwords in URLs are
  redacted. An optional `group` limits the listing to that group. `-v` adds a header row plus a
  `FORMAT` column (a file source's detected dump format) and an `OPTIONS` column (the source's
  stored option defaults); `-g` lists groups instead of sources; `-j`/`--json` or `-y`/`--yaml`
  emit machine-readable output. `--reveal` prints
  a password stored inline in the config verbatim, and `--expand` resolves a keyring-backed
  source's stored password and inlines it — combine both to print a keyring password verbatim.
- `iq src [<name>]` — show the active source, or set it.
- `iq rm <name>...` — remove one or more sources, or whole groups (a group name removes every
  source under it). Atomic: if any name is unknown, nothing is removed.
- `iq mv <old> <new>` — rename a source, or move it into a group with a group-qualified target
  (`iq mv books prod/books`). When `<old>` is a group, every member is re-prefixed
  (`iq mv prod staging`). The active source and group follow the move.
- `iq ping [<name>...]` — check that sources are reachable, reporting each driver and round-trip
  time (or the error). No arguments pings the active source; a group name pings every member.
  Bounded by `--timeout`; exits non-zero if any source is unreachable.
- `iq inspect [<source>[.<collection>]]` — show a source's native introspection. The positional
  names the source (`iq inspect prod`); with none it uses `--src` or the active source. MongoDB,
  Cassandra, DynamoDB, HBase, CouchDB, Couchbase, Neo4j, Elasticsearch, and OpenSearch sources accept sq-style `<source>.<collection>` /
  `<source>.<table>` / `<source>.<database>` / `<source>.<label>` / `<source>.<index>`
  addressing (`iq inspect prod.books`) to pick the collection/table/database/label/index, overriding the source URL's
  `?collection=`/`?table=`/`?database=`/`?label=`/`?index=` default; Redis sources take no collection.
  `--only`
  narrows the output (repeatable or comma-separated): for Redis, `INFO` sections
  (`iq inspect prod --only memory,server`); for MongoDB, the diagnostic commands (`dbStats`,
  `serverStatus`, `listCollections`, `collStats`, `buildInfo`, `hostInfo`); for Cassandra, the
  system-schema reads (`local`, `tables`, `columns`); for DynamoDB, the metadata reads (`tables`,
  `table`); for HBase, the table listing (`tables`); for CouchDB, the introspection reads (`server`,
  `databases`, `dbinfo`, `indexes`); for Couchbase, the introspection reads (`cluster`, `buckets`,
  `collections`, `indexes`); for Neo4j, the metadata procedures (`server`, `databases`,
  `labels`, `reltypes`, `constraints`); for Elasticsearch and OpenSearch, the metadata reads
  (`server`, `indices`, `mapping`, `aliases`) — no `--only` runs them
  all. `--list` prints the subcommands/sections available for the source (Mongo's, Cassandra's,
  DynamoDB's, HBase's, CouchDB's, Couchbase's, Neo4j's, Elasticsearch's, and OpenSearch's fixed sets; Redis's live INFO sections). `-j`/`--json` or `-y`/`--yaml` for machine-readable output; bounded
  by `--timeout`. The location header is redacted like `iq ls`: `--reveal` un-redacts an inline
  password, `--expand` resolves a keyring-backed one.
- `iq diff <a> <b>` — compare two sources. `--data` (the default) diffs items key by key —
  added / removed / changed, keyed by document `_id` (MongoDB) or key (Redis); it reads both
  keyspaces fully into memory, the deliberate cost of needing both key sets at once, and is allowed
  across drivers (a power tool for verifying a migration, not a schema comparison — the match is
  only as meaningful as the keys lining up). `--stats` diffs native introspection trees (**same
  driver only**). `--schema` diffs an inferred, sampled field/type shape (`--sample`) and is **allowed
  across drivers**: each field path — with `[]` array-element and `{}` map-value wildcards — carries a
  canonical type (`integer`/`number`/`string(date-time)`/`map`/`array`) and a `required`/`optional`
  presence, so it measures logical shape rather than sampling luck and stays quiet under resampling.
  Two backends that genuinely normalize a native type differently (a timestamp as an RFC3339 string
  vs an epoch number) still diff — that is the JSON each serves back, and the format tags make the
  row legible rather than mysterious. Layers combine; `-j`/`--json` or `-y`/`--yaml` for a
  machine-readable delta. `diff` exits non-zero when the sources differ and zero when they match
  (diff(1)-style), so scripts can branch on the exit status. Bounded by `--timeout`.
- `iq schema [source]` — sample a source and emit a draft 2020-12 **JSON Schema** inferred from its
  values (the same inference `diff --schema` uses, projected to standard JSON Schema). It is the
  driver-agnostic, sampled complement to `iq inspect`'s native introspection: address the source like
  `inspect` (`iq schema shop.orders`), sample with `--sample`, bounded by `--timeout`. Non-object
  keyspaces are legal (a string keyspace emits `{"type":"string"}`); it describes values, not keys.
  The output is standard JSON Schema for interop — pipe it to a code generator
  (`iq schema prod.orders > s.json && quicktype -s schema s.json -l go`). A schema is field names and
  types, a few hundred bytes — not a dump of production documents into a third-party tool.
- `iq group [<name>] [--clear]` — show, set, or clear the active **group**.
- `iq driver ls` — list the backend drivers iq can dispatch to, each with its description, the URL
  schemes that select it, and its upstream docs. `-v` appends the file driver's readable dump
  formats and which of them auto-detect versus need `?format=`. `-j`/`--json` or `-y`/`--yaml` for
  machine-readable output. The driver
  name shown here is the same canonical name `iq ls -v`, `ping`, `inspect`, and `diff` report
  (`redis` covers both `redis://` and `rediss://`; `mongo` covers `mongodb://` and `mongodb+srv://`).

**Groups.** A `/` in a name groups sources (`prod/books`, `dev/books`). Set an active group with
`iq group prod`, and an unqualified name resolves inside it — `iq src books` then selects
`prod/books`, falling back to a top-level `books` if the group has none.

Sources live in a TOML file at `<os user config dir>/iq/iq.toml` (e.g. `~/.config/iq/iq.toml`),
written `0600` because a URL may carry a password. Override the path with `IQ_CONFIG`, or per run
with the global `--config <path>` flag (which wins over `IQ_CONFIG`). A source added with
`--store keyring` keeps no password in this file — it lives in the OS keyring (Secret Service on
Linux, Keychain on macOS, Credential Manager on Windows) and is spliced back into the URL only
when connecting.

> `iq add` shadows jq's built-in `add` filter at the top level. To sum with jq, write it inside a
> larger expression, e.g. `iq '[ .a, .b ] | add'`.

### Stored options (`iq config`)

The same file also holds **stored option defaults**: persist a flag's value once so you need not
retype it. Set an option globally, or scope it to one source with `--src`. At query time the
precedence is **explicit flag > per-source option > base option > built-in default**, so a saved
default fills any flag you leave unset, and an explicit flag on the command line always wins.

```bash
iq config set format yaml                 # every query defaults to YAML output
iq config set --src prod timeout 30s      # 30s timeout only when querying "prod"
iq config get --src prod format           # effective value for "prod" (source > base > default)
iq config ls -v                           # every persistable option: value, default, and help
iq '.[]'                                  # renders YAML (the stored default)
iq -f json '.[]'                          # explicit flag overrides the stored default
```

- `iq config location` — print the resolved config file path.
- `iq config get [--src <name>] <option>` — print an option's effective value at that scope.
- `iq config set [--src <name>] <option> <value>` — validate and store a value (base, or per
  source). The value is checked exactly as the flag would check it, so an invalid value is refused.
  `iq config set -D/--delete [--src <name>] <option>` removes a stored value instead.
- `iq config ls [--src <name>]` — list the options set at that scope; `-v` lists every persistable
  option with its effective value, built-in default, and help.
- `iq config edit` — open the config file in `$IQ_EDITOR` (then `$VISUAL`, `$EDITOR`, else `vi`).
- `iq config view [--reveal] [--expand]` — dump the whole config as TOML, source URLs redacted
  like `iq ls`.
- `iq config keyring` — manage the OS-keyring secrets that back `--store keyring` sources:
  - `ls` — list keyring-backed sources, each marked `present` or `missing` (`-j`/`-y` for
    machine-readable output).
  - `get <handle>` — print a source's secret, redacted unless `--reveal`.
  - `set <handle> [value]` — write or update a secret (reads stdin/prompt when the value is
    omitted). A source with an inline password is left untouched — use `migrate` for it.
  - `rm <handle>` — delete a source's secret and mark it inline again.
  - `migrate [<handle>] [--all] [--dry-run]` — move an inline password into the keyring, rewriting
    the stored URL to its password-less form.
  - `prune [--dry-run]` — delete stale entries left for non-keyring sources. The keyring cannot be
    enumerated, so entries whose source was deleted are undetectable and are not pruned.

Persistable options are the flags whose default you would reasonably persist — output (`format`,
`format.decimal`, `compact`), `timeout`, display (`monochrome`, `color`, `no-progress`), and the
diagnostics family (`verbose`, `log*`, `error*`). Per-invocation flags (`--src`,
`--from`/`--combine`, `--explain`, `--unbounded`, `--no-compile`, `--reveal`/`--expand`,
`--debug.pprof`) are not storable. A `--from`/`--combine` query has no single source, so it uses
the base options only, never a per-source override.

> The `log*` options are the one place a stored default and the environment overlap: a stored
> `log*` value fills an unset flag, but an `IQ_LOG*` environment variable still wins over it (the
> `flag > env > default` chain for logging applies before a stored default is treated as "set").
> An explicit `--log*` flag beats both. No other option reads the environment, so this interaction
> is unique to the logging family.

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

Results print as pretty JSON by default. A single format flag selects another rendering; the
flags are mutually exclusive and apply to the jq read path and to `--from`/`--combine`, not to
`exec` (which prints the backend's native reply):

| flag | output |
| --- | --- |
| `-j`, `--json` (default) | pretty JSON, one value per result |
| `-J`, `--jsonl` | compact JSON, one value per line (JSON Lines) |
| `-A`, `--jsona` | every result wrapped in one `[ ... ]` document |
| `-r`, `--raw` | scalars unquoted, one per line; objects and arrays fall back to compact JSON |
| `-y`, `--yaml` | YAML documents, separated by `---` |
| `-g`, `--gron` | flattened `json.path = value;` assignment statements, one per line (gron); greppable and reversible with `ungron`, each result rooted at a repeated `json` |
| `-G`, `--grona` | like `--gron` but result N roots at `json[N]`, so the whole stream ungrons back to one JSON array (gron's `--stream` style) |

`-f`, `--format <name>` selects the same renderings by name — `json`, `jsonl`, `jsona`,
`yaml`, `values` (with `raw` as an alias for `values`), `gron`, `grona` — as an alternative to
the shorthand flags above. It is mutually exclusive with them, so `-f json --jsonl` is rejected.

> **`--jsona` differs from sq's.** iq's `--jsona` wraps the whole result stream in one array
> (like `jq -s`); it is the analogue of sq's plain `--json`. sq's `--jsona` instead emits one
> JSON array *per row* with the keys dropped — a columnar projection that a heterogeneous jq
> value stream has no honest analogue for, so iq keeps the sq flag name but its own behaviour.

`--compact` collapses the pretty renderings to single-line: `--json` becomes one compact
value per line (equivalent to `--jsonl`) and `--jsona` becomes a single-line `[ ... ]`. It
is a no-op for `--jsonl`, `--raw`, `--yaml`, `--gron`, and `--grona`, which are already
condensed (gron and grona are inherently line-based).

`-o`, `--output <file>` writes results to `<file>` instead of stdout, truncating an existing
file. It is global — every command honours it (for example `iq inspect -o report.json`) — and is
orthogonal to the format flags. Color is off for a file unless you force it with `-C`, and
progress and errors still go to stderr.

```bash
./iq '.[].title' --raw          # bare titles, one per line, for shell substitution
./iq '.[]' --jsonl              # one compact document per line
./iq '.[]' -f jsona             # a single JSON array of every result (same as --jsona)
./iq '.[]' -A --compact         # the same array on one line
./iq '.[]' --yaml               # YAML, easier to read for deeply nested documents
./iq '.[]' --gron | grep price  # flat json.path = value; lines, greppable and ungron-able
./iq '.[]' -A -o results.json   # write the results to a file instead of stdout
```

> **gron paths.** `--gron`/`--grona` emit one `path = <compact JSON>;` statement per line,
> object keys sorted, `ungron`-reversible. A key that is an ASCII identifier
> (`^[A-Za-z_$][A-Za-z0-9_$]*$`) follows a bare dot (`json.name`); any other key is bracketed
> and JSON-quoted (`json["odd key"]`) — a deliberate ASCII subset of gron's rule, since
> over-quoting stays ungron-safe. `--gron` repeats the `json` root for every result, so
> ungron is last-write-wins across results; `--grona` roots result N at `json[N]` under a
> leading `json = [];`, so ungron rebuilds the full array (an empty stream ungrons to `[]`,
> like `--jsona`).

#### Decimal numbers

`--format.decimal <auto|number|string>` chooses how a **non-integer decimal** from the backend
is presented to the filter. Because the jq filter runs client-side over the fetched value, this
choice is made at normalization time — it changes what the filter computes on, not just how the
result prints (unlike `sq`, where jq is not involved).

| value | behavior |
| --- | --- |
| `auto` (default) | each backend keeps its faithful form: MongoDB `Decimal128` is an exact string, a Redis fractional number is a `float64` |
| `number` | decimals become bare numbers (`float64`); convenient for arithmetic but lossy beyond `float64` |
| `string` | decimals become their exact literal as a string; precision-safe — use `tonumber` to compute |

Integers are always exact regardless of the mode: they arrive as an `int`, or a big integer when
they exceed 64 bits, so `.count + 1` stays exact rather than rounding through `float64`. Note
that a backend may round before iq sees the value — RedisJSON, for example, stores an integer
larger than 64 bits as a double, so it arrives already in scientific notation. A big integer
renders as a bare number in the JSON formats but as a quoted string under `--yaml` (a `yaml.v3`
limitation); exactness is kept in preference to YAML's numeric form.

```bash
./iq '.book.price' --format.decimal=string   # "19.99" — exact, precision-safe
./iq '.book.price | tonumber * 1.2'          # compute on the exact decimal
./iq '.[].price' --format.decimal=number     # bare numbers, ready for jq arithmetic
```

### Colored output

Output is syntax-highlighted when `iq` writes to a terminal and left plain when it is piped or
redirected, so captured output stays clean. The `--json`, `--jsonl`, `--jsona`, and
`--yaml` renderings get syntax highlighting; the `--raw`, `--gron`, and `--grona` renderings
are always plain so they stay safe for shell capture. The human commands color their signal too: `ping` shows `ok`/`error` in
green/red, `diff` shows additions green, removals red, and changes yellow, and `ls`/`inspect`
highlight the active source and section headers. The raw reply bodies from `exec` and `inspect`
are colored in their native form — JSON syntax highlighting for MongoDB, and redis-cli-style
value tokens for Redis.

Two global flags and the `NO_COLOR` convention control it:

| control | effect |
| --- | --- |
| (default) | color on only when the destination is a terminal |
| `-M`, `--monochrome` | force color off |
| `-C`, `--color` | force color on, even into a pipe or pager |
| `NO_COLOR` env (any value) | color off unless overridden by `-C` |

```bash
./iq '.[]'                # colored on a terminal, plain when piped
./iq -M '.[]'             # never colored
./iq -C '.[]' | less -R   # keep color through a pager
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

A scan has no reliable upfront total (Redis `SCAN`, Mongo cursor), so while one runs `iq` shows an
animated spinner with a running `N scanned` count on **stderr** — a sparse `.[] | select(...)` over
a large keyspace is never silent. When a backend can supply a cheap approximate total (MongoDB's
`estimatedDocumentCount` for an unfiltered whole-collection scan, Redis's `DBSIZE` for its
whole-keyspace `MATCH *` scan), the count is shown against it as
`N scanned (~M est)`; the tilde marks it a hint — it comes from cached metadata and drifts under
concurrent writes, so the scan may exceed it and it never becomes a percentage bar. No total is
shown for a pushed-down filtered scan (it walks a subset) or for a cross-source scan (a per-source
estimate would mislead the aggregate). The
spinner appears only after a short delay, so a fast query never flashes one, and only when stderr
is a terminal: piped or redirected output is never touched, and result rows streamed to stdout are
never garbled by it. Disable it with `--no-progress`.

### Diagnostics & logging

Global flags (adopted from [sq](https://github.com/neilotoole/sq)) control verbose output, file
logging, error rendering, and profiling. They are cross-cutting concerns handled at the CLI
boundary; query results are never changed by them.

| flag | default | effect |
| --- | --- | --- |
| `-v`, `--verbose` | off | print diagnostics (source resolved, store opened, query complete with scan count and elapsed) to stderr, plus the [query plan](#query-plan---explain--v) and a live backend command trace (disables the progress spinner) |
| `--log` | off | enable logging to a file (also via `IQ_LOG`) |
| `--log.file` | `<user cache dir>/iq/iq.log` | log file path; an empty value disables logging |
| `--log.level` | `DEBUG` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |
| `--log.format` | `text` | `text` or `json` |
| `--error.format` | `text` | error output format: `text` or `json` |
| `--error.stack` | off | append the wrapped error cause chain (may include backend internals; redacted) |
| `--error.format.text.verbose` | on | for a jq syntax error in text format, draw a caret span under the offending token |
| `--debug.pprof` | off | write a runtime profile of the whole run: `cpu`, `mem`, `block`, `mutex`, `goroutine`, `thread`, or `trace` |

The `--log*` flags also read the environment when the flag is not set, precedence
**flag > env > default**: `IQ_LOG`, `IQ_LOG_FILE`, `IQ_LOG_LEVEL`, `IQ_LOG_FORMAT`. `-v` writes a
terse human stream to stderr (INFO and above), tinted when stderr is a terminal and following the
same `-M`/`-C`/`NO_COLOR` decision as [colored output](#colored-output); `--log` writes structured
records to a file (down to the chosen level, always plain — never tinted). A source location is
always redacted before it is logged, so a stored credential never reaches a log file.

```bash
./iq -v '.[]'                                        # verbose diagnostics on stderr
./iq --log --log.file=/tmp/iq.log --log.format=json '.[]'   # structured logs to a file
IQ_LOG=true IQ_LOG_FILE=/tmp/iq.log ./iq '.[]'       # enable logging via the environment
./iq --error.format=json '.bad |'                    # machine-readable errors
./iq --debug.pprof=cpu '.[]' && go tool pprof cpu.pprof
```

### Query plan (`--explain`, `-v`)

`--explain` prints a formatted **query plan** and exits without connecting or executing;
`-v`/`--verbose` prints the same plan to stderr, then runs, tracing each backend command. The plan
shows three things, syntax-highlighted when the destination is a terminal:

- the jq filter, pretty-printed with real line breaks (nested `source("name"; "<jq>")` sub-filters
  and every `--from`/`--combine` fragment are formatted too);
- the backend **access plan** — the concrete calls each source will make, derived from the filter's
  route (bounded keys, streaming scan, or materialize): MongoDB `find(<filter>)` (the pushed-down
  filter, on by default, or `{}` under `--no-compile`) or `find` by `_id`; Redis `SCAN 0 MATCH *
  COUNT n` plus per-key `TYPE`/typed reads, or pipelined typed reads for bounded keys;
- for MongoDB, the compiled server-side filter as JSON — exactly what the store pre-filters with
  (empty under `--no-compile`).

```bash
./iq --src orders --explain '.[] | select(.total > 99) | {id, total}'
./iq --src cache --explain '.[] | select(.active)'   # Redis SCAN + typed reads
./iq --src orders -v '.[] | select(.total > 99)'     # plan + live `mongo> find(...)` trace
./iq -v '.[]' 2>/dev/null                            # trace on stderr; stdout stays pure data
```

Under `-v`, the live trace shows the actual commands (`redis> TYPE …`, `mongo> find …`) as they run;
credentials are never traced (Redis `AUTH` and the MongoDB auth handshake are redacted or skipped).
`--explain` never opens a connection, so it works offline against any saved source.

## Drivers

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

Add `-j`/`--json` or `-y`/`--yaml` for machine-readable rows (see [Sources](#sources) for the full flag).

> [!NOTE]
> `VERSIONS` is the range of backend server versions the bundled client library supports —
> [`go-redis` v9](https://github.com/redis/go-redis) for Redis, the
> [MongoDB Go driver v2](https://www.mongodb.com/docs/drivers/go/current/) for MongoDB, the
> [Apache Cassandra gocql driver v2](https://github.com/apache/cassandra-gocql-driver) for
> Cassandra, the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) for DynamoDB
> (`AWS (managed)` — a managed service with no server version),
> [gohbase](https://github.com/tsuna/gohbase) (native protobuf RPC, no Thrift gateway) for HBase,
> [`kivik` v4](https://github.com/go-kivik/kivik) for CouchDB,
> the [Couchbase Go SDK v2 (`gocb`)](https://github.com/couchbase/gocb) for Couchbase,
> the [Neo4j Go driver v5](https://github.com/neo4j/neo4j-go-driver) for Neo4j,
> the [go-elasticsearch v8](https://github.com/elastic/go-elasticsearch) client for
> Elasticsearch,
> and the [opensearch-go v4](https://github.com/opensearch-project/opensearch-go) client for
> OpenSearch (a fork of go-elasticsearch without the product check that refuses non-Elasticsearch
> servers) —
> not a matrix `iq` tests against.
> The integration tests are pinned to `redis:8`, `mongo:8`, `cassandra:5`,
> `amazon/dynamodb-local:2.5.2`, `harisekhon/hbase:2.1`, `couchdb:3`,
> `couchbase:community-7.6.2`, `neo4j:5`,
> `docker.elastic.co/elasticsearch/elasticsearch:8.17.4`, and
> `opensearchproject/opensearch:2.17.1`.

### What every driver guarantees

The per-driver blocks below differ in encoding and pushdown detail, but every backend honors the
same contract:

- **One URL, native nouns.** The URL scheme picks the driver; the keyspace rides in the URL as the
  backend's own noun (`?collection=`, `?table=`, `?database=`, `?label=`/`?rel=`, `?index=`), and a
  query overrides it per run with the dotted `handle.<keyspace>` suffix (see [Sources](#sources)).
- **One jq surface.** A bounded filter fetches exactly the named keys — a missing key reads as
  `null`, never an error; a `.[]`-rooted filter streams the keyspace in bounded pages; a holistic
  filter materializes only behind `--unbounded` (see
  [Bounded reads, streaming scans, and materialized scans](#bounded-reads-streaming-scans-and-materialized-scans)).
- **Pushdown never changes results.** A pushed predicate is only ever a conservative server-side
  pre-filter; the full jq always re-runs client-side, so output is identical with or without it, and
  [`--explain`](#query-plan---explain--v) shows exactly what was pushed.
- **Capabilities are explicit.** Filtered scans, count estimates, writes, clear, drop, and per-key
  delete are opt-in ports: a backend implements what its model supports, and a command against a
  missing capability fails with a clear message instead of emulating it (Redis, whose DB index cannot
  be removed, simply has no `drop`; the read-only file dump has no per-key `delete`).
- **Values round-trip.** Every value normalizes to JSON under a frozen per-backend encoding
  contract, and a `--typed` dump restores through `--insert` losslessly (see
  [Moving data](#moving-data---insert---typed)).
- **Bounded and redacted.** Every backend call is bounded by `--timeout`, and a URL's password is
  redacted from every listing, log line, and error.
- **A native escape hatch.** `iq exec` speaks the backend's own language — verbatim where one exists
  (Redis commands, Mongo command documents, CQL, PartiQL, Cypher, Mango, the Elasticsearch DSL), a small
  fixed verb set where none does (HBase) — see each driver's Raw commands section.

<details>
<summary><b>Redis</b> — value encoding and raw commands</summary>

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
memory; `keys`/`.`/`map` materialize and require the flag). Predicate pushdown is a no-op on Redis,
which has no server-side filtering; the full jq always runs client-side.

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

</details>

<details>
<summary><b>MongoDB</b> — collection keyspace, predicate pushdown, and connection</summary>

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

The database is a saved [source](#sources) — a connection URL whose scheme selects the backend,
`redis://[user:pass@]host:port[/db]` (`rediss://` for TLS) or `mongodb://host:port/db`
(`mongodb+srv://` too). A query resolves its source in this order:

1. the `--src` / `-s` flag
2. the active source (`iq src <name>`)

With no source selected the command errors — there is no ambient URL or environment fallback.
A dotted `--src handle.collection` (or `handle.collection` positional, for `inspect`/`data`/`diff`)
overrides the source URL's MongoDB `?collection=` default for one run (rejected for Redis, which has
no collections); `--timeout` (default `5s`) bounds each query.

</details>

<details>
<summary><b>Apache Cassandra</b> — table keyspace, primary-key mapping, predicate pushdown, and CQL</summary>

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

</details>

<details>
<summary><b>Amazon DynamoDB</b> — table keyspace, primary-key mapping, predicate pushdown, and PartiQL</summary>

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

</details>

<details>
<summary><b>Apache HBase</b> — table keyspace, row-key mapping, cell encoding, and shell-verb raw path</summary>

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

</details>

<details>
<summary><b>Apache CouchDB</b> — database keyspace, _id mapping, Mango pushdown, and raw _find</summary>

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

</details>

<details>
<summary><b>Couchbase</b> — collection keyspace, document-ID mapping, SQL++ pushdown, and raw SQL++</summary>

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

</details>

<details>
<summary><b>Neo4j</b> — node-label and relationship-type keyspaces, key mapping, Cypher pushdown, and raw Cypher</summary>

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

</details>

<details>
<summary><b>Elasticsearch & OpenSearch</b> — index keyspace, _id mapping, Query-DSL pushdown, and raw _search</summary>

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

</details>

<details>
<summary><b>File dumps</b> — query a snapshot offline (read-only)</summary>

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

</details>

## Cross-source queries

`--from` and `--combine` run one query across several sources and stitch the results together.
Each `--from name='<jq>'` reduces a source *at the source* — bounded reads, streaming scans, and
predicate pushdown all still apply — and binds its result set to `$name`; `--combine '<jq>'` then
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

`source("name"; "<jq>")` runs `<jq>` against source `name` (reduced, streamed, and pushed down
like any query) and **yields its results as a stream**; a one-argument `source("name")` yields the
whole source. Both arguments are **strings**, so the sub-filter is quoted — inside the single-quoted
outer filter that means double quotes, `source("orders"; ".[] | select(.x)")`. Because `source()`
yields a stream, collect it before indexing: `INDEX(source(…); .id)` or `[source(…)]`, not
`source(…) | INDEX(.id)`.

> **Memory.** Binding `source()` to a jq value materializes that call's whole result set in memory
> (jq indexing needs a concrete array), even though the read itself streams. Push the reduction into
> the sub-filter — `source("orders"; ".[] | select(.total > 99)")`, not
> `source("orders"; ".[]")` filtered outside — so only the rows you need are held. A bare
> `source("big")` over a large source buys no streaming benefit; prefer `--from`/`--combine` when
> each side is large and independent.

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

## Moving data (`--insert`, `--typed`)

Data movement lives on the query command, sq-style: the jq filter is the transform, `--insert` names a
destination, and piped stdin is an implicit source — there is no separate copy command. (`iq exec`
remains the untyped escape hatch for anything the typed path does not cover.)

- **`--insert <handle>`** — write each item into a destination source: copy, restore, import, or a
  cross-driver migration. This is sq's `--insert`.
- **`--typed`** — emit iq's typed `{"key":…,"type":…,"value":…}` records — a re-importable dump. Only
  **Redis** needs it: a Redis key and its type (hash/list/set/zset/string) live outside the value, so a
  plain value stream loses them. A **Mongo** document self-describes (its `_id` is a field), so plain
  `iq '.[]' --jsonl` is already a restorable backup, exactly as in sq.

With `--insert`/`--typed` the filter transforms **each item** (its key is preserved), so you do **not**
write `.[]` — iteration over the source is implicit. Existing keys are overwritten (upsert) unless
`--no-overwrite`; `--replace` empties the destination first (with confirmation, or `--force`).

A document store (Mongo, CouchDB, Couchbase, Elasticsearch) stores each value exactly as given and so requires it
be a JSON object: a bare scalar — a Redis string value, say — is **rejected** with a hint rather than
silently wrapped as `{"value": …}`, so a successful copy round-trips exactly. Shape it explicitly first,
e.g. `--filter 'if type == "object" then . else {value: .} end'`.

```bash
iq --src books --insert books2                        # source → source, key/_id-preserving
iq --src cache --insert docs                          # cross-driver (Redis → Mongo); object values only
iq --src cache --typed -o dump.jsonl                  # back up Redis losslessly (typed dump)
iq --src books --jsonl -o dump.jsonl                  # back up Mongo with plain output (self-describing)
iq add file:///dump.jsonl -n snap                     # a dump file is a source; then restore it:
iq --src snap --insert cache                          # restore the dump into a live source
cat dump.jsonl | iq '.[]'                             # query a piped dump (implicit stdin)
cat foreign.json | iq --insert books --key-field id   # import foreign JSON from stdin, keyed by id
iq '{t: .title}' --src books --insert kv --key '.t'   # reshape + re-key while copying
```

`--typed` serializes the records in the chosen format — `--jsonl` (default), `--jsona`, or
`--yaml` — and **all three re-import** through a `file://` source or a piped `--insert`,
auto-detected from content by their typed `{key,…,value}` envelope. A huge first record can defeat
the content sniff, so a `.yaml`/`.yml` name or an explicit `?format=` / `--from-format` remains
available as an override. `--dry-run` reports the effect without writing; `--explain` prints the move
plan without connecting.

`iq data clear <target>…` empties a container (Mongo `deleteMany({})`, Redis `FLUSHDB`); `iq data
drop <target>…` removes one (Mongo drops the collection). Redis has no droppable container — a DB
index only empties — so `drop` is rejected for a Redis target with a pointer to `clear`. Both are
distinct from `iq rm`, which only *unregisters* a saved source; these destroy stored data, and prompt
for confirmation unless `--force`. Both take `--explain` (plan without connecting) and `--dry-run`.

`iq data delete <target> <key>…` removes a named set of keys, keeping the container — the typed,
capability-gated, explainable counterpart of the raw per-key `exec delete`. Each key uses the same
spelling as a Get: a bare string (`book:1`), or a JSON array for a composite key (`["shop",42]`). A
key already absent is not an error (delete is idempotent), and the report is honest about it:
`deleted N key(s), M already absent`. Unlike `clear`/`drop` it does **not** prompt — the explicit key
list you typed is the confirmation; use `--dry-run` to preview. A backend with no per-key identity
(the read-only file dump) rejects it, like Redis rejects `drop`. It also takes `--explain`.

```bash
iq --src books --insert books2 --explain   # move plan, no connection
iq data clear books --dry-run              # "would clear books.books (~1240 item(s))"
iq data drop cache --explain               # reports the Redis drop as unsupported
iq data delete cache book:1 book:2         # "deleted 2 key(s), 0 already absent"
iq data delete shop.orders '["eu",42]'     # a composite-key row, by its JSON-array spelling
```

## Architecture

The core read path: a jq filter is classified by the **selector**, a scan is optionally **decomposed**
into a native predicate, and each backend maps that predicate its own way — MongoDB pushes it
server-side, Cassandra pushes equality as a CQL `WHERE` (with `ALLOW FILTERING` when it is not the
partition key), DynamoDB pushes equality and existence as a `Scan` `FilterExpression`, HBase pushes
column equality as a `SingleColumnValueFilter`, CouchDB pushes equality, ranges, existence, a
byte-safe regex, and length as a
Mango `_find` selector, Couchbase pushes equality, ranges, and existence as a SQL++ `WHERE`, Neo4j pushes equality and existence as a Cypher `WHERE` clause, Elasticsearch
and OpenSearch push equality and existence as a `bool` query, Redis scans
and filters client-side. Either way the
full jq re-runs client-side, so
the pushed predicate is only ever a conservative pre-filter and results are identical with or without it.

Query / read path:

```mermaid
graph TD
  F["jq filter (CLI)"] --> SEL["selector.Keys<br/>(AST analysis)"]
  SEL -->|bounded| GET["KVStore.Get(keys)"]
  SEL -->|"streamable scan"| CMP{"FilteredScanner?<br/>(pushdown on)"}
  SEL -->|"holistic scan"| MAT["materialize<br/>(--unbounded)"]

  CMP -->|yes| PD["pushdown.Compile → predicate.Node<br/>(adapter pre-filters server-side)"]
  CMP -->|no| RS["KVStore.ScanBatches<br/>(full scan, no pushdown)"]

  GET --> JQ["run full jq<br/>client-side, per batch"]
  MAT --> JQ
  PD --> JQ
  RS --> JQ
  JQ --> OUT["format renderer<br/>→ output"]
```

A scan emits per-page progress (`RunOptions.OnPage`) to a stderr spinner — CLI only, off unless
attached to a terminal — and an unfiltered scan can fetch a cheap up-front total
(`RunOptions.OnEstimate`, answered from backend metadata where the backend keeps one — a collection
estimate like Mongo's `estimatedDocumentCount`, table metadata, an index count) so progress reads
as ~N.

Data movement & lifecycle / write path:

```mermaid
graph TD
  MV["iq --insert / --typed (CLI)"] --> MSRC["source: --src (live or file:// dump) / stdin → TypedScan"]
  MSRC --> TX["per-item jq transform + re-key"]
  TX --> DST{"--insert or --typed?"}
  DST -->|--insert| PUT["Putter.Put (upsert / insert-only)"]
  DST -->|--typed| ENC["emit {key,type,value} → jsonl / jsona / yaml"]
  PUT --> BW["backend adapter:<br/>type-aware native writes"]
  LF["iq data clear / drop / delete (CLI)"] --> CAP["Clearer.Clear / Dropper.Drop / Deleter.Delete (capability-gated)"]
```

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
- The **write path** mirrors the read path through optional capability ports in `internal/query`,
  the same idiom as `FilteredScanner`/`Estimator`: `Putter` (write a batch of typed records),
  `TypedReader` (`TypedScan`, read `{key, type, value}` batches so a copy preserves each item's
  native type), `Clearer` (empty a container), `Dropper` (remove one), and `Deleter` (remove a
  named set of keys by canonical key). A backend implements
  only the capabilities its model supports, and a command type-asserts and rejects cleanly when one
  is absent — so a new backend never edits the commands, and Redis, whose DB index cannot be
  removed, simply omits `Dropper`, while a keyless store like InfluxDB omits `Deleter`. `Copier` streams `TypedScan → optional --filter transform →
  Put` in bounded pages; the type tag is what makes a Redis round-trip lossless, since a hash and a
  document both normalize to a JSON object. The default command's `--insert` (write items into a
  source) and `--typed` (emit a re-importable `{key,type,value}` dump) drive this — sq-style, with
  the jq filter as the transform, a source/`file://`/piped-stdin read side, and cross-driver
  included — while `iq data clear`/`drop` drive the lifecycle ports. `iq exec` remains the untyped
  escape hatch for anything the typed path does not cover.
- `drivers/file` — a read-only adapter over a local dump file (or a buffered piped stdin). Its
  `*Store` satisfies the read ports (`Get`/`ScanBatches`) and `TypedReader` (so a dump restores
  through `--insert` and a piped dump is queryable), detecting the
  format from content or a `?format=` hint (Redis RDB via `hdt3213/rdb`, mongodump BSON and
  mongoexport Extended JSON via the Mongo driver, DynamoDB export/scan JSON via the DynamoDB driver,
  cqlsh COPY CSV via the Cassandra driver, APOC JSON export reusing the Neo4j envelope, or iq's own
  typed JSONL; gzip is unwrapped transparently)
  and decoding each item to the **same** JSON shape the
  live adapter produces, so a query or restore is identical to the live backend. It implements no
  writer, so a `file://` endpoint is never a copy destination, and `Query` returns a sentinel that
  makes `exec`/`inspect` degrade cleanly. It streams; whole-dataset materialization stays the core's,
  gated by `--unbounded`. Behind the read ports it keeps an optional **decode cache** (`iq cache`,
  CBOR under `<user cache dir>/iq/dumps`): a full scan of a large dump tees its normalized records
  to disk, and later scans read them back instead of re-decoding — transparent to the ports, so the
  data-flow above is unchanged, and never authoritative (a miss just re-decodes).
- `drivers/redis`, `drivers/mongo`, `drivers/cassandra`, `drivers/dynamodb`, `drivers/hbase`, `drivers/couchdb`, `drivers/couchbase`, `drivers/neo4j`, `drivers/elasticsearch` — the
  adapters. Each has one `*Store`
  satisfying the read ports
  (`Query` for exec, `Get`/`ScanBatches` for jq) and the write ports (`Put`/`Clear`/`TypedScan`,
  plus `Drop` for Mongo, Cassandra, DynamoDB, HBase, CouchDB, Couchbase, and Elasticsearch, and `Delete`
  for every live backend except Couchbase — all but the read-only file dump and Couchbase, whose
  per-key delete is a v1 follow-up), with a type-to-JSON normalization frozen as
  that backend's
  encoding contract (Redis types; BSON → `ObjectID`-hex, dates, nested docs; CQL types → uuid-string,
  RFC 3339, base64 blob, collections; DynamoDB `S`/`N`/`B`/`BOOL`/`M`/`L`/sets; HBase raw cell bytes →
  honest UTF-8-or-base64, or an exact `Bytes`-layout value for a `?types=`-declared column; CouchDB
  documents are already JSON, decoded with exact-integer precision, `_id`/`_rev` kept; Couchbase
  documents are already JSON, decoded with exact-integer precision, the document ID kept as KV
  metadata (never injected), a non-JSON document surfaced as a string on a KV get; Neo4j node
  properties with exact integers, base64 bytes, and ISO temporal/spatial strings, `_id`/`_labels`
  kept; Elasticsearch `_source` documents are already JSON, decoded with exact-integer precision, the
  hit `_id` injected) and its
  inverse for
  writes
  (Mongo `bulkWrite`, chunked `deleteMany({_id:{$in}})` per-key delete; Redis pipelined
  `SET`/`HSET`/`RPUSH`/`SADD`/`ZADD`/`XADD`/`JSON.SET` by
  type, chunked `DEL` per-key delete; Cassandra parameterized `INSERT`, `TRUNCATE`, `DROP TABLE`,
  per-key `DELETE … WHERE pk = ?` (pre-read for the present/absent count); DynamoDB `PutItem`, `Scan` +
  `BatchWriteItem` delete-all, `DeleteTable`, per-key `BatchWriteItem` delete (pre-read for the count);
  HBase `Put` / `CheckAndPut` insert-only, a key-only
  `Scan` + per-row `Delete` for clear, per-key whole-row `Delete` (exists-only pre-read), and
  `DisableTable` + `DeleteTable` for drop; CouchDB
  `_bulk_docs` upsert/insert reading current `_rev`s first, `_bulk_docs {_deleted:true}` clear and
  per-key delete, `DELETE /{db}` drop; Couchbase KV bulk `Upsert`/`Insert` (a bulk get pre-read for
  the overwrite count), `DELETE FROM <keyspace>` clear, `DropCollection` drop (the default collection
  refuses, pointing at clear); Neo4j `UNWIND … MERGE (n:Label {key}) SET n += props`
  upsert/insert-only,
  paged `MATCH … DETACH DELETE` clear, per-key resolve + `DETACH DELETE` delete, no drop;
  Elasticsearch refreshing `_bulk` index/create by
  `_id`, `_delete_by_query {match_all}` clear, `_bulk {delete:{_id}}` per-key delete, `DELETE /{index}`
  drop). Redis maps a key
  to a Redis key;
  Mongo maps a key to a document `_id` within the collection it owns from the URL's `?collection=`
  (or a dotted `handle.collection` override); Cassandra maps a key to a row's full primary key within
  the table from the URL's `?table=` (or a `handle.table` override), reading the table schema once at
  connect to encode and reverse it; DynamoDB maps a key to an item's partition (and optional sort) key
  within the table from the URL's `?table=` (region as the host, credentials from the AWS default
  chain), reading the key schema once at connect; HBase maps a key to a row key and the row to a
  nested `{family: {qualifier: value}}` object, with the ZooKeeper quorum as the host and the
  `namespace:table` from the URL's `?table=` (or a `handle.table` override); CouchDB maps a key to a
  document `_id` within the database it owns from the URL's `?database=` (host as the server, or a
  dotted `handle.database` override); Couchbase maps a key to a document ID within the
  bucket.scope.collection it owns from the URL's `?bucket=` and `?collection=` (cluster as the host, or
  a dotted `handle.[scope.]collection` override); Neo4j maps a key to a node — the `?key=` property's value or
  the elementId — within the label it owns from the URL's `?label=` (bolt host as the server, or a
  dotted `handle.label` override) inside the `?database=` (default `neo4j`), or to a relationship
  within a `?rel=` type (or a `handle.:TYPE` override), read-only, whose value carries the endpoint
  elementIds; Elasticsearch maps a key to a document `_id` within the index it owns from the URL's
  `?index=` (host as the server, or a dotted `handle.index` override), reading the index mapping once
  at connect so a term is pushed only onto an exactly-matchable field. Cassandra pushes
  equality/`IN` as a CQL `WHERE`
  (`FilteredScanner`) but has no cheap count, so it omits `Estimator`; DynamoDB pushes
  equality/existence as a `Scan` `FilterExpression` (`FilteredScanner`) and answers `Estimator` from
  its table metadata; HBase pushes column equality as a `SingleColumnValueFilter` (`FilteredScanner`)
  but has no cheap count, so it omits `Estimator`; CouchDB pushes equality, ranges, existence, a
  byte-safe regex, and a polymorphic length as a Mango `_find`
  selector (`FilteredScanner`, falling back to a plain `_all_docs` scan for the exact-negation
  operators and a case-insensitive or non-byte-safe regex) and answers `Estimator` from its `doc_count`; Couchbase pushes equality, ranges,
  and existence as a SQL++ `WHERE` (`FilteredScanner`, falling back to a plain keyset scan for the
  exact-negation operators, regex, and length) but has no cheap metadata count, so it omits
  `Estimator` (like Cassandra and HBase); Neo4j pushes equality and
  existence as a Cypher `WHERE` clause (`FilteredScanner`, falling back to a plain label scan for
  ranges — Cypher's cross-type comparison is not jq's — and the other operators) and answers
  `Estimator` from the label's count store; Elasticsearch pushes equality and existence as a `bool`
  query (`FilteredScanner`, falling back to a plain point-in-time scan for ranges and the other
  operators) and answers `Estimator` from `_count`. An `opensearch://` source is the **same
  `drivers/elasticsearch` adapter** behind a small transport port (`esClient`): the URL scheme picks
  the [opensearch-go](https://github.com/opensearch-project/opensearch-go) client (Elasticsearch's
  own client refuses non-Elasticsearch servers) and its `_search/point_in_time` endpoint and `_id`
  keyset sort; the keyspace model, normalization, pushdown, writes, `exec`, and `inspect` are all
  shared. DynamoDB's connectionless
  client verifies reachability at open (a
  bounded `ListTables` probe), HBase's likewise (a bounded `ClusterStatus` probe, since gohbase
  connects lazily), CouchDB pings at open, Couchbase waits for the cluster and bucket to become ready
  at open, Neo4j verifies connectivity at open, and Elasticsearch
  and OpenSearch probe with an info request at open, so `ping`/`add` need no second round-trip. Each
  also contributes pure, connection-free `--explain` describers
  (`ExplainWrite`/`ExplainClear`/`ExplainDrop`) alongside `ExplainPlan`.
- `internal/diff` — a driver-agnostic structural diff over the normalized JSON values every adapter
  produces. `Tree` diffs two values and `Keyed` aligns two keyed item sets. It holds no I/O: `iq diff`
  reads each side through the ports and hands the materialized values here.
- `internal/shape` — driver-agnostic schema inference (heuristics adapted from quicktype, Apache-2.0;
  no code copied). `Infer` reduces a sample to a typed shape tree; `Comparable` projects a stable
  field/type map that feeds `diff.Tree`, so `diff --schema` compares logical shape and works
  cross-driver, and `JSONSchema` projects a draft 2020-12 document for `iq schema`. Presence is
  parent-relative (`required`/`optional`), numbers split integer from number, strings tag
  date-time/date/uuid, id-keyed sub-objects collapse to maps, and arrays unify their element shape.
  Like `diff`, it holds no I/O — the CLI samples through the ports and hands it the values.
- `internal/config` — the saved sources and stored options. A small TOML store (named connections
  keyed by handle, plus the active source and group, plus a base **options** table and a per-source
  one) the CLI reads to resolve a query's connection and its default flags. It stays driver-agnostic
  and dumb: the backend is inferred from a source's URL scheme and the persistable-option allowlist
  and value validation both live in `cmd`, not here. It records only that a source is keyring-backed;
  the password itself never enters this store.
- `internal/secret` — the credential port. A `Keyring` interface over the OS secret store, so a
  keyring-backed source keeps its password out of the config file; `cmd` splices it back into the
  URL at connect time.
- `cmd` — the CLI adapter and composition root. It holds the driver registry (`cmd/driver.go`): one
  self-describing entry per backend (name, description, schemes, docs, opener, and the connection-free
  `--explain` describers) that `openStore`, `supportedScheme`, every driver label, and `iq driver ls`
  all derive from, so adding a backend is
  one entry. It resolves the selected source (`--src` or the active source) to a URL and an opaque
  address (the dotted `handle.collection` suffix, which the driver interprets),
  picks the adapter by URL scheme through that registry (`openStore`), runs the jq action (routing a
  `source()`-driven filter to the cross-source engine), the `exec` escape hatch, the `data`
  movement/lifecycle group (`copy`/`clear`/`drop`), a source or config command
  (`add`/`ls`/`rm`/`mv`/`src`/`group`/`ping`/`inspect`/`diff`/`driver`/`config`), or a `--from`/`--combine` cross-source query —
  resolving every source name through the same registry — and formats output (a format-flag-selected
  renderer for the jq path — `--json`, `--jsonl`, `--jsona`, `--raw`, or `--yaml`, also selectable by
  name with `--format`, and with `--format.decimal` governing how decimals normalize; per-backend for `exec` —
  redis-cli style for Redis, JSON for Mongo), keeping the core free of any output format. The
  diagnostics surface (verbose output, file logging, error rendering, `--debug.pprof`) also lives
  here: it is set up once per invocation and emits from the CLI boundary, so the query core imports
  no logger and produces no diagnostics of its own. Before any of that, one pre-run step merges the
  stored option defaults into the flags — an unset flag falls back to the selected source's option,
  then the base option, then its built-in default — so persisted defaults reach the whole run
  (`iq config` manages the store; `--config` redirects the file).

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
iq config set format yaml # persist a default flag value (add --src <name> to scope it to a source)
iq config ls -v           # list every persistable option: value, default, and help
iq --config ./iq.toml ls  # run against an alternate config file (overrides IQ_CONFIG)
iq --src books --insert books2 # copy a source into another (handle → handle, cross-driver ok; jq filter transforms each item)
iq --src cache --typed -o dump.jsonl   # dump a source to a re-importable typed JSONL file (Redis-lossless)
iq add file:///backups/prod.rdb -n snap   # register a dump file as a read-only source
iq --src snap '.[] | select(.active)'     # query a dump offline (RDB, BSON, mongoexport, DynamoDB JSON, Cassandra CSV, Neo4j APOC JSON, JSONL)
iq --src snap --insert prod               # restore a dump into a live source (both registered with iq add)
iq data clear books       # empty a container (drop removes it; both prompt unless --force)
iq schema prod.orders     # infer a draft 2020-12 JSON Schema from a sampled source (--sample; describes values, not keys)
iq schema prod.orders > s.json && quicktype -s schema s.json -l go  # generate typed models (a schema is field names/types, a few hundred bytes — not a dump of documents)
go test -short ./...      # fast unit tests, no external services
go test ./...             # full suite; starts ephemeral Redis + MongoDB + Cassandra + DynamoDB Local + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch via testcontainers-go (HBase needs IQ_HBASE_URL)
docker compose up -d --wait   # optional: local Redis + MongoDB + Cassandra + DynamoDB Local + HBase + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch for manual exploration (:6379, :27017, :9042, :8000, :2181, :5984, :8091-8096/:11210, :7687, :9200, :9201)
bash scripts/seed-redis.sh    # load example data into the running Redis
bash scripts/seed-mongo.sh    # load example documents into the running MongoDB
bash scripts/seed-cassandra.sh    # load example rows into the running Cassandra
bash scripts/seed-dynamodb.sh    # load example items into the running DynamoDB Local
bash scripts/seed-hbase.sh    # load example rows into the running HBase
bash scripts/seed-couchdb.sh    # load example documents into the running CouchDB
bash scripts/seed-couchbase.sh    # provision + load example documents into the running Couchbase
bash scripts/seed-neo4j.sh    # load an example graph into the running Neo4j
bash scripts/seed-elasticsearch.sh    # load example documents into the running Elasticsearch
bash scripts/seed-opensearch.sh    # load example documents into the running OpenSearch (:9201)
docker compose down       # stop the local services
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate, scoped to the branch diff vs origin/main (fails on any escaped mutant not in the baseline); set IQ_*_URL to a pre-started stack
make check                # fast offline gate: format, vet, build, lint, dead code, unit tests + coverage report
make cover                # full suite + coverage floor (IQ_COVER_MIN, default 80); IQ_COVER_SHORT=1 for a fast report-only run
make security             # supply-chain + secrets sweep (govulncheck, osv-scanner, gitleaks) + SBOMs to dist/
make sbom                 # write SPDX + CycloneDX SBOMs of the module to dist/
make e2e                  # black-box smoke tests that build and drive the iq binary (+ live Redis/Mongo round-trips when IQ_REDIS_URL/IQ_MONGO_URL are set)
make bench                # JSON decode + pre-filter benchmarks (no containers); pair two runs with benchstat: make bench | tee new.txt; benchstat old.txt new.txt
make mutation             # mutation gate over the branch diff vs origin/main (part of make ci)
make ci                   # full pre-merge gate: check + cover + security + mutation (needs Docker + network)
make tools                # install release tools (svu, git-chglog) into GOPATH/bin
make tools-dev            # install the quality/security toolchain (mutago, deadcode, govulncheck, osv-scanner, gitleaks, syft)
make version              # print the version the next release would take
bash scripts/release.sh --dry-run   # preview the next release without changing anything
make release              # bump version, regenerate CHANGELOG.md, commit, and tag
```

Integration tests skip under `go test -short`. The full `go test ./...` needs Docker: it starts
an ephemeral Redis, MongoDB, Cassandra, DynamoDB Local, CouchDB, Couchbase, Neo4j, Elasticsearch, and OpenSearch via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) on random ports and
tears them down afterwards — no manual `docker compose up` (Cassandra takes ~1 minute to become
ready; Couchbase is the slowest, ~30-60 s). Set `IQ_REDIS_URL` / `IQ_MONGO_URL` / `IQ_CASSANDRA_URL` / `IQ_DYNAMODB_URL` / `IQ_COUCHDB_URL` / `IQ_COUCHBASE_URL` / `IQ_NEO4J_URL` / `IQ_ELASTICSEARCH_URL` / `IQ_OPENSEARCH_URL`
to point at an already-running server (for example the `docker compose` stack) to skip container
startup; the mutation gate, which reruns the suite per mutant, wants this to avoid churn. Against
a shared Redis the suites operate on reserved databases so no run flushes another's data — 15 and 14
are the `cmd` integration scratch pair, 13 is the Redis driver's private DB, and 12 is the black-box
e2e round-trip's — so data seeded into DB 0 by `scripts/seed-redis.sh` survives a test run. The live
e2e flows (`e2e/live_test.go`) drive the built binary against a real backend and run only when
`IQ_REDIS_URL` / `IQ_MONGO_URL` are set, with no localhost fallback, so the offline suite never needs
a server. **HBase is the exception**: its native RPC
needs a fixed-hostname cluster (testcontainers' random ports would break the region server's
advertised name), so its integration tests run only when `IQ_HBASE_URL` points at a running cluster
(`docker compose up -d --wait hbase`); without it they skip. The compose HBase service uses host
networking so a host-side client reaches the region server, and takes ~1–2 minutes to become ready.
The compose stack runs under a fixed project name (`iq`) on a pinned `10.100.0.0/24` bridge, so
`docker compose` behaves the same from any worktree and the auto-assigned bridge subnet can't
collide with a LAN host.

The mutation gate runs [mutago](https://github.com/quality-gates/mutago) and scopes to the current
branch's diff against `origin/main` by default (`--git-diff-lines`), so it only mutates the lines a
change touched. The base is handed to mutago as the merge-base commit with `HEAD`, so it works from
a linked git worktree and tolerates a local base ref that has drifted from the remote. The gate is
`--fail-on-escaped`: a covered mutant that survives (asserts nothing) fails it, while `--coverage`
keeps uncovered lines out of the escaped set — the zero-survivor-on-covered-code contract. Timed-out
mutants are reported as "errored" and are not gated (a wide `--timeout-coefficient` keeps a slow
suite from erroring). A genuine equivalent mutant that cannot be killed is accepted into
`mutago-baseline.json` (committed) with `IQ_MUTATION_UPDATE_BASELINE=1`, after which only *new*
escapes fail — the baseline uses line-number-independent IDs so it survives refactors. Override the
base ref with `IQ_MUTATION_BASE` (set it empty for a full-module scan), or pass a package path (e.g.
`bash scripts/mutation-gate.sh ./cmd`) for a full scan of that package (a path drops the diff-scoping
flags). `IQ_MUTATION_DRYRUN=1` prints the mutant counts without running the tests (a whole-target
upper bound; mutago's dry run is not diff-scoped).

Quality gates are local and layered — the project uses no CI service. `make check` is the fast,
offline pre-commit gate (format, `go vet`, `go build`, `golangci-lint`, dead code via
`deadcode`, and `go test -short` with a coverage report). `make cover` runs the full
container-backed suite and enforces a coverage floor (`IQ_COVER_MIN`, default 80;
`IQ_COVER_SHORT=1` for a fast report-only run). `make security` sweeps dependencies and secrets
(`govulncheck`, `osv-scanner`, `gitleaks`) and writes SBOMs to `dist/`; gosec runs as the Go SAST
inside `golangci-lint run`. `bash scripts/mutation-gate.sh` (also `make mutation`) is the mutation
gate. `make ci` runs check, cover, security, and the mutation gate together — the full pre-merge
gate. Because mutago reruns the suite per mutant, `make ci` is the slowest target; start a shared
stack (`docker compose up -d --wait`) first so the containers are reused. Install the toolchain once
with `make tools-dev`.

## Comparison

How `iq` relates to other query tools. Its niche is narrow: a single static binary that gives
NoSQL stores one jq-based query surface, the filter running client-side over normalized JSON so
semantics are identical across backends.

The tools it resembles fall into five groups:

- Multi-backend SQL (`sq`, Trino, Drill, OctoSQL, usql) unifies databases under one SQL-ish
  language, but targets relational stores; where it reaches NoSQL it runs as a server or engine.
- SQL over files (DuckDB, dsq, trdsql, and others) queries CSV/JSON/Parquet locally, not live
  databases.
- Relational + NoSQL languages (PartiQL, SQL++, JSONiq, GraphQL) span nested and tabular data,
  but are language specs or tied to a specific engine, not a portable CLI.
- Data virtualization / federation platforms (Denodo, Dremio, MindsDB) run as a server that
  translates SQL into each backend's native query, spanning relational, NoSQL, and files without
  migrating data — broad reach, but the unification lives in a heavyweight service, not a binary
  you run locally.
- Universal database clients (DBeaver, DBX, LazySQL) put one GUI or TUI in front of many
  backends, but each connection still speaks that backend's native query language — a shared
  shell, not a shared language.

`sq` — the tool `iq`'s command surface is modelled on — belongs to the first group: it unifies
relational databases and files, and never reaches NoSQL.

Apache Calcite doesn't fit any group above: it's the SQL parsing/optimization framework several
multi-backend engines (Drill, Dremio) embed, not a standalone tool. It's listed because its
adapter model — translating SQL onto MongoDB, Cassandra, Elasticsearch, and others — is the
template most SQL-over-NoSQL tools follow.

Legend: ● primary, ◐ partial, — none. Model is the shape the query language speaks; footprint is
what you run.

| Tool | Query language | Relational | NoSQL | Files | Data model | Footprint |
|---|---|:---:|:---:|:---:|---|---|
| **iq** | **jq** | — | **●** | **◐** | **document** | **single binary** |
| [sq](https://sq.io) | SLQ / SQL | ● | — | ● | tabular | single binary |
| [Apache Calcite](https://calcite.apache.org) | SQL | ● | ◐ | ◐ | relational (via adapters) | library / embedded |
| [Apache Drill](https://drill.apache.org) | SQL | ● | ● | ● | schema-free (both) | server / engine |
| [DBeaver](https://dbeaver.io) | native per-backend | ● | ◐ | ◐ | client-side, per backend | desktop app (JVM) |
| [DBX](https://github.com/t8y2/dbx) | native per-backend | ● | ◐ | ◐ | client-side, per backend | desktop app / CLI |
| [Denodo](https://www.denodo.com) | SQL | ● | ● | ◐ | virtual relational | server (commercial) |
| [Dremio](https://www.dremio.com) | SQL | ● | ◐ | ● | tabular (Arrow) | server / cluster |
| [dsq](https://github.com/multiprocessio/dsq) | SQL | — | — | ● | tabular | single binary |
| [DuckDB](https://duckdb.org) | SQL | ◐ | — | ● | tabular | in-process / CLI |
| [GraphQL federation](https://graphql.org/learn/federation/) | GraphQL | ● | ● | — | typed graph (both) | server |
| [JSONiq](https://www.jsoniq.org) | JSONiq | — | ● | ● | document | library / engine |
| [LazySQL](https://github.com/jorgerojas26/lazysql) | native SQL | ● | — | — | tabular | single binary (TUI) |
| [MindsDB](https://mindsdb.com) | SQL | ● | ● | ◐ | virtual relational | server |
| [OctoSQL](https://github.com/cube2222/octosql) | SQL | ● | ◐ | ● | tabular | single binary |
| [PartiQL](https://partiql.org) | PartiQL | ● | ● | ◐ | nested (both) | spec / embedded |
| [SQL++](https://asterixdb.apache.org/docs/0.9.9/sqlpp/manual.html) / [N1QL](https://www.couchbase.com/products/n1ql/) | SQL++ | ◐ | ● | — | document | DB engine |
| [Trino](https://trino.io) / [Presto](https://prestodb.io) | SQL | ● | ● | ● | tabular (◐ JSON) | server / engine |
| [usql](https://github.com/xo/usql) | native SQL | ● | ◐ | — | tabular | single binary (multiplexer) |

Placement is by each tool's primary targets; several (Trino, Drill, OctoSQL, DuckDB, Calcite)
partially reach neighbouring columns via connectors, adapters, or extensions, and `iq` reaches
Files the same way — a read-only `file://` source over database dumps, not arbitrary files. The
takeaway is the
NoSQL column paired with footprint: `iq` is the only tool pairing a unified query language across
NoSQL backends with a single lightweight binary. Data virtualization platforms (Denodo, Dremio,
MindsDB) get the unified language but need a server. Universal clients (DBeaver, DBX, LazySQL)
get a lightweight footprint but no unified language — each connection still speaks that backend's
native dialect.

## See also

- [awesome-jq](https://github.com/jqlang/awesome-jq) — the curated list of jq tools, guides, and
  resources. `iq` uses jq as its filter language, so most of what applies to jq carries over.
