---
icon: material/magnify
---

# Query data

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as pretty JSON by default (see [Output formats](output.md) to change it; these run
against the active source — see [Sources](sources.md)):

```bash
iq '.greeting'                          # fetch key "greeting"
iq '.["book:1"]'                        # a key containing a colon needs bracket-quoting
iq '.["book:1"].title'                  # fetch book:1, extract one field
iq '[ .["book:1"].title, .["book:2"].title ]'   # fetch both keys, project a field from each
iq '.["book:2"].price | tonumber + 5'   # values are strings; convert before arithmetic
```

Always wrap the filter in single quotes. jq syntax is full of characters the shell would
otherwise expand or split — brackets (`[ ]`), whitespace, `|`, `*`, `$` — and bracket-quoting a
colon key like `.["book:1"]` reads as a glob to zsh (`no matches found`) or bash unless quoted.

# Cross-source queries

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

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
