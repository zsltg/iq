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
| | `--insert <source>` | ✗ | write the combined results into a destination source instead of rendering (needs `--key` or `--key-field`) |
| | `--typed` | ✗ | emit typed `{"key":…, "type":…, "value":…}` records, a re-importable dump (not required for all sources) |

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
    `--no-overwrite` or `--replace` empties the destination first (with
    confirmation, or `--force`).

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
    override.

    The renderings that cannot carry a record back are rejected rather than
    written. `--raw` (a scalar cannot hold the envelope), `--format parquet`
    (columnar) and `--gron`/`--grona` (flattened assignments no source
    decodes) drop `--typed` to grep or export the value stream instead.

## Key mapping `-key*`

`--insert <source>` writes the results into a destination rather than rendering
them, reusing the same write path as `--insert`, so `--key-prefix`, `--type`,
`--no-overwrite`, `--replace`/`--force` and `--dry-run` all mean what they do
there.

`--key` (or `--key-field`) is required with `--insert`, unlike a plain copy
where each item carries its source key. A combine's results come out of one
program over a null input, so no value has a key to inherit and the run is
refused up front rather than failing partway through a copy.

For the same reason there is no `--typed` here, a typed dump is a stream of
`{key,type,value}` records and would need the same key. A write flag used
without `--insert` is an error, never silently ignored.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--key <string>` | none | `jq` expression yielding each written item's key (--insert) |
| | `--key-field <string>` | none | object field to take each written item's key from (--insert) |
| | `--key-prefix <string>` | none | string prepended to every written key (--insert) |

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

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--type <string>` | none | native type stamped on each written value, e.g. hash, list, json (--insert) |

## Replace `--replace`

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--replace` | ✗ | empty the destination before writing, with confirmation (--insert) |
| | `--force` | ✗ | skip the confirmation prompt for `--replace` |
| | `--no-overwrite` | ✗ | skip keys that already exist (--insert) |

## Delete `data delete`

`iq data delete <target> <key>` removes a named set of keys, keeping the
container. The typed, capability-gated, explainable counterpart of the raw
per-key `iq exec delete`.

Each key uses the same spelling as a *Get*, a bare string (`book:1`) or a JSON
array for a composite key (`["shop",42]`). A key already absent is not an error
(delete is idempotent) and the report is honest about it
(`deleted N key(s), M already absent`).

Unlike `data clear`/`data drop` it does not prompt, the explicit key list you
typed is the confirmation (use `--dry-run` to preview). A backend with no
per-key identity (the read-only file dump) rejects it (like Redis rejects
`data drop`).

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--dry-run` | ✗ | report the effect of `data delete` without writing anything |
| | `--explain` | ✗ | prints a formatted query plan and exists without connecting or executing |

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

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--force` | ✗ | skip the confirmation prompt for `drop` |

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

```sh { title='Dry run for clearing source "books"' }
iq data clear books --dry-run
```
