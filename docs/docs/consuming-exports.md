# Consuming iq exports

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

`iq --jsonl` writes one JSON document per line — JSON Lines, the format every dataframe tool
reads directly. iq's normalization is what makes that read clean: one canonical rendering per
value, exact integers, decimal strings, RFC3339Nano UTC timestamps, base64 binary, and an
explicit `null` for an absent field rather than a placeholder. This section is the consumer's
side — loading an export into a data-science stack without losing that fidelity. The whys below
were checked against pandas 3.0, Polars 1.42, and DuckDB 1.5.

### pandas

```python
import pandas as pd
df = pd.read_json("dump.jsonl", lines=True, dtype_backend="pyarrow")
```

Pass `dtype_backend="pyarrow"`, not the default. pandas' default NumPy dtypes have no nullable
integer, so the first `null` in an integer column silently widens the whole column to `float64` —
`7` becomes `7.0` and any exact integer past `2^53` is corrupted before you look. The Arrow-backed
path keeps a typed, null-safe `int64[pyarrow]` (missing values read as `pd.NA`, not `NaN`), and
decimal strings and RFC3339Nano stay strings you can lift to exact types:

```python
import pyarrow as pa
df["amount"] = df["amount"].astype(pd.ArrowDtype(pa.decimal128(20, 2)))   # exact decimal
df["ts"] = df["ts"].astype("timestamp[ns, tz=UTC][pyarrow]")             # nanosecond UTC
```

pandas still labels `pd.NA` semantics experimental — prefer the Arrow backend, but pin your
pandas version rather than depend on the exact behaviour.

### Polars

```python
import polars as pl
df = pl.read_ndjson("dump.jsonl")
df.null_count()   # O(1) per column, tracked in the validity bitmap
```

Polars has one missing value — `null` — uniform across every type. `NaN` is a float *value*, not
missingness, and iq never emits `NaN` for an absent field, so `mean`, `min`, and `null_count` stay
honest on iq output: a gap is a `null` that statistics skip, never a `NaN` that poisons the result.

### DuckDB

```sql
SELECT * FROM read_json_auto('dump.jsonl');
```

`read_json_auto` (aka `read_json`) infers a type per field and shreds nested documents into
`STRUCT`/`LIST` you query with dot- and list-access. Two edges of iq's output to know:

- **Timestamps need an explicit cast.** DuckDB's type sniffer accepts fractional seconds only to
  millisecond precision, so iq's nanosecond RFC3339Nano strings (`2026-07-18T12:34:56.123456789Z`)
  are inferred as `VARCHAR`, not `TIMESTAMP` (observed on DuckDB 1.5.4 — verify in your version).
  Cast to `TIMESTAMP_NS` to keep the nanoseconds; plain `TIMESTAMP` truncates to microseconds:

  ```sql
  SELECT CAST(ts AS TIMESTAMP_NS) AS ts FROM read_json_auto('dump.jsonl');
  ```

- **The UNNEST trap.** `UNNEST` of an empty *or* `null` list yields zero rows, so a naive
  `SELECT id, UNNEST(tags) …` silently drops every record whose list is empty or null. Keep them
  with a lateral left join back:

  ```sql
  SELECT j.id, u.tag
  FROM read_json_auto('dump.jsonl') j
  LEFT JOIN LATERAL UNNEST(j.tags) AS u(tag) ON true;
  ```

### Splink (entity resolution)

[Splink](https://moj-analytical-services.github.io/splink/) needs a per-record `unique_id`, column
names that conform across the sources you link, dates truncated to `yyyy-mm-dd`, and — critically —
*true nulls*, never empty-string placeholders. iq's export already fits: the key rides in each
record (a ready `unique_id`) and iq emits an explicit `null` for an absent field. Prepare a source
for Splink with the jq filter — reshape, re-key, and truncate dates in one pass:

```bash
iq '.[] | {unique_id: .id, name, dob: (.created_at | .[0:10])}' --src people --jsonl
```

For an entity-resolution consumer the choice between *omitting* a field and writing an explicit
`null` is load-bearing — they are different inputs to the match. Export explicit nulls (a jq object
constructor like `{name}` already writes `null` for a missing field); see
[Null vs missing](architecture.md#null-vs-missing) for how iq draws that line at each layer.
