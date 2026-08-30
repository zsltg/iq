---
icon: lucide/package-open
---

# Get started

`jq`[^2] for NoSQL databases.

`iq` runs `jq` filters to query, dump, copy, diff and write data across NoSQL
databases, and their dump files, from a single static binary (see
[Drivers](drivers.md#drivers)).

<p align="center"><img src="assets/demo.svg" width="100%" alt="iq registers a MongoDB source, reads one document by key, filters a scan with a pushed-down predicate, explains the plan, and prints the result as gron"></p>

Fetched values are normalized to JSON and the filter runs entirely client-side,
so one filter means the same thing everywhere.

The backend is chosen by the URI scheme[^4], and the filter is both the
*transform* and the *key selector*: the selector walks the parsed `jq` AST[^ast]
and, based on that, executes a *bounded read*, a *streaming scan* (with
*pushdown*[^pushdown]), or a *materialized scan* (see
[How it works](how-it-works.md#how-it-works)).

Typed dumps carry native types across stores, so a copy, a restore or a
migration is one command instead of an export plus a conversion script.

`iq` is inspired by [`sq`](https://sq.io "Command-line tool giving jq-style
access to SQL databases and files like CSV or Excel"), whose command surface it
deliberately follows.

## What it's for

Register the sources once, then every row below is a command you can run:

```sh
iq add -n orders 'mongodb://localhost:27017/shop?collection=orders'
iq add -n staging 'mongodb://staging:27017/shop?collection=orders'
iq add -n cache redis://localhost:6379/0
iq add -n snap file:///backups/prod.rdb
```

| You want to | Run |
| --- | --- |
| Read one document from any store | `iq --src orders '.["o-42"]'` |
| Stream a filtered sample, pushed to the server where it can be | `iq --src orders '.[] \| select(.status == "new") \| {id, total}'` |
| Query a backup without restoring it | `iq --src snap '.[] \| select(.active)'` |
| Copy one store into another, native types intact | `iq --src orders --insert cache` |
| Diff two environments, data or inferred schema | `iq diff orders staging --schema` |
| See the plan before anything runs | `iq --src orders '.[] \| select(.total > 99)' --explain` |

## Why not `<native cli> | jq`?

- The filter names the keys, so there is no native query to write first, and a
  missing key reads as `null` rather than an error.
- Paging, bounded memory and `--timeout` are handled: a `.[]`-rooted filter
  streams the keyspace, and a filter that would load all of it at once is
  refused unless you ask with `--unbounded`.
- Every backend's values (Redis hashes, sets and streams, BSON, DynamoDB
  attribute values, CQL types) normalize to one JSON, so one filter means one
  thing on all of them.
- Typed dumps and `--insert` carry native types across stores, so a copy, a
  restore or a migration is one command instead of an export plus a conversion
  script.

!!! note

    `iq` is built with AI assistance, and every change passes the full test
    suite, container-backed integration tests for every backend, and a mutation
    gate before it lands (see
    [CONTRIBUTING.md](https://github.com/zsltg/iq/blob/main/CONTRIBUTING.md)).

    Queries are read-only. `--insert`, `--replace`, `iq data clear`,
    `iq data drop` and `iq data delete` write to the target. Use `--explain` to
    see the [query plan](query-plan.md#query-plan) or `--dry-run` to report the
    effect, without changing anything.

    Feedback and bug reports are very welcome.

## Installation

`iq` ships as a single static binary (no runtime dependencies, no CGO[^5]).

=== ":fontawesome-brands-linux: Linux"

    ```sh
    curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
    ```

    !!! note "Version & Location"

        The script downloads the release for your OS/arch, verifies its SHA-256[^6]
        against the release checksums, and installs the binary, `IQ_VERSION`
        pins a version and `IQ_INSTALL_DIR` picks the target directory.

        You can also download a `.deb`, `.rpm`, `.apk`, or Arch `.pkg.tar.zst` from the
        [releases](https://github.com/zsltg/iq/releases).

=== ":fontawesome-brands-apple: macOS"

    ```sh
    brew install zsltg/tap/iq
    ```

    The Linux install script above works on macOS too.

=== ":fontawesome-brands-windows: Windows"

    ```powershell
    scoop bucket add zsltg https://github.com/zsltg/scoop-bucket
    scoop install iq
    ```

=== ":fontawesome-brands-golang: Go"

    ```sh
    go install github.com/zsltg/iq@latest
    ```

## Building from source

```sh
git clone https://github.com/zsltg/iq
cd iq && make build
```

## The basics

```sh { title='Add a collection named "books" from a MongoDB source' }
iq add 'mongodb://localhost:27017/iq?collection=books'
```
```sh { title='Check the list of sources you added' }
iq ls
```
```sh { title='Make a source active' }
iq src books
```
```sh { title='Inspect the database' }
iq inspect
```
```sh { title='Explain the query plan for a bounded read, a dry run' }
iq '.["1"]' --explain -v
```
```sh { title='Run the query to get the document with id "1"' }
iq '.["1"]'
```
```sh { title='Run a query to get all documents in batches, a streaming scan' }
iq '.[]'
```

You can find detailed examples in [Sources](sources.md#sources), [Query data](query-data.md#query-data) and [Write data](write-data.md#write-data).

For more advanced usage check [Output](output.md#output), [Query plan](query-plan.md#query-plan), [Cookbook](cookbook.md#cookbook) and
[Loading exports](loading-exports.md#loading-exports).

For debugging, see [Diagnostics & Logging](diagnostics-and-logging.md#diagnostics-logging).

Supported data sources are listed in [Drivers](drivers.md#drivers).

## Shell completions

The `.deb`, `.rpm`, `.apk` and `.pkg.tar.zst` packages install
[Bash](https://tiswww.case.edu/php/chet/bash/bashtop.html "GNU Bourne-Again
SHell, the default shell on most Linux distributions"),
[Zsh](https://www.zsh.org/ "Extended Bourne shell, the default on macOS since
Catalina") and [fish](https://fishshell.com/ "Friendly Interactive SHell,
deliberately non-POSIX, with autosuggestions built in") completions for you.

For a [brew](https://brew.sh/ "Homebrew, the third-party package manager for
macOS and Linux"), [scoop](https://scoop.sh/ "Command-line installer for
Windows, installing per-user without admin rights"),
[go-install](https://go.dev/ref/mod#go-install "Builds and installs a Go
command from its module path into GOPATH/bin") or source build,
`iq completion <shell>` prints a script to install by hand.

=== ":simple-gnubash: Bash"

    ```sh title="load in the current session"
    eval "$(iq completion bash)"
    ```
    ```sh title="copy it to the completion path"
    iq completion bash | sudo tee /usr/share/bash-completion/completions/iq >/dev/null
    ```

=== ":simple-zsh: Zsh"

    ```sh title="write to a directory on your $fpath, then restart the shell"
    iq completion zsh > ~/.zsh/completions/_iq
    ```

=== ":simple-fishshell: fish"

    ```sh title="write to a directory on your $fish_complete_path"
    iq completion fish > ~/.config/fish/completions/iq.fish
    ```

=== ":material-powershell: PowerShell"

    ```sh title="append to your profile"
    iq completion powershell >> $PROFILE
    ```

Completions cover the commands, their sub-subcommands and flags, and read live
from your config the saved source handles, groups, and config-option keys, so
`iq --src <TAB>` offers the sources `iq ls` lists. A flag that takes a closed
set offers that option's own values.

`iq inspect --only <TAB>` and `iq diff --section <TAB>` offer the
introspection subcommands of the selected source's backend, worked out from its
saved URI.

The `jq` filter itself is a program, not a completable value, so `iq` offers no
candidates there (and never falls back to filenames), nor do `iq exec`'s
backend verb and its operands.

Every completion is offline, it reads your config file and nothing else, so a
`<TAB>` never opens a connection, never reads the OS keyring[^keyring], and cannot hang.
That is why a collection suffix does not complete, `iq --src shop.<TAB>` offers
nothing, since listing collections would mean connecting.

## Man page

The `.deb`, `.rpm`, `.apk`, and `.pkg.tar.zst` packages also install an `iq(1)` manual page, so `man iq` works
after a package install. For any other install, pipe it into your man path:

```sh
iq man | sudo tee /usr/share/man/man1/iq.1 >/dev/null
```
[^2]: `jq` is a widely-used command-line utility and very high-level, functional, domain-specific programming language designed for processing JSON data. https://jqlang.org
[^4]: RFC3986 proposes a generic URI syntax and a process for resolving URI references that might be in relative form, along with guidelines and security considerations for the use of URIs on the Internet. https://datatracker.ietf.org/doc/html/rfc3986
[^5]: Cgo enables the creation of Go packages that call C code. https://pkg.go.dev/cmd/cgo
[^6]: SHA-256 is a Secure Hash Algorithm with a message digest size of 256. https://nvlpubs.nist.gov/nistpubs/fips/nist.fips.180-4.pdf
[^ast]: An abstract syntax tree is the tree a parser builds from a program's source, here the parsed `jq` filter the key selector inspects to decide how to read (see [How it works](how-it-works.md#read-strategies)). https://en.wikipedia.org/wiki/Abstract_syntax_tree
[^pushdown]: Predicate pushdown hands part of the filter to the database so it returns only matching items instead of everything for client-side filtering, each driver's page lists what it can push (see [Drivers](drivers.md#drivers)). https://en.wikipedia.org/wiki/Predicate_pushdown
[^keyring]: The operating system's credential store (macOS Keychain, Windows Credential Manager, the Secret Service on Linux), where `--store keyring` sources keep their secrets (see [Configuration](configuration.md#keyring-keyring)). https://pkg.go.dev/github.com/zalando/go-keyring
