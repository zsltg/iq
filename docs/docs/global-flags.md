---
icon: material/flag-outline
---

# Global flags

Flags every `iq` command accepts, a query, `iq inspect`, `iq diff`, `iq data drop`
alike.

Each one is documented on the page that owns its subject, and carries a
:material-earth:{ title="Global flag" } there to mark that it works everywhere.

| short :material-flag-outline: | long :material-flag-outline: | documented in |
| --- | --- | --- |
| `-s` | `--src` | [Sources](sources.md#showset-src) |
| | `--config` | [Configuration](configuration.md#configuration-config) |
| `-o` | `--output` | [Output](output.md#file-output-file) |
| `-C` | `--color` | [Output](output.md#color-color) |
| `-M` | `--monochrome` | [Output](output.md#no-color-monochrome) |
| | `--format.decimal` | [Output](output.md#numbers-formatdecimal) |
| | `--no-cache` | [Drivers](drivers.md#file-dumps) |
| | `--no-cache-index` | [Drivers](drivers.md#file-dumps) |
| | `--no-progress` | [Diagnostics & Logging](diagnostics-and-logging.md#flags) |
| | `--log*` | [Diagnostics & Logging](diagnostics-and-logging.md#flags) |
| | `--error*` | [Diagnostics & Logging](diagnostics-and-logging.md#flags) |
| | `--debug.pprof` | [Diagnostics & Logging](diagnostics-and-logging.md#flags) |
| `-v` | `--verbose` | [Diagnostics & Logging](diagnostics-and-logging.md#flags), plus [Query plan](query-plan.md#query-plan) for the formatted plan and [Sources](sources.md#list-ls) for the extra listing columns |

The three below have no topic page of their own.

## Timeout `--timeout`

Timeout for the whole operation, a query, a diff, a combine, or an `--insert`
copy (default is `5s`).

## Help `--help`

Display help on the command line for `iq`.

## Version `--version`

Prints the bare version and exits, with no name or prefix. As a result, a script
can use it directly (`v=$(iq --version)`). The version has one of these forms:

- `v1.2.3` for a release build
- The git-describe form (`v1.2.3-14-gabc1234`) for an untagged build
- `dev+<commit>` only for a plain `go build` with no version metadata at all.

It always writes to stdout, unaffected by `--output`. For the human form
(version, commit, build date, Go version), run `iq version`.

## Not global

The query command carries flags of its own, `--unbounded` and `--no-compile` on
[Query data](query-data.md#query-data).

`--explain` on [Query plan](query-plan.md#query-plan).

`--from-format` on [Write data](write-data.md#write-data) and the output-format
and write families on their own pages.

`iq combine` and `iq data` carry their own copies of `--unbounded`/`--explain`
where they apply.
