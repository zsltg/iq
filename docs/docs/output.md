---
icon: lucide/palette
---

# Output

Results print as pretty JSON by default. A single format flag selects another rendering.

The flags are mutually exclusive and apply to the jq read path and to `iq combine`, but not to
`exec` (which prints the backend's native reply).

## Format flags

Shorthand flags are available for most format.

`-f`, `--format <name>` selects the same renderings by name (`json`, `jsonl`,
`jsona`, `yaml`, `raw`, `gron`, `grona`, `parquet`). It is mutually exclusive
with them, so `-f json --jsonl` is rejected.

`parquet` has no shorthand flag, it is a binary columnar format, selected by name only.

### JSON `-j`, `--json`

Pretty JSON, one value per result (default).

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

### JSON Lines `-J`, `--jsonl`

Compact JSON, one value per line.

```
iq '.[]' -J
```
```
{"_id":"1","author":"Donovan and Kernighan","price":39,"tags":["go","programming"],"title":"The Go Programming Language","year":2015}
{"_id":"2","author":"Martin Kleppmann","price":45,"tags":["data","architecture"],"title":"Designing Data-Intensive Applications","year":2017}
```

### JSON Array `-A`, `--jsona`

Every result wrapped in one array `[ ... ]`.

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

    `iq --jsona` wraps the whole result stream in one array (like `jq -s`),
    it is the analogue of `sq --json`.

    `sq --jsona` instead emits one JSON array *per row* with the keys dropped,
    a columnar projection that a heterogeneous `jq` value stream has no honest
    analogue for, so `iq` keeps the `sq` flag name but its own behaviour.

### Raw `-r`, `--raw`

Unquoted scalars, one per line. Objects and arrays fall back to compact JSON.

```
iq '.[]' -r
```
```
{"_id":"1","author":"Donovan and Kernighan","price":39,"tags":["go","programming"],"title":"The Go Programming Language","year":2015}
{"_id":"2","author":"Martin Kleppmann","price":45,"tags":["data","architecture"],"title":"Designing Data-Intensive Applications","year":2017}
```

### YAML `-y`, `--yaml`

YAML documents, separated by `---`.

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

### gron `-g`, `--gron`

Flattened `json.path = value;` assignment statements, one per line.

Greppable and reversible with `gron --ungron`[^1], each result rooted at a repeated `json`.

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
    a bare dot (`json.name`), any other key is bracketed and JSON-quoted
    (`json["odd key"]`), a deliberate ASCII subset of gron's rule, since
    over-quoting stays ungron-safe.

    `--gron` repeats the `json` root for every result, so ungron[^1] is
    last-write-wins across results.

### gron Array `-G`, `--grona`

Like `--gron` but result N roots at `json[N]`, so the whole stream ungrons[^1] back
to one JSON array (gron's `--stream` style).

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
file ([Apache Arrow](https://arrow.apache.org/) columnar format), the bridge
to [pandas](https://pandas.pydata.org/), [Polars](https://pola.rs/),
[DuckDB](https://duckdb.org/), and the wider data-science ecosystem.

Because it is binary, `iq` refuses to write it to a terminal. Redirect it or use `-o out.parquet`, a pipe or file is required.

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

    The schema is inferred from the first 1000 result values (schema-inference sample) and
    projected onto Arrow types: `integer→int64`, `number→float64`, `boolean→bool`, `string→utf8`,
    a `date-time` string→`timestamp[ns, UTC]`, a `date` string→`date32`, `object→struct`,
    `array→list`, an id-keyed map→`map<utf8, T>`. A column whose sampled shape is heterogeneous or
    null-only falls back to the `arrow.json` canonical extension (utf8 storage holding byte-lossless
    canonical JSON), marked in field metadata.

    The Arrow schema is embedded in the file (`ARROW:schema`), so exact types survive a read-back.
    A value that does not fit its inferred column type past the sample fails the export naming
    the column — switch to `--format jsonl` for fully heterogeneous data rather than coercing.

!!! info "Presence caveat"

    Arrow's validity bitmaps cannot distinguish a *missing* field from a field
    present as `null`, both collapse to a null in the column.

    `iq` preserves the distinction inferred from the sample in field metadata
    (`iq:presence` = `required` | `optional`), so it survives in the schema
    even though the values collapse.

!!! warning "Typed dumps are not supported"

    `--typed` dumps cannot use `parquet` (they carry a `{key,type,value}`
    envelope); run the query without `--typed` to export a columnar file.

## Compact `--compact`

Collapses the pretty renderings to single-line: `--json` becomes one compact
value per line (equivalent to `--jsonl`) and `--jsona` becomes a single-line
`[ ... ]`.

It is a no-op for `--jsonl`, `--raw`, `--yaml`, `--gron`, and `--grona`, which
are already condensed (`--gron` and `--grona` are inherently line-based).

## File `-o`, `--output <file>`

Writes results to `<file>` instead of stdout, truncating an existing file.

It is global, every command honours it (for example `iq inspect -o report.json`)
and is orthogonal to the format flags.

Color is off for a file unless you force it with `-C`, progress and errors
still go to stderr.

## Numbers `--format.decimal`

`--format.decimal <auto|number|string>` chooses how a **non-integer decimal**
from the backend is presented to the filter.

| value | behavior |
| --- | --- |
| `auto` (default) | each backend keeps its faithful form: MongoDB `Decimal128` is an exact string, a Redis fractional number is a `float64` |
| `number` | decimals become bare numbers (`float64`), convenient for arithmetic but lossy beyond `float64` |
| `string` | decimals become their exact literal as a string, precision-safe, use `tonumber` to compute |

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
    made at normalization time, it changes what the filter computes on, not just
    how the result prints (unlike `sq`, where `jq` is not involved).

!!! note

    Integers are always exact regardless of the mode, they arrive as an `int` or
    a big integer when they exceed 64 bits, so `.count + 1` stays exact rather than
    rounding through `float64`.

    A backend may round before `iq` sees the value, RedisJSON, for example, stores
    an integer larger than 64 bits as a double, so it arrives already in scientific
    notation.

    A big integer renders as a bare number in the JSON formats but as a quoted
    string under `--yaml` (a `yaml.v3` limitation), exactness is kept in preference
    to YAML's numeric form.

## Color `-C`, `--color`

Output is syntax-highlighted when `iq` writes to a terminal and left plain when
it is piped or redirected, so captured output stays clean, TTY detection is
where capture safety comes from.

| control | effect |
| --- | --- |
| (default) | color on only when the destination is a terminal |
| `-M`, `--monochrome` | force color off |
| `-C`, `--color` | force color on, even into a pipe or pager |
| `NO_COLOR` env (any value) | color off unless overridden by `-C` |

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

    The human commands color their signal too: `ping` shows `ok`/`error` in
    green/red, `diff` shows additions green, removals red, and changes yellow,
    and `ls`/`inspect` highlight the active source and section headers.

    The raw reply bodies from `exec` and `inspect` are colored in their native
    form, for example JSON syntax highlighting for MongoDB, and redis-cli-style
    value tokens for Redis.

[^1]: https://github.com/tomnomnom/gron#ungronning
