---
icon: material/flag-outline
---

# Global flags

Flags every `iq` command accepts. The query-only flags (`--unbounded`,
`--explain`, `--no-compile`, `--from-format` and the
[output](output.md#output) and [write](write-data.md#write-data) families)
live on the query command alone; `iq combine` and `iq data` carry their own
copies of `--unbounded`/`--explain` where they apply.

## Config `--config`

Path to the config file (overrides `IQ_CONFIG`, default is
`<user_config_dir>/iq/iq.toml`).

## Source `--src`

Run against this saved source for one invocation (overrides the active source,
see [Sources](sources.md#sources)).

## Timeout `--timeout`

Timeout for the whole operation, a query, a diff, a combine, or an `--insert`
copy (default is `5s`).

## Output `--output`

Write output to a file instead of stdout (`-o`); progress and errors stay on
stderr, and color turns off for the file unless `-C` forces it.

## Color `--color`, `--monochrome`

`-C`/`--color` forces colored output even when the destination is not a
terminal; `-M`/`--monochrome` disables it (also honored via `NO_COLOR`). Color
is on by default only when writing to a terminal.

## Decimals `--format.decimal`

How a non-integer decimal is presented to the filter: `auto` (exact string
where the backend is exact, number where it is not), `number` (bare, may lose
precision), or `string` (exact, quoted; use `tonumber`). It changes what the
filter computes on, not just the print, see
[Output](output.md#numbers-formatdecimal).

## Help `--help`

Display help on the command line for `iq`.

## Version `--version`

Prints the bare version and exits, with no name or prefix, so a script can use
it directly (`v=$(iq --version)`): `v1.2.3` for a release build, the
git-describe form (`v1.2.3-14-gabc1234`) for an untagged build, and
`dev+<commit>` only for a plain `go build` with no version metadata at all.

It always writes to stdout, unaffected by `--output`. For the human form
(version, commit, build date, Go version) run `iq version`.

## No progress `--no-progress`

Disable the scan progress spinner (shown on stderr for long scans when it is a
terminal).

## No cache `--no-cache`

Disable the `file://` dump decode cache (on by default for local dump files
above 4 MiB, a cached decode skips re-parsing the dump on later queries).

## No index `--no-cache-index`

Skip the `file://` cache's per-page key index (still caches the decode, a
bounded key read streams the whole cache instead of decoding only candidate
pages).

## Verbose `--verbose`

Verbose output. For a query, the formatted plan and a live backend command
trace on stderr (disables the progress spinner); for a listing (`iq ls`,
`iq config ls`, `iq driver ls`), extra columns.

## Logging and errors `--log*`, `--error*`, `--debug.pprof`

File logging, error rendering, and profiling are covered on
[Diagnostics & Logging](diagnostics-and-logging.md).

## Query flags

These apply to the query command only.

### Unbounded `--unbounded`

Permit a filter that loads the whole dataset into memory (also materializes a
`.[]`-rooted filter instead of streaming it). `iq combine` and `iq data` carry
their own copies.

### Explain `--explain`

Print the formatted query plan and exit without opening any connection or
executing, see [Query plan](query-plan.md#query-plan).

### Client-side `--no-compile`

Disable predicate pushdown and run the full `.[] | select()` filter
client-side; results are unchanged either way, see
[Drivers](drivers.md#drivers).

### From format `--from-format`

Format of a piped-stdin source when its content cannot be sniffed: `jsonl`,
`yaml`, `mongoexport`, `bson`, `rdb`, `dynamodb-json`, `cassandra-csv`, or
`neo4j-json` (aliases like `json` are accepted), see
[Write data](write-data.md#write-data).
