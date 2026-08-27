---
icon: material/flag-outline
---

# Global flags

## Config `--config`

Path to the config file (overrides `IQ_CONFIG`, default is
`<user_config_dir>/iq/iq.toml`)

## Source `--src`

Run against this saved source for one invocation (overrides the active source,
see [Sources](sources.md#sources)).

## Timeout `--timeout`

Per-query timeout (defaults is `5s`).

## Unbound `--unbounded`

Permit a filter that loads the whole dataset into memory (also materializes a
`.[]`-rooted filter instead of streaming it).

## Explain `--explain`

Prints a formatted query plan and exit without opening any connection or
executing.

## Help `--help`

Display help on the command line for `iq`.

## Version `--version`

Prints the bare version (`v1.2.3`) and exits for a release build,
`dev+<commit>` for an untagged one, with no name or prefix, so a script can use
it directly (`v=$(iq --version)`).

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

Print verbose diagnostics to stderr. For a query, the formatted plan and a live
backend command trace (disables the progress spinner).
