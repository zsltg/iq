---
icon: lucide/palette
---

# Output

Results print as pretty JSON by default. A single format flag selects another rendering.

The flags are mutually exclusive. They apply to the jq read path and to `iq combine`, but not to
`exec` (which prints the backend's native reply).

## Format flags

Shorthand flags are available for most formats.

`-f`, `--format <name>` selects the same renderings by name (`json`, `jsonl`,
`jsona`, `yaml`, `values` with the alias `raw`, `gron`, `grona`, `parquet`).
It is mutually exclusive
with them, so `-f json --jsonl` is rejected.

`parquet` has no shorthand flag. It is a binary columnar format, selected by name only.

### JSON `--json`

Pretty JSON, one value per result (default). Shorthand `-j`.

```
iq '.[]' -j
```
```
{
  "_id": "1",
  "author": "Donovan and Kernighan",
  "price": 39,
  "tags": [
    "go",
    "programming"
  ],
  "title": "The Go Programming Language",
  "year": 2015
}
{
  "_id": "2",
  "author": "Martin Kleppmann",
  "price": 45,
  "tags": [
    "data",
    "architecture"
  ],
  "title": "Designing Data-Intensive Applications",
  "year": 2017
}
```

### JSON Lines `--jsonl`

Compact JSON, one value per line. Shorthand `-J`.

```
iq '.[]' -J
```
```
{"_id":"1","author":"Donovan and Kernighan","price":39,"tags":["go","programming"],"title":"The Go Programming Language","year":2015}
{"_id":"2","author":"Martin Kleppmann","price":45,"tags":["data","architecture"],"title":"Designing Data-Intensive Applications","year":2017}
```

### JSON Array `--jsona`

Every result wrapped in one array `[ ... ]`. Shorthand `-A`.

```
iq '.[]' -A
```
```
[
  {
    "_id": "1",
    "author": "Donovan and Kernighan",
    "price": 39,
    "tags": [
      "go",
      "programming"
    ],
    "title": "The Go Programming Language",
    "year": 2015
  },
  {
    "_id": "2",
    "author": "Martin Kleppmann",
    "price": 45,
    "tags": [
      "data",
      "architecture"
    ],
    "title": "Designing Data-Intensive Applications",
    "year": 2017
  }
]
```

!!! info "`iq --jsona` differs from `sq --jsona`"

    `iq --jsona` wraps the whole result stream in one array (like `jq -s`). It
    is the analogue of `sq --json`.

    `sq --jsona` instead emits one JSON array *per row* with the keys dropped.
    This is a columnar projection that a heterogeneous `jq` value stream has no
    exact analogue for. As a result, `iq` keeps the `sq` flag name but its own
    behaviour.

### Raw `--raw`

Unquoted scalars, one per line. Objects and arrays fall back to compact JSON.
Shorthand `-r`.

```
iq '.[].title' -r
```
```
The Go Programming Language
Designing Data-Intensive Applications
```

### YAML `--yaml`

YAML documents, separated by `---`. Shorthand `-y`.

```
iq '.[]' -y
```
```
_id: "1"
author: Donovan and Kernighan
price: 39
tags:
    - go
    - programming
title: The Go Programming Language
year: 2015
---
_id: "2"
author: Martin Kleppmann
price: 45
tags:
    - data
    - architecture
title: Designing Data-Intensive Applications
year: 2017
```

### gron `--gron`

Flattened `json.path = value;` assignment statements, one per line. Shorthand `-g`.

It is greppable and reversible with `gron --ungron`[^1]. Each result is rooted at a repeated `json`.

```
iq '.[]' -g
```
```
json = {};
json._id = "1";
json.author = "Donovan and Kernighan";
json.price = 39;
json.tags = [];
json.tags[0] = "go";
json.tags[1] = "programming";
json.title = "The Go Programming Language";
json.year = 2015;
json = {};
json._id = "2";
json.author = "Martin Kleppmann";
json.price = 45;
json.tags = [];
json.tags[0] = "data";
json.tags[1] = "architecture";
json.title = "Designing Data-Intensive Applications";
json.year = 2017;
```

!!! note "Paths"

    `--gron` (and `--grona`) emit one `path = <compact JSON>;` statement per
    line, object keys sorted.

    A key that is an ASCII identifier(`^[A-Za-z_$][A-Za-z0-9_$]*$`) follows
    a bare dot (`json.name`). Any other key is bracketed and JSON-quoted
    (`json["odd key"]`). This is a deliberate ASCII subset of gron's rule,
    because over-quoting stays ungron-safe.

    `--gron` repeats the `json` root for every result, so ungron[^1] is
    last-write-wins across results.

!!! warning "Typed dumps are not supported"

    `--typed` rejects `--gron` and `--grona`. A flattened assignment stream is
    a rendering to grep, not a dump. No source re-imports it. To gron the value
    stream, drop `--typed`. To dump instead, use `--jsonl` (default), `--json`,
    `--jsona`, or `--yaml`.

### gron Array `--grona`

Like `--gron` but result N roots at `json[N]`, so the whole stream ungrons[^1] back
to one JSON array (gron's `--stream` style). Shorthand `-G`.

```
iq '.[]' -G
```
```
json = [];
json[0] = {};
json[0]._id = "1";
json[0].author = "Donovan and Kernighan";
json[0].price = 39;
json[0].tags = [];
json[0].tags[0] = "go";
json[0].tags[1] = "programming";
json[0].title = "The Go Programming Language";
json[0].year = 2015;
json[1] = {};
json[1]._id = "2";
json[1].author = "Martin Kleppmann";
json[1].price = 45;
json[1].tags = [];
json[1].tags[0] = "data";
json[1].tags[1] = "architecture";
json[1].title = "Designing Data-Intensive Applications";
json[1].year = 2017;
```

!!! note "Paths"

    `--grona` roots result **N** at `json[N]` under a leading `json = [];`, so
    ungron[^1] rebuilds the full array (an empty stream ungrons[^1] to `[]`, like
    `--jsona`).

### Parquet `--format parquet`

Streams the result values to an [Apache Parquet](https://parquet.apache.org/)
file ([Apache Arrow](https://arrow.apache.org/) columnar format). Parquet is the
bridge to [pandas](https://pandas.pydata.org/), [Polars](https://pola.rs/),
[DuckDB](https://duckdb.org/), and the wider data-science ecosystem.

Because it is binary, `iq` refuses to write it to a terminal. Redirect it or use `-o out.parquet`. A pipe or file is required.

```
iq '.[]' --format parquet -o out.parquet
```
```
iq '.[]' --format parquet > out.parquet
```
```bash
iq '.[]' --format parquet | python3 -c "
import sys, pyarrow.parquet as pq, io
table = pq.read_table(io.BytesIO(sys.stdin.buffer.read()))
print(table)
"
```

!!! note "Schema"

    The schema is inferred from the first 1000 result values (schema-inference sample). It is
    projected onto Arrow types:

    - `integer→int64`
    - `number→float64`
    - `boolean→bool`
    - `string→utf8`
    - A `date-time` string→`timestamp[ns, UTC]`
    - A `date` string→`date32`
    - `object→struct`
    - `array→list`
    - An id-keyed map→`map<utf8, T>`.

    A column whose sampled shape is heterogeneous or
    null-only falls back to the `arrow.json` canonical extension (utf8 storage holding byte-lossless
    canonical JSON), marked in field metadata.

    The Arrow schema is embedded in the file (`ARROW:schema`), so exact types survive a read-back.
    A value that does not fit its inferred column type past the sample fails the export, naming
    the column. For fully heterogeneous data, switch to `--format jsonl` rather than coercing.

!!! info "Presence caveat"

    Arrow's validity bitmaps cannot distinguish a *missing* field from a field
    present as `null`. Both collapse to a null in the column.

    `iq` preserves the distinction inferred from the sample in field metadata
    (`iq:presence` = `required` | `optional`), so it survives in the schema
    even though the values collapse.

!!! warning "Typed dumps are not supported"

    `--typed` dumps cannot use `parquet` (they carry a `{key,type,value}`
    envelope). Run the query without `--typed` to export a columnar file.

## Compact `--compact`

Collapses the pretty renderings to single-line: `--json` becomes one compact
value per line (equivalent to `--jsonl`) and `--jsona` becomes a single-line
`[ ... ]`.

It is a no-op for `--jsonl`, `--raw`, `--yaml`, `--gron`, `--grona`, and
`--format parquet`, which are already condensed or binary (`--gron` and
`--grona` are inherently line-based).

## File `--output <file>`

`--output` :material-earth:{ title="Global flag" } is global. Every command honours it (for example
`iq inspect -o report.json`). See [Global flags](global-flags.md#global-flags).

Writes results to `<file>` instead of stdout, truncating an existing file, with the
shorthand `-o`. It is orthogonal to the format flags.

Color is off for a file unless you force it with `-C`. Progress and errors
still go to stderr.

## Numbers `--format.decimal`

`--format.decimal` :material-earth:{ title="Global flag" } is global.

`--format.decimal <auto|number|string>` chooses how a **non-integer decimal**
from the backend is presented to the filter.

| value | behavior |
| --- | --- |
| `auto` (default) | each backend keeps its faithful form. MongoDB `Decimal128` is an exact string. A Redis fractional number is a `float64` |
| `number` | decimals become bare numbers (`float64`), convenient for arithmetic but lossy beyond `float64` |
| `string` | decimals become their exact literal as a string, precision-safe. Use `tonumber` to compute |

```bash { title='"19.99" — exact, precision-safe' }
./iq '.book.price' --format.decimal=string
```
```bash title="Compute on the exact decimal"
./iq '.book.price | tonumber * 1.2'
```
```bash title="Bare numbers, ready for jq arithmetic"
./iq '.[].price' --format.decimal=number
```

!!! warning

    Because the `jq` filter runs client-side over the fetched value, this choice is
    made at normalization time. It changes what the filter computes on, not only
    how the result prints (unlike `sq`, where `jq` is not involved).

!!! note

    Integers are always exact regardless of the mode. They arrive as an `int`, or
    as a big integer when they exceed 64 bits. As a result, `.count + 1` stays
    exact rather than rounding through `float64`.

    A backend can round before `iq` sees the value. For example, RedisJSON stores
    an integer larger than 64 bits as a double, so it arrives already in scientific
    notation.

    A big integer renders as a bare number in the JSON formats but as a quoted
    string under `--yaml` (a `yaml.v3` limitation). Exactness is kept in
    preference to YAML's numeric form.

## Color `--color`

`--color` :material-earth:{ title="Global flag" }, shorthand `-C`, is global.

Output is syntax-highlighted when `iq` writes to a terminal. It is left plain when
it is piped or redirected, so captured output stays free of color codes. TTY detection is
where capture safety comes from.

```bash title="Colored on a terminal, plain when piped"
./iq '.[]'
```
```bash title="Never colored"
./iq '.[]' -M
```
```bash title="Keep color through a pager"
./iq '.[]' -C | less -R
```

!!! note "Rendering"

    Every rendering syntax-highlights on a terminal (`--json`, `--jsonl`,
    `--jsona`, `--yaml`, `--raw`, `--gron`, `--grona`). Under `--raw`, strings
    and nulls still print bare and uncolored, keeping shell substitution exact.

    The human commands color their signal too:

    - `ping` shows `ok`/`error` in green/red.
    - `diff` shows additions green, removals red, and changes yellow.
    - `ls`/`inspect` highlight the active source and section headers.

    The raw reply bodies from `exec` and `inspect` are colored in their native
    form. For example, MongoDB gets JSON syntax highlighting, and Redis gets
    redis-cli-style value tokens.

## No color `--monochrome`

`--monochrome` :material-earth:{ title="Global flag" }, shorthand `-M`, is global.

Disable colored output. Color is on by default only when writing to a terminal.

!!! tip "NO_COLOR"

    Colored output is also disabled if the `NO_COLOR` environment variable is
    set. `-C` forces colored output and overrides `NO_COLOR`.

[^1]: `gron` flattens JSON into one `json.path = value;` assignment per line, so it can be grepped. `gron --ungron` reverses that and rebuilds the JSON from the assignments. This is what makes `--gron` output round-trippable. https://github.com/tomnomnom/gron#ungronning
