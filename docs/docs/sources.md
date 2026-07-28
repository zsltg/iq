---
icon: material/database-outline
---

# Sources

`iq` connects only through **saved sources**, a named connection you register
once, then select by name or as the default.

## Add `add`

| short | long | default | description |
| --- | --- | --- | --- |
| `-a` | `--active` | off | make the new source the active source |
| `-d <string>` | `--driver <string>` | auto-detect | expected backend driver, must match the URL scheme |
| `-n <string>` | `--handle <string>` | the keyspace the URL names | handle for the source, derived from the URL when omitted |
| `-p` | `--password` | off | prompt for the URL passwrod or read it from stdin |
| | `--skip-verify` | off | skip the post-add reachability check |
| | `--store <string>` | `inline` | where the URL's password is kept, `inline` (in the config file) or `keyring` (OS keyring) |

```sh { title='Add a MongoDB source, does not become active, defaults to handle "books"' }
iq add mongodb://localhost:27017/books
```
```sh { title='Add a Redis source with the handle "cache"' }
iq add -n cache redis://localhost:6379/0
```
```sh { title='Add a Cassandra source scoped to the "orders" table and make it active; the handle defaults to "orders"' }
iq add -a 'cassandra://localhost:9042/shop?table=orders'
```
```sh
iq add -n books 'hbase://localhost:2181/?table=iq_books'     # an HBase source (host = ZooKeeper quorum)
iq add -n docs 'couchdb://admin:pass@localhost:5984/?database=iq' # a CouchDB source (host = server)
iq add -n cb 'couchbase://Administrator:pass@localhost/?bucket=iq' # a Couchbase source (host = cluster)
iq add -n graph 'neo4j://neo4j:pass@localhost:7687/?label=Person&key=id' # a Neo4j source (label = keyspace)
iq add -n docs 'elasticsearch://localhost:9200/?index=books' # an Elasticsearch source (index = keyspace)
iq add -n logs 'opensearch://localhost:9201/?index=books'   # an OpenSearch source (same driver)
iq src cache                                                 # make "cache" the active source
iq ls                                                        # list sources: handle driver url (active marked *); -v adds format + options
```

## Group `group`

## List `ls`

## Move `mv`

## Ping `ping`

## Remove `rm`

## Show/Set `src`


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
  `-n`/`--handle` names the source; when omitted a handle is derived from the most specific
  container the URL names — the keyspace a driver-owned param pins (`?collection=`, `?table=`,
  `?index=`, `?bucket=`, `?database=`, `?label=`, `?rel=`), else the MongoDB database or Cassandra
  keyspace name, else the dump file's stem for a `file://` source, else the driver, disambiguated
  with a numeric suffix on collision. A MongoDB default collection rides in the URL as `?collection=`
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
  time (or the error). No arguments pings the active source; a group name pings every member;
  `--all` pings every saved source and takes no arguments. Bounded by `--timeout`; exits
  non-zero if any source is unreachable.
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
  row legible rather than mysterious. Arrays are aligned by a longest common subsequence, so a single
  insertion reports one addition rather than a cascade at every later index. `--set-arrays` instead
  compares every array order-insensitively as a multiset (duplicates counted), reporting membership
  deltas at the array's own path with no index segment — a pure reorder becomes no difference. Layers
  combine; `-j`/`--json` or `-y`/`--yaml` for a machine-readable delta. `--patch` emits an
  [RFC 6902](https://www.rfc-editor.org/rfc/rfc6902) JSON Patch that transforms the left source into
  the right (piped to any JSON Patch tool); it renders exactly one layer (choose one of
  `--data`/`--stats`/`--schema`), and for `--data` the pointers read `/<key>/<field>` over the whole
  keyspace map. `--patch` excludes `--json`, `--yaml`, and `--set-arrays` (a positional patch cannot
  carry order-insensitive semantics). `diff` exits non-zero when the sources differ and zero when they
  match (diff(1)-style), so scripts can branch on the exit status. Bounded by `--timeout`.
- `iq schema [source]` — sample a source and emit a draft 2020-12 **JSON Schema** inferred from its
  values (the same inference `diff --schema` uses, projected to standard JSON Schema). It is the
  driver-agnostic, sampled complement to `iq inspect`'s native introspection: address the source like
  `inspect` (`iq schema shop.orders`), sample with `--sample`, bounded by `--timeout`. Non-object
  keyspaces are legal (a string keyspace emits `{"type":"string"}`); it describes values, not keys.
  The output is standard JSON Schema for interop — pipe it to a code generator
  (`iq schema prod.orders > s.json && quicktype -s schema s.json -l go`). A schema is field names and
  types, a few hundred bytes — not a dump of production documents into a third-party tool.
  `--format` picks the contract dialect the same inference projects into: `jsonschema` (default,
  draft 2020-12, honoring `-y` for YAML) or `odcs`, an **Open Data Contract Standard v3.1.0** contract
  emitted as YAML (its canonical form) for data-contract tooling (datacontract-cli, Soda, Great
  Expectations). ODCS carries nine logical types with no binary or decimal member, so iq's date-time
  and date formats demote to `logicalType: date` with a JDK format pattern, a UUID stays `string` with
  the `uuid` format, and base64-binary and exact-decimal values stay plain `string`. The contract's
  identifiers (`id`, `name`, schema-object name) derive deterministically from the source handle and
  keyspace — no timestamps or random ids (`iq schema prod.orders --format odcs > orders.odcs.yaml`).
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

## Stored options (`iq config`)

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
