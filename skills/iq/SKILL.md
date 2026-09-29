---
name: iq
description: Query, dump, copy, diff and write NoSQL data with jq filters through the iq CLI, one language over Redis, MongoDB, Cassandra, DynamoDB, Elasticsearch, OpenSearch, CouchDB, Couchbase, HBase, Neo4j and their dump files (RDB, BSON, mongoexport, DynamoDB export, cqlsh CSV, APOC JSON). Use when a task reads, samples, inspects, exports, restores, migrates or compares data in any of those stores, needs a JSON Schema of a collection, or must run a backend's native command, and the iq binary is available.
license: MIT
compatibility: Requires the iq CLI on PATH (https://github.com/zsltg/iq). A live source needs network access to its database; a file:// dump needs no server.
metadata:
  author: zsltg
  docs: https://zsltg.github.io/iq/
---

# iq: jq for NoSQL databases

`iq` runs a jq filter against a registered source. The filter is both the key selector and the transform: `.["k"]` fetches one key, `.[] | select(...)` streams the keyspace in pages, and a filter that needs the whole keyspace at once (`.`, `keys`, `length`, `group_by`, `limit()`, `first()`, slices) is refused unless `--unbounded`. Values normalize to JSON, the filter runs client-side, and a `select()` is pushed to the server only as a conservative pre-filter, so results are identical on every backend. Queries never write.

Not for: relational databases (use the native client or `sq`), or heavy aggregation over a large keyspace (use the backend's own language through `iq exec`).

## Workflow

1. Find the source: `iq ls -v`. Address it as `<handle>` or `<handle>.<keyspace>` (collection, table, index, label, ...). Register a new one only when asked, `iq add -n <handle> '<uri>'` (the password goes to the OS keyring), and never put a password in a command line or repeat a URI that carries one.
2. Plan first: `iq --src <handle> --explain '<filter>'`. It never connects; it prints the route (bounded read, streaming scan, materialized scan) and which `select()` conjuncts the backend evaluates. Prefer bounded or streaming.
3. Run machine-readable: `iq --src <handle> --jsonl -M --error.format json --timeout 30s '<filter>'`. Single-quote the filter. A missing key reads as `null`, never an error.
4. Keep results small: project fields (`{id, total}`), put equality tests in `select()` so they push down, and send anything larger than a screen to a file (`--typed -o out.jsonl`) to read selectively.
5. Write only on an explicit instruction: preview with `--dry-run` (connects, reports the exact effect, changes nothing), then run. Prefer `--no-overwrite`. `--replace` and `iq data clear`, `iq data drop`, `iq data delete` destroy data: confirm with the user first and never add `--force` on your own.

## Commands

- Read one key: `iq --src shop.orders '.["o-42"]'`
- Filtered stream, pushed down: `iq --src shop.orders '.[] | select(.status == "new") | {id, total}'`
- Whole-keyspace filter (ask first on a large keyspace): `iq --src shop.orders --unbounded 'length'`
- Typed, lossless dump: `iq --src cache --typed -o cache.jsonl`
- Query a backup without a server: `iq add -n snap file:///backups/prod.rdb`, then `iq --src snap '.[] | select(.active)'` (format sniffed; gzip or unusual layouts take `?format=`)
- Copy or restore across stores, native types intact: `iq --src snap --insert cache --dry-run`, then without `--dry-run`
- Import foreign JSON: `cat rows.jsonl | iq --insert cache --key-field id`
- Diff two sources, exit 1 on a difference: `iq diff prod staging --data`, `--schema` (cross-driver), `--stats` (same driver)
- JSON Schema of a collection: `iq schema prod.orders`
- Backend metadata and reachability: `iq inspect prod --list`, `iq inspect prod --only <section>`, `iq ping prod`
- Join across sources: `iq combine 'users=.[]' 'orders=.[] | select(.total > 99)' --with '($users | INDEX(.id)) as $u | $orders[] | . + {name: $u[.userId].name}'`
- Native command, verbatim, every `iq` flag before `exec`: `iq --src prod exec '<backend language>'`
- Other outputs: `-y` YAML, `-r` raw strings, `-g` gron, `--format parquet -o out.parquet`

## Rules

- Never guess a handle, keyspace or field name; `iq ls -v`, `iq inspect` and `iq schema` are cheap.
- `--explain` before every scan and every write; `--dry-run` before every write.
- Say what a command costs before running it on production-sized data: `--unbounded` loads the whole keyspace into memory, a DynamoDB scan reads and bills the whole table, a Cassandra pushdown may run with `ALLOW FILTERING`.
- Pushdown covers equality and existence everywhere, ranges and regex on some backends; everything else runs client-side over a full scan.
- Streamed output is in scan order and may repeat an item if the keyspace resizes mid-scan; use `--unbounded` when the user needs sorted, exactly-once output.
- Redis strings stay strings (`tonumber` before arithmetic), hashes are objects, sets are sorted arrays, sorted sets are `[{member, score}]`.
- With `--error.format json` an error is `{"error":{"message":...,"causes":[...]}}`. If `iq` refuses a filter as needing `--unbounded`, ask before adding it; on a timeout raise `--timeout`.
- Passwords are redacted everywhere; keep it that way (no `--reveal`, no pasted URIs).

## Reference

`iq --help`, `iq <command> --help`, `man iq`, and the docs at https://zsltg.github.io/iq/ (Drivers for each backend's key and value mapping and pushdown table, Write data, Query plan). Read the help before guessing a flag.
