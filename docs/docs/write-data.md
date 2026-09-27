---
icon: material/pencil-outline
---

# Write data

Data movement lives on the query command, like in `sq`. The `jq` filter is the
transform, `--insert` names a destination, and piped stdin is an implicit
source, there is no separate copy command. `iq exec` remains the untyped escape
hatch for anything the typed path does not cover.

## Insert `--insert`

With `--insert`/`--typed` the filter transforms each item (its key is
preserved), so you do not write `.[]`, iteration over the source is implicit.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--insert <source>` | ✗ | write each item into this destination source (copy/restore/import) instead of rendering |
| | `--typed` | ✗ | emit typed `{"key":…, "type":…, "value":…}` records, a re-importable dump (needed for Redis, a document store's plain output already restores) |

```sh { title='Source → Source, key/id preserving' }
iq --src books --insert books2
```
```sh { title='Cross-driver (Redis → Mongo), object values only' }
iq --src cache --insert docs
```
```sh { title='Back up Redis losslessly (typed dump)' }
iq --src cache --typed -o dump.jsonl
```
```sh { title='Back up Mongo with plain output (self-describing)' }
iq --src books --jsonl -o dump.jsonl
```
```sh { title='Add a dump file, then restore it into a live source' }
iq add file:///dump.jsonl -n snap
iq --src snap --insert cache
```
```sh { title='Move plan, no connection' }
iq --src books --insert books2 --explain
```

!!! warning "Implicit iteration"

    This is the one place `iq` reads a filter per item, everywhere else (a
    plain query and the `<source>=<jq>` specs `iq combine`, `iq diff` and
    `iq schema` take) the filter is rooted at the whole keyspace and you write
    `.[]` yourself.

    The split is deliberate, a keyspace-rooted filter can aggregate across
    items (`[.[] | .total] | add`) and can name a single key
    (`.["orders:42"]`), neither of which a per-item filter can express, while
    the write path has no keyspace at all when items arrive from piped stdin.

    Nothing can tell the two apart automatically (`.name` means the key named
    "name" keyspace-rooted and the field `name` per item) so they stay separate
    rather than guessing. Existing keys are overwritten (upsert) unless
    `--no-overwrite` makes the run insert-only. `--replace` empties the
    destination first (with confirmation, or `--force`).

    A document store (Mongo, CouchDB, Couchbase, Elasticsearch) stores each
    value exactly as given and so requires it be a JSON object. A bare scalar
    (for example a Redis string value) is rejected with a hint rather than
    silently wrapped as `{"value": …}`, so a successful copy round-trips
    exactly. Shape it explicitly first, for example
    `iq 'if type == "object" then . else {value: .} end' --insert <dest>`.

!!! note "Typed format"

    `--typed` serializes the records in the chosen format, `--jsonl` (default),
    `--json`, `--jsona`, or `--yaml`. All of these re-import through a
    `file://` source or a piped `--insert`, auto-detected from content by their
    typed `{"key":…, "type":…, "value":…}` envelope.

    A huge first record can defeat the content sniff, so a `.yaml`/`.yml` name
    or an explicit `?format=` / `--from-format` remains available as an
    override (`jsonl`, `yaml`, `mongoexport`, `bson`, `rdb`, `dynamodb-json`,
    `cassandra-csv`, or `neo4j-json`, aliases like `json` are accepted).

    The renderings that cannot carry a record back are rejected rather than
    written. `--raw` (a scalar cannot hold the envelope), `--format parquet`
    (columnar) and `--gron`/`--grona` (flattened assignments no source
    decodes) drop `--typed` to grep or export the value stream instead.

## Key mapping `--key*`

On a plain copy each item carries its source key, so no key flag is needed.
Foreign JSON (piped stdin, a reshaped stream) has no natural key: `--key-field`
takes it from an object field, `--key` computes it with a jq expression, and
`--key-prefix` prepends a namespace either way.

`iq combine --insert` is the one write that always requires `--key` or
`--key-field`: a combine's results come out of one program over a null input,
so no value has a key to inherit and the run is refused up front rather than
failing partway through a copy. For the same reason `combine` has no
`--typed`, a typed dump is a stream of `{key,type,value}` records and
needs the same key. A write flag used without `--insert` is an error, never
silently ignored.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--key <string>` | none | `jq` expression yielding each written item's key (--insert/--typed) |
| | `--key-field <string>` | none | object field to take each written item's key from (--insert) |
| | `--key-prefix <string>` | none | string prepended to every written key (--insert/--typed) |

```sh { title='Persist the joined rows into a third source' }
iq combine 'users=.[] | {id, name}' 'orders=.[] | select(.total > 99)' \
   --with '($users | INDEX(.id)) as $u | $orders[] | . + {name: $u[.userId].name}' \
   --insert joined --key '.userId | tostring' --key-prefix 'j:'
```

```sh { title='import foreign JSON from stdin, keyed by id' }
cat foreign.json | iq --insert books --key-field id
```
```sh { title='reshape + re-key while copying' }
iq '{t: .title}' --src books --insert kv --key '.t'
```

## Type mapping `--type`

A plain copy carries each item's native type along (a Redis hash lands as a
hash), so no type flag is needed. A filter that reshapes the value drops that
type, the output is plain JSON, and `--type` names the native type the
destination stores it as.

Left unset, a typed destination infers it from the shape (Redis writes a scalar
as a `string` and an object or array as `json`), so `--type` is only required
when you want something else, a `hash` built from an object, a `list` or `set`
from an array, a `zset` from `[{member, score}]` pairs.

A document store ignores it, every value is a document there. With `--typed`
the same flag stamps the `type` field of each dump record instead.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--type <string>` | none | native type stamped on each written value, for example hash, list, json (--insert/--typed) |

