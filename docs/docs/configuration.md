---
icon: material/cog-outline
---

# Configuration `config`

The config file lives at `<user_config_dir>/iq/iq.toml`. `IQ_CONFIG` points
it elsewhere, and `--config` :material-earth:{ title="Global flag" } overrides both for one run (precedence:
`--config` > `IQ_CONFIG` > default). It holds the saved sources and the stored option
defaults below. `iq config location` prints the resolved path.

Inspect the config file and manage stored option defaults. Persist a flag's
value once so you need not retype it. Set an option globally, or scope it to
one source with `--src`.

At query time, the precedence is **explicit flag > per-source option > base
option > built-in default**. As a result, a saved default fills any flag you
leave unset. An explicit flag on the command line always takes precedence.

```sh { title='Every query defaults to YAML output' }
iq config set format yaml
```
```sh { title='30s timeout only when querying "prod"' }
iq config set --src prod timeout 30s
```
```sh { title='Effective value for "prod" (source > base > default)' }
iq config get --src prod format
```
```sh { title='Every persistable option: value, default, and help' }
iq config ls -v
```
```sh { title='Renders YAML (the stored default)' }
iq '.[]'
```
```sh { title='Explicit flag overrides the stored default' }
iq -f json '.[]'
```

!!! note "Persistable options"

    Persistable options are the flags whose default it is reasonable to
    persist:

    - `--format`, `--format.decimal`, `--compact`
    - `--timeout`
    - `--monochrome`, `--color`, `--no-progress`
    - `--no-cache`, `--no-cache-index`
    - `--verbose`, `--log*`, `--error*`

    Per-invocation flags are not storable:

    - `--src`
    - `--explain`
    - `--unbounded`
    - `--no-compile`
    - `--reveal`, `--expand`
    - `--debug.pprof`

    An `iq combine` query has no single source, so it uses the base options
    only, never a per-source override.

!!! warning "Logging options"

    The `log*` options are the one place where a stored default and the
    environment overlap. A stored `log*` value fills an unset flag. But an
    `$IQ_LOG*` environment variable still wins over it (the `flag > env > default` chain
    for logging applies before a stored default is treated as "set").

    An explicit `--log*` flag beats both. No other option reads the
    environment, so this interaction is unique to the logging family.

## Edit `edit`

Open the config file in `$IQ_EDITOR` (then `$VISUAL`, `$EDITOR`, else `vi`).

```sh
iq config edit
```

## Get `get`

Print an option's effective value at that scope.

```sh
iq config get [--src <name>] <option>
```

## Keyring `keyring`

Manage the OS-keyring secrets that back `--store keyring` sources.

```sh
iq config keyring
```

### List `ls`

List keyring-backed sources, each marked `present` or `missing` (`-j`/`-y` for
machine-readable output).

```sh
iq config keyring ls
```

### Get `get`

Print a source's secret, redacted unless `--reveal` is used.

```sh
iq config keyring get <handle>
```

### Set `set`

Write or update a secret (reads stdin/prompt when the value is omitted). A
source with an inline password is left untouched. Use `migrate` for it.

```sh
iq config keyring set <handle> [value]
```

### Remove `rm`

Delete a source's secret and mark it inline again.

```sh
iq config keyring rm <handle>
```

### Migrate `migrate`

Move an inline password into the keyring, rewriting the stored URI to its
password-less form.

```sh
iq config keyring migrate [<handle>] [--all] [--dry-run]
```

### Prune `prune`

Delete stale entries left for non-keyring sources. The keyring cannot be
enumerated, so entries whose source was deleted are undetectable and are not
pruned.

```sh
iq config keyring prune [--dry-run]
```

## Location `location`

Print the resolved config file path.

```sh
iq config location
```

## List `ls`

List the options set at that scope. `-v` lists every persistable option with
its effective value, built-in default, and help.

```sh
iq config ls [--src <name>]
```

## Set `set`

Validate and store a value (base, or per source). The value is checked exactly
as the flag checks it, so an invalid value is refused.

```sh
iq config set [--src <name>] <option> <value>
```

With `-D` or `--delete` it removes a stored value instead.

```sh
iq config set -D/--delete [--src <name>] <option>
```

## View `view`

Dump the whole config as TOML, source URIs redacted like `iq ls`.

```sh
iq config view [--reveal] [--expand]
```
