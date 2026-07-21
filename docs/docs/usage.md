# Usage

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as pretty JSON by default (see [Output formats](#output-formats) to change it; these run
against the active source — see [Sources](sources.md)):

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
`yaml`, `values` (with `raw` as an alias for `values`), `gron`, `grona`, `parquet` — as an
alternative to the shorthand flags above. It is mutually exclusive with them, so `-f json --jsonl`
is rejected. `parquet` has no shorthand flag: it is a binary columnar format, selected by name only.

> **Parquet export (`--format parquet`).** Streams the result values to an Apache Parquet file
> (Apache Arrow columnar format) — the bridge to pandas, Polars, DuckDB, and the wider
> data-science ecosystem. Because it is binary, iq refuses to write it to a terminal: redirect it
> (`iq '.[]' --format parquet > out.parquet`) or use `-o out.parquet`; a pipe or file is required.
> The schema is inferred from the first 1000 result values (iq's schema-inference sample) and
> projected onto Arrow types: `integer→int64`, `number→float64`, `boolean→bool`, `string→utf8`,
> a `date-time` string→`timestamp[ns, UTC]`, a `date` string→`date32`, `object→struct`,
> `array→list`, an id-keyed map→`map<utf8, T>`. A column whose sampled shape is heterogeneous or
> null-only falls back to the `arrow.json` canonical extension (utf8 storage holding byte-lossless
> canonical JSON), marked in field metadata. The Arrow schema is embedded in the file
> (`ARROW:schema`), so exact types survive a read-back. A value that does not fit its inferred
> column type past the sample fails the export naming the column — switch to `--format jsonl` for
> fully heterogeneous data rather than coercing.
>
> **Presence caveat.** Arrow's validity bitmaps cannot distinguish a *missing* field from a field
> present as `null` — both collapse to a null in the column. iq preserves the distinction inferred
> from the sample in field metadata (`iq:presence` = `required` | `optional`), so it survives in
> the schema even though the values collapse. `--typed` dumps cannot use `parquet` (they carry a
> `{key,type,value}` envelope); run the query without `--typed` to export a columnar file.

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
redirected, so captured output stays clean — that TTY detection is where capture safety comes
from. Every rendering syntax-highlights on a terminal: `--json`, `--jsonl`, `--jsona`, `--yaml`,
and now `--raw`, `--gron`, and `--grona` too. Under `--raw`, strings and nulls still print bare
and uncolored, keeping shell substitution exact. The human commands color their signal too: `ping` shows `ok`/`error` in
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
| `-v`, `--verbose` | off | print diagnostics (source resolved, store opened, query complete with scan count and elapsed) to stderr, plus the [query plan](#query-plan-explain-v) and a live backend command trace (disables the progress spinner) |
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
shows four things, syntax-highlighted when the destination is a terminal:

- the jq filter, pretty-printed with real line breaks (nested `source("name"; "<jq>")` sub-filters
  and every `--from`/`--combine` fragment are formatted too);
- the backend **access plan** — the concrete calls each source will make, derived from the filter's
  route (bounded keys, streaming scan, or materialize): MongoDB `find(<filter>)` (the pushed-down
  filter, on by default, or `{}` under `--no-compile`) or `find` by `_id`; Redis `SCAN 0 MATCH *
  COUNT n` plus per-key `TYPE`/typed reads (naming the client-side raw-byte prefilter when a
  predicate compiles), or pipelined typed reads for bounded keys;
- the **pushdown** breakdown — one line per top-level `select(...)` conjunct saying whether the
  backend evaluates it (`pushed`) or it re-runs client-side (`client-side`), and why a client-side
  conjunct did not push. A conjunct is `client-side` when the compiler cannot express it as a
  provable superset (an inexact negation, a non-portable regex, an unsafe field name, or any other
  unpushable construct), when the backend's translator declines the compiled predicate (a range on
  Elasticsearch, say), or when the source does no server-side filtering at all (the read-only file
  driver). This is the observable split of what the backend evaluated versus what ran client-side;
  it is absent under `--no-compile`;
- the compiled filter as JSON — the merged fragment of the pushed conjuncts: MongoDB's server-side
  `find` filter, or the predicate Redis's client-side prefilter applies to RedisJSON values (empty
  under `--no-compile`, or when no conjunct pushes).

```bash
./iq --src orders --explain '.[] | select(.total > 99) | {id, total}'
./iq --src cache --explain '.[] | select(.active)'   # Redis SCAN + typed reads
./iq --src orders --explain '.[] | select(.total > 99 and (.active | not))'  # one pushed, one client-side
./iq --src orders -v '.[] | select(.total > 99)'     # plan + live `mongo> find(...)` trace
./iq -v '.[]' 2>/dev/null                            # trace on stderr; stdout stays pure data
```

Under `-v`, the live trace shows the actual commands (`redis> TYPE …`, `mongo> find …`) as they run;
credentials are never traced (Redis `AUTH` and the MongoDB auth handshake are redacted or skipped).
`--explain` never opens a connection, so it works offline against any saved source.
