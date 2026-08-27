---
icon: material/clipboard-list-outline
---

# Query Plan

`--explain` prints a formatted **query plan** and exits without connecting or
executing.

`-v`/`--verbose` prints the same plan to stderr, then runs, tracing each
backend command.

## Examples

```bash title="Plan only, no connection"
./iq --src orders '.[] | select(.total > 99) | {id, total}' --explain
```
```bash title="Annotated plan, a note per pipe stage"
./iq --src orders '.[] | select(.total > 99) | {id, total}' --explain -v
```
```bash title="Redis SCAN + typed reads"
./iq --src cache '.[] | select(.active)' --explain
```
```bash title="One pushed, one client-side"
./iq --src orders '.[] | select(.total > 99 and (.active | not))' --explain
```
```bash { title='Plan + live "mongo> find(...)" trace' }
./iq --src orders '.[] | select(.total > 99)' -v
```
```bash title="Trace on stderr, stdout stays pure data"
./iq '.[]' -v 2>/dev/null
```

## Breakdown

The plan shows four things, syntax-highlighted when the destination is
a terminal:

- **Filter**

    - The `jq` filter pretty-printed with real line breaks, nested
      `source("name"; "<jq>")` sub-filters and every `iq combine` fragment
      are formatted too.
    - Under `-v`/`--verbose` each top-level pipe stage also carries
      a short right-aligned note describing it (`— keep inputs where …`), and the stage
      that reads from the store is marked with its route, colored by cost.

        - A green `bounded read` (keyed lookup)
        - A yellow `streaming scan` (batched over `.[]`)
        - A red `materialized scan` (an aggregate, a non-`.[]` root, or any scan under
          `--unbounded`)

- **Access Plan**

    - The concrete backend calls each source will make, derived
      from the filter's route (bounded keys, streaming scan, or materialize)

- **Pushdown**

    - The breakdown, one line per top-level `select(...)` conjunct saying
      whether the backend evaluates it (`pushed`) or it re-runs client-side
      (`client-side`), and why a client-side conjunct did not push.
    - A conjunct is `client-side` when the compiler cannot express it as
      a provable superset (an inexact negation, a non-portable regex, an unsafe
      field name, or any other unpushable construct), when the backend's
      translator declines the compiled predicate (a range on Elasticsearch,
      say), or when the source does no server-side filtering at all (the
      read-only file driver).
    - This is the observable split of what the backend evaluated versus what
    ran client-side, it is absent under `--no-compile`.

- **Compiled Filter**

    - As JSON, the merged fragment of the pushed conjuncts.

!!! danger "Redaction"

    Credentials are never traced (for example Redis `AUTH` and the MongoDB auth
    handshake are redacted or skipped).

!!! info "Explain is a dry run"

    `--explain` never opens a connection, so it works offline against any saved
    source.

!!! note "Verbosity"

    Under `-v`, the live trace shows the actual commands (`redis> TYPE …`,
    `mongo> find …`) as they run.