```sh { title='Reshape Mongo documents into Redis hashes' }
iq '{title, year: (.year | tostring)}' --src books --insert cache --type hash
```
```sh { title='Project one field per item into a Redis list' }
iq '[.tags[]]' --src books --insert cache --type list --key-prefix 'tags:'
```
```sh { title='Same reshape, no --type: an object lands as RedisJSON' }
iq '{title, year}' --src books --insert cache
```
```sh { title='Dump reshaped items with an explicit type tag' }
iq '{title}' --src books --typed --type json -o titles.jsonl
```

## Replace `--replace`

By default a write upserts, existing keys are overwritten, everything else in
the destination stays. `--replace` turns the copy into a restore, it empties
the destination first (the same operation as `iq data clear`, a Redis
`FLUSHDB`, a Mongo `deleteMany({})`) and then writes, so the destination ends
up holding exactly the source.

Because it destroys data it prompts (`clear <destination> before writing`),
`--force` answers yes, and `--dry-run` reports the copy without clearing
anything.

A destination that cannot be cleared (the read-only file dump) is refused up
front. `--no-overwrite` is the opposite choice, insert-only, so the two are
mutually exclusive, and neither applies to a `--typed` dump.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--replace` | ✗ | empty the destination before writing, with confirmation (--insert) |
| | `--force` | ✗ | skip the confirmation prompt for `--replace` |
| | `--no-overwrite` | ✗ | skip keys that already exist (--insert) |

```sh { title='Restore a dump so the destination matches it exactly' }
iq --src snap --insert cache --replace
```
```sh { title='Same, unattended (no prompt)' }
iq --src snap --insert cache --replace --force
```
```sh { title='Preview the restore: reports the copy, clears nothing' }
iq --src snap --insert cache --replace --dry-run
```
```sh { title='Fill gaps only, never touch an existing key' }
iq --src books --insert books2 --no-overwrite
```

## Lifecycle previews

Every `iq data` subcommand (`delete`, `clear`, `drop`) shares two previews.
`clear` and `drop` also prompt before destroying data, `--force` skips the
prompt.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--explain` | ✗ | print the access plan and exit without connecting or changing anything |
| | `--dry-run` | ✗ | connect and report the real effect without changing anything |

```sh { title='Plan only: which operation, and whether the driver supports it' }
iq data drop cache --explain
```
```sh { title='Connect and count what a clear would remove, then stop' }
iq data clear shop.orders --dry-run
```
```sh { title='Check which of the named keys exist before deleting' }
iq data delete cache book:1 book:2 --dry-run
```

## Delete `data delete`

`iq data delete <target> <key>` removes a named set of keys, keeping the
container. The typed, capability-gated, explainable counterpart of a raw
per-key delete (HBase's `exec delete` verb, a Redis `DEL`).

Each key uses the same spelling as a *Get*, a bare string (`book:1`) or a JSON
array for a composite key (`["shop",42]`). A key already absent is not an error
(delete is idempotent) and the report states it
(`deleted N key(s), M already absent`).

Unlike `data clear`/`data drop` it does not prompt, the explicit key list you
typed is the confirmation (use `--dry-run` to preview). A backend with no
per-key identity (the read-only file dump) rejects it (like Redis rejects
`data drop`).

```sh { title='"deleted 2 key(s), 0 already absent"' }
iq data delete cache book:1 book:2
```
```sh { title='a composite-key row, by its JSON-array spelling' }
iq data delete shop.orders '["eu",42]'
```

## Clear `data clear`

Empties a container but keeps it (for example MongoDB `deleteMany({})` or Redis
`FLUSHDB`). Both this and `iq data drop` are distinct from `iq rm`, which only
unregisters a saved source. These destroy stored data, and prompt for
confirmation unless `--force` is used.

```sh { title='Empty a Redis source called "cache" (FLUSHDB)' }
iq data clear cache
```
```sh { title='Empty two MongoDB collections "shop.orders" and "shop.users"' }
iq data clear shop.orders shop.users
```

## Drop `data drop`

Removes the container entirely and prompts for confirmation unless `--force` is
used. Sources without a droppable container are rejected (for example Redis).

```sh { title='Reports drop as unsupported for Redis source called "cache"' }
iq data drop cache --explain
```
```sh { title='Drop one Mongo collection called "shop.orders"' }
iq data drop shop.orders
```
```sh { title='Drop containers "shop.orders" and "shop.users"' }
iq data drop shop.orders shop.users
```

## Dry run `--dry-run`

Reports the effect without writing. It does everything except the final
mutation, connects, opens source and destination, runs the real scan, applies
the real transform, probes capabilities and then suppresses the write.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--dry-run` | ✗ | report the effect of `--insert` without writing anything |

```sh { title='Dry run for a copy into "books2"' }
iq --src books --insert books2 --dry-run
```
```sh { title='Dry run for clearing source "books"' }
iq data clear books --dry-run
```
