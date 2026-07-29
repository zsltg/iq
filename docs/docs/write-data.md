---
icon: material/pencil-outline
---

# Moving data (`--insert`, `--typed`)

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

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
write `.[]` — iteration over the source is implicit. This is the one place iq reads a filter per item;
everywhere else — a plain query, `--from`, and the `<source>=<jq>` specs `iq diff` and `iq schema` take —
the filter is rooted at the whole keyspace and you write `.[]` yourself. The split is deliberate: a
keyspace-rooted filter can aggregate across items (`[.[] | .total] | add`) and can name a single key
(`.["orders:42"]`), neither of which a per-item filter can express, while the write path has no keyspace
at all when items arrive from piped stdin. Nothing can tell the two apart automatically — `.name` means
*the key named "name"* keyspace-rooted and *the field `name`* per item — so they stay separate rather
than guessing. Existing keys are overwritten (upsert) unless
`--no-overwrite`; `--replace` empties the destination first (with confirmation, or `--force`).

A document store (Mongo, CouchDB, Couchbase, Elasticsearch) stores each value exactly as given and so requires it
be a JSON object: a bare scalar — a Redis string value, say — is **rejected** with a hint rather than
silently wrapped as `{"value": …}`, so a successful copy round-trips exactly. Shape it explicitly first,
e.g. `iq 'if type == "object" then . else {value: .} end' --insert <dest>`.

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
