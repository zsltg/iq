---
icon: material/magnify
---

# Query data

The default action, a `jq` filter run against the active source (see
[Sources](sources.md)).

The top-level paths name the keys to fetch, the result is printed as pretty
JSON by default (see [Output formats](output.md))

```sh { title='Fetch the key "greeting"' }
iq '.greeting'
```
```sh { title='Fetch the key "book:1" (a key containing a colon needs bracket-quoting)' }
iq '.["book:1"]'
```
```sh { title='Fetch the key "book:1" and extract one field' }
iq '.["book:1"].title'
```
```sh { title='Fetch keys "book:1", "book:2" and project a field from each' }
iq '[ .["book:1"].title, .["book:2"].title ]'
```
```sh { title='Values are strings, convert before arithmetic calculations' }
iq '.["book:2"].price | tonumber + 5'
```

!!! warning "Escaping"

    Always wrap the filter in single quotes. `jq` syntax is full of characters
    the shell would otherwise expand or split, brackets (`[ ]`), whitespace,
    `|`, `*`, `$` and bracket-quoting a colon key like `.["book:1"]` reads as a
    glob to `zsh` (`no matches found`) or `bash` unless quoted.

## Cross-source queries

Both [Compose](#compose-source) and [Combine](#combine-combine) reduce per
source then combine, pick what the query needs.

| | Compose `source()` | Combine `iq combine` |
| --- | --- | --- |
| Shape | a single `jq` filter | a positional spec per source, then one `--with` program |
| Correlated&nbsp;reads<br><small>(B&nbsp;keyed&nbsp;by&nbsp;A's&nbsp;rows)</small> | ✓ nest `source()` | ✗ specs are independent |
| Quoting | sub-filter is a quoted string inside the filter | each spec is its own argument |
| Memory | each reduced result held until combine, plus a correlated `source()` re-runs per row | each reduced result held until combined |

Reach for `source()` when a read depends on another source's values, or to keep
everything in one composable filter.

Reach for `iq combine` for a straightforward join, union, or aggregate across a
few sources.

### Compose `source()`

`source("name"; "<jq>")` runs `<jq>` against source `name` (reduced, streamed
and pushed down like any query) and yields its results as a stream, a
one-argument `source("name")` yields the whole source.

Both arguments of `source()` are strings, so they need to be quoted. It also
yields a stream, collect it before indexing with `INDEX(source(…); .id)` or
`[source(…)]`, not `source(…) | INDEX(.id)`.

A filter that calls `source()` runs over a null input, every read is an
explicit `source()` call and there is no implicit primary source, so it needs
no active source. Names resolve through the registry like `--src`, active-group
namespacing included.

```sh { title='Join users and orders in a single filter (no active source needed)' }
iq 'INDEX(source("users"; ".[]"); .id) as $u
    | source("orders"; ".[] | select(.total > 99)")
    | {name: $u[.userId].name, total}'
```

!!! tip "Correlated lookups re-run"

    A `source()` opened inside a stream runs its sub-filter once per element,
    the connection is reused, but the sub-filter re-executes.

    Hoist a constant lookup into a binding `INDEX(source("users"; ".[]"); .id)
    as $u | …` and index `$u` per element instead.

!!! tip "Optimize memory usage"

    Binding `source()` to a jq variable materializes that call's whole result
    set in memory (`jq` indexing needs a concrete array), even though the read
    itself streams.

    Push the reduction into the sub-filter, `source("orders"; ".[] |
    select(.total > 99)")`, not `source("orders"; ".[]")` filtered outside, so
    only the rows you need are held.

    A bare `source("big")` over a large source buys no streaming benefit,
    prefer `iq combine` when each side is large and independent.

### Combine `combine`

`iq combine <source>[=<jq>]... --with <jq> [flags]`

Query several sources and combine their results with one `jq` program.

Each positional is a source spec (`<source>[=<jq>]`) reduced at the source
(bounded reads, streaming scans, and predicate pushdown all still apply). The
spec's results bind to a `jq` variable named after the source, with `/`, `.`
and `-` becoming `_` (so `prod/books=.[]` binds `$prod_books`) and `--with` is
the final program, so it can join, union (`$a + $b`), aggregate, or fan across
any number of sources.

Each source reduces at the source, and a pushable filter pushes down, so this
never copies whole datasets to join them. A spec with no filter binds the whole
keyspace, which is a holistic read, it needs `--unbounded`, exactly as the same
expression would on a plain query.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--no-compile` | ✗ | disable server-side predicate pushdown; run each spec's filter client-side |
| | `--with <string>` | required | final jq over the bound source results (each spec's results bound to $name), run over a null input |

```sh { title='Join users with orders on a shared id, across two sources' }
iq combine 'users=.[] | {id, name}' \
           'orders=.[] | select(.total > 99)' \
   --with '($users | INDEX(.id)) as $u | $orders[] | . + {name: $u[.userId].name}'
```

!!! tip "Reduce, then combine"

    Each spec is evaluated independently and its (already reduced) result is
    held in memory before `--with` runs, so keep a stage's output small with
    `select`/projection/aggregation.

    A spec that must materialize its whole source (`keys`, `.`, `map(...)`)
    still needs `--unbounded`, exactly like a single-source query. A
    `.[]`-rooted spec streams without it.

## Unbounded `--unbounded`

Permit a filter that loads the whole dataset into memory. It also materializes a
`.[]`-rooted filter instead of streaming it, so a filter that collapses the
keyspace into one value (`keys`, `.`, `map(...)`) runs only with it, see
[Read strategies](how-it-works.md#read-strategies).

`iq combine` and `iq data` carry their own copies of the flag where they apply.

## Client-side `--no-compile`

Disable pushdown.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--no-compile` | ✗ | disable predicate pushdown, run the full `.[] | select()` filter client-side |
