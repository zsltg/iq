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
```bash title="One conjunct on the server, one only in jq"
./iq --src orders '.[] | select(.total > 99 and (.active | not))' --explain
```
```bash { title='Plan + live "mongo> find(...)" trace' }
./iq --src orders '.[] | select(.total > 99)' -v
```
```bash title="Trace on stderr, stdout stays pure data"
./iq '.[]' -v 2>/dev/null
```

## Breakdown

The plan shows these parts, syntax-highlighted when the destination is
a terminal:

- **Filter**

    - The `jq` filter pretty-printed with real line breaks. Nested
      `source("name"; "<jq>")` sub-filters and every `iq combine` fragment
      are formatted too.
    - Under `-v`/`--verbose`, each top-level pipe stage also carries
      a short right-aligned note describing it (`— keep inputs where …`). The stage
      that reads from the store is marked with its route, colored by cost.

        - A green `bounded read` (keyed lookup)
        - A yellow `streaming scan` (batched over `.[]`)
        - A red `materialized scan` (an aggregate, a non-`.[]` root, or any scan under
          `--unbounded`)

- **Access Plan**

    - The concrete backend calls each source will make, derived
      from the filter's route (bounded keys, streaming scan, or materialize)

- **Filter stages**

    - A filtered scan runs in up to three stages, in this order. The plan has
      one section per stage. Each section lists only what its stage evaluates.
      The note on the right of a row is the `jq` source of the conjunct. A
      conjunct is one operand of the top-level `and` of a `select(...)`.
    - Server filter: the backend evaluates the conjunct. The row is the
      fragment that the driver sends, as JSON.
    - Prefilter: the client tests the raw bytes of each record against the
      whole compiled predicate, before decode. It drops a record only if it can
      prove that the record does not match. It also tests a conjunct that the
      server declined. The rows use the neutral notation below.
    - jq: the full filter runs again on every decoded record. The section
      lists only the conjuncts that no earlier stage evaluates. Each row starts
      with `only here:` and ends with the reason.
    - A stage has one of four states.

        - `none`: the store does not have the stage. The section is one line
          with the reason.
        - `off`: the store has the stage, but it does not run for this
          predicate. The section is one line with the reason.
        - `on`: the stage runs.
        - `conditional`: the stage runs only if a fact that the plan cannot
          know holds, for example the live Elasticsearch field mapping. The
          header names the fact.

    - A row ends with `not pushed: <reason>` when the server declined the
      conjunct. A conjunct that the compiler cannot express as a provable
      superset gets `not compiled: <reason>` and reaches only `jq`. Examples are
      an inexact negation, a non-portable regex, and an unsafe field name.
    - Under `--unbounded`, a streamable scan reads every record. This is the
      materialized route. The plan then has the `jq` section alone. Every
      conjunct shows `materialized: the scan reads every record`.
    - A bounded read shows its key fetch filter as a JSON block, with no
      conjunct rows.
    - A server fragment that fits in 34 characters prints on one line. A wider
      one breaks into indented lines, with the note on the first line.
    - Filter stages are absent under `--no-compile`.

- **Compiled predicate**

    - The prefilter rows show each compiled node in a short neutral notation.
      It is not `jq` syntax and not the syntax of a backend. It shows what the
      compiler made of the conjunct.
    - A path is the field names joined with dots. A field name that is not a
      plain identifier is a JSON string. An empty path is `@`, the current
      array element inside `any` or `none`.
    - A value is a JSON literal. A composite conjunct prints as a tree, with
      the operator on its own line and its children under it. An `or` with a
      dropped operand shows only the operands that remain.

    | Node | Notation | `jq` source |
    | --- | --- | --- |
    | `Eq` | `author.name == "Kleppmann"` | `.author.name == "Kleppmann"` |
    | `Ne` | `status != "draft"` | `.status != "draft"` |
    | `Cmp` | `year > 2015` (also `>=`, `<`, `<=`) | `.year > 2015` |
    | `Regex` | `title =~ /^go/i` | `.title \| test("^go"; "i")` |
    | `Exists` | `has(isbn)` | `has("isbn")` |
    | `NotExists` | `!has(isbn)` | `has("isbn") \| not` |
    | `Size` | `len(tags) == 3` | `.tags \| length == 3` |
    | `ElemMatch` | `tags any(@ == "go")` | `.tags \| any(. == "go")` |
    | `NoneMatch` | `tags none(@ == "draft")` | `.tags \| any(. == "draft") \| not` |
    | `And`, `Or` | `(a == 1 and b == 2)` | `a and b`, `a or b` |

### Example

The filter below has three conjuncts. This is the plan for Elasticsearch. The
server pushes the equality, depending on the live mapping. It declines the
range, and the prefilter still tests it. The third conjunct does not compile.

```bash
./iq --src logs '.[] | select(.n == 1 and .t > 3 and (.name | ascii_downcase) == "x")' --explain
```

```text
query plan
source: logs (elasticsearch)

jq filter:
  .[]
  | select(
    .n == 1 and .t > 3 and (
      .name
      | ascii_downcase
    ) == "x"
  )

elasticsearch calls:
  _search(<bool>): server-side pre-filter (equality on an exactly-mapped field and existence), then the full jq re-runs client-side
  pages streamed in batches of 100

server filter (elasticsearch bool query, if each equality field is exactly mapped and its literal type matches):
  {"term": {"n": 1}}                — .n == 1

prefilter (rawpred on the raw _source of each hit, before decode):
  and
    n == 1                          — .n == 1
    t > 3                           — .t > 3  (not pushed: declined by backend translator)

jq (after decode, the full filter, every conjunct again):
  only here: (.name | ascii_downcase) == "x"  — not compiled: not a pushable comparison
```

Redis filters nothing on the server, so the prefilter tests the compiled
conjuncts:

```text
query plan
source: cache (redis)

jq filter:
  .[]
  | select(
    .n == 1 and .t > 3 and (
      .name
      | ascii_downcase
    ) == "x"
  )

redis calls:
  SCAN 0 MATCH * COUNT 100: cursor over the whole keyspace
  per page: pipeline TYPE for the page's keys
  then per key by type: GET / HLEN + HSCAN / LLEN + LRANGE / SCARD + SSCAN / ZCARD + ZSCAN / XLEN + XRANGE COUNT / JSON.GET

server filter: none, Redis filters nothing on the server

prefilter (rawpred on the raw bytes of RedisJSON values, before decode):
  and
    n == 1                          — .n == 1
    t > 3                           — .t > 3

jq (after decode, the full filter, every conjunct again):
  only here: (.name | ascii_downcase) == "x"  — not compiled: not a pushable comparison
```

### Structured output

The "query plan" log record and the MCP `iq_explain` result carry the same
data. They have a `server` object and a `prefilter` object, each with `state`,
`scope`, and `note`. Each conjunct has `stages`, `compiled`, `server`,
`skipped`, and `reason`. The `pushed` field is removed.

!!! danger "Redaction"

    Credentials are never traced (for example Redis `AUTH` and the MongoDB auth
    handshake are redacted or skipped).

!!! info "Explain is a dry run"

    `--explain` never opens a connection, so it works offline against any saved
    source.

!!! note "Verbosity"

    Under `-v`, the live trace shows the actual commands (`redis> TYPE …`,
    `mongo> find …`) as they run.
