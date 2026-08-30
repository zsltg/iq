---
icon: material/stethoscope
---

# Diagnostics & Logging

Global flags, adopted from `sq`, control verbose output, file logging, error
rendering, and profiling.

They are cross-cutting concerns handled at the CLI boundary, query results are
never changed by them.

## Flags

| flag | default | effect |
| --- | --- | --- |
| `-v`, `--verbose` | off | print diagnostics (source resolved, store opened, query complete with scan count and elapsed) to stderr, plus the [query plan](query-plan.md) and a live backend command trace (disables the progress spinner) :material-earth:{ title="Global flag" } |
| `--log` | off | enable logging to a file (also via `IQ_LOG`) :material-earth:{ title="Global flag" } |
| `--log.file` | `<user cache dir>/iq/iq.log` | log file path; an empty value disables logging :material-earth:{ title="Global flag" } |
| `--log.level` | `DEBUG` | `DEBUG`, `INFO`, `WARN`, or `ERROR` :material-earth:{ title="Global flag" } |
| `--log.format` | `text` | `text` or `json` :material-earth:{ title="Global flag" } |
| `--error.format` | `text` | error output format: `text` or `json` :material-earth:{ title="Global flag" } |
| `--error.stack` | off | print the wrapped error cause chain to stderr (may include backend internals; credentials stay redacted) :material-earth:{ title="Global flag" } |
| `--no-progress` | off | disable the scan progress spinner (`-v` disables it too), see [Global flags](global-flags.md#global-flags) :material-earth:{ title="Global flag" } |
| `--error.format.text.verbose` | on | for a jq syntax error in text format, draw a caret span under the offending token :material-earth:{ title="Global flag" } |
| `--debug.pprof` | off | write a runtime profile of the whole run: `cpu`, `mem`, `block`, `mutex`, `goroutine`, `thread`, or `trace` :material-earth:{ title="Global flag" } |

:material-earth:{ title="Global flag" } marks a flag every `iq` command accepts, see [Global flags](global-flags.md#global-flags).

!!! note "Logging"

    The `--log*` flags also read the environment when the flag is not set,
    precedence **flag > env > default**: `IQ_LOG`, `IQ_LOG_FILE`,
    `IQ_LOG_LEVEL`, `IQ_LOG_FORMAT`.

    `--log` writes structured records to a file (down to the chosen level,
    always plain, never tinted). A source location is always redacted before
    it is logged, so a stored credential never reaches a log file.

!!! note "Verbose"

    `-v` writes a terse human stream to stderr (INFO and above), tinted when
    stderr is a terminal and following the same `-M`/`-C`/`NO_COLOR` decision
    as [colored output](output.md#color-color).

## Examples

```bash title="Verbose diagnostics on stderr"
./iq -v '.[]'
```
```bash title="Structured logs to a file"
./iq --log --log.file=/tmp/iq.log --log.format=json '.[]'
```
```bash title="Enable logging via the environment"
IQ_LOG=true IQ_LOG_FILE=/tmp/iq.log ./iq '.[]'
```
```bash title="Machine-readable errors"
./iq --error.format=json '.bad |'
```
```bash title="Runtime profile"
./iq --debug.pprof=cpu '.[]' && go tool pprof cpu.pprof
```
