---
icon: lucide/package-open
---

# Get started

`iq` runs `jq`[^2] filters to query, dump, copy, diff and write data across NoSQL
databases, and their dump files, from a single static binary. See
[Drivers](drivers.md#drivers) for supported databases.

<p align="center"><img src="assets/demo.svg" width="100%" alt="iq registers a MongoDB source, reads one document by key, filters a scan with a pushed-down predicate, explains the plan, and prints the result as gron"></p>

`iq` normalizes fetched values to JSON. The filter runs entirely client-side,
so one filter means the same thing everywhere.

The URI scheme[^4] chooses the backend. The filter is both the *transform* and
the *key selector*. The selector walks the parsed `jq` AST[^ast]. Based on the
AST, the selector executes a *bounded read*, a *streaming scan* (with
*pushdown*[^pushdown]), or a *materialized scan* (see
[How it works](how-it-works.md#how-it-works)).

Typed dumps carry native types across stores. As a result, a copy, a restore or a
migration is one command instead of an export plus a conversion script.

`iq` is inspired by [`sq`](https://sq.io "Command-line tool giving jq-style
access to SQL databases and files like CSV or Excel"), whose command set it
deliberately follows.

!!! note

    `iq` is built with AI assistance, and every change passes the full test
    suite, container-backed integration tests for every backend, and a mutation
    gate before it lands (see
    [CONTRIBUTING.md](https://github.com/zsltg/iq/blob/main/CONTRIBUTING.md)).

    Queries are read-only. `--insert`, `--replace`, `iq data clear`,
    `iq data drop` and `iq data delete` write to the target. `iq exec` forwards
    a native command to the database, so it can write too. Use `--explain` to
    see the [query plan](query-plan.md#query-plan) or `--dry-run` to report the
    effect of a write, without changing anything. `iq exec` has no dry run.

    Feedback and bug reports are very welcome. Report security problems privately,
    see the [security policy](https://github.com/zsltg/iq/security/policy).

## Installation

`iq` ships as a single static binary (no runtime dependencies, no CGO[^5]).

=== ":fontawesome-brands-linux: Linux"

    ```sh
    curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
    ```

    !!! note "Version & Location"

        The script downloads the release for your OS/arch, verifies its SHA-256[^6]
        against the release checksums, and installs the binary. `IQ_VERSION`
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

Register a source for each store you work with. Then make one of them active:

```sh
iq add -n orders 'mongodb://localhost:27017/shop?collection=orders'
iq add -n staging 'mongodb://staging:27017/shop?collection=orders'
iq add -n cache redis://localhost:6379/0
iq add -n snap file:///backups/prod.rdb
iq src orders
```

```sh { title='Check the list of sources you added' }
iq ls
```
```sh { title='Inspect the active source' }
iq inspect
```
```sh { title='Explain the query plan for a bounded read, a dry run' }
iq '.["o-42"]' --explain -v
```
```sh { title='Run the query to get the document with id "o-42"' }
iq '.["o-42"]'
```
```sh { title='Run a query to get all documents in batches, a streaming scan' }
iq '.[]'
```
```sh { title='Stream a filtered sample, pushed to the server where it can be' }
iq --src orders '.[] | select(.status == "new") | {id, total}'
```
```sh { title='Query a backup without restoring it' }
iq --src snap '.[] | select(.active)'
```
```sh { title='Copy one store into another, native types intact' }
iq --src orders --insert cache
```
```sh { title='Diff two environments, data or inferred schema' }
iq diff orders staging --schema
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

Completions cover the commands, their sub-subcommands and flags. They also cover
the saved source handles, groups, and config-option keys, which they read live
from your config. As a result, `iq --src <TAB>` offers the sources `iq ls` lists.
A flag that takes a closed
set offers that option's own values.

`iq inspect --only <TAB>` and `iq diff --section <TAB>` offer the
introspection subcommands of the selected source's backend, worked out from its
saved URI.

The `jq` filter itself is a program, not a completable value. As a result, `iq`
offers no candidates there (and never falls back to filenames). `iq` also offers
no candidates for the backend verb of `iq exec` and its operands.

Every completion is offline. It reads your config file and nothing else. As a
result, a `<TAB>` never opens a connection, never reads the OS
keyring[^keyring], and cannot hang. That is why a collection suffix does not
complete. `iq --src shop.<TAB>` offers nothing, because listing collections
needs a connection.

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
[^ast]: An abstract syntax tree is the tree that a parser builds from the source of a program. Here, it is the parsed `jq` filter that the key selector inspects to decide how to read (see [How it works](how-it-works.md#read-strategies)). https://en.wikipedia.org/wiki/Abstract_syntax_tree
[^pushdown]: Predicate pushdown hands part of the filter to the database, so that the database returns only matching items instead of everything for client-side filtering. Each driver page lists what the driver can push (see [Drivers](drivers.md#drivers)). https://en.wikipedia.org/wiki/Predicate_pushdown
[^keyring]: The operating system's credential store (macOS Keychain, Windows Credential Manager, the Secret Service on Linux), where `--store keyring` sources keep their secrets (see [Configuration](configuration.md#keyring-keyring)). https://pkg.go.dev/github.com/zalando/go-keyring
