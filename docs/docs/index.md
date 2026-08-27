---
icon: lucide/package-open
---

# Get started

`iq` is a Go[^1] command-line tool that runs
`jq`[^2] filters against
NoSQL[^3] databases. The backend is chosen by the URI
scheme[^4], and the query core is driver-agnostic, so further backends slot in
behind the same port (see
[Drivers](drivers.md#drivers)).

Normally, `jq` parses the whole top-level JSON value into memory before the
filter runs. In the case of `iq`, the filter is both the *transform* and the
*key selector*: the selector walks the parsed `jq` AST and, based on that,
executes a *bounded read*, a *streaming scan* (with *pushdown*), or a
*materialized scan* to optimize the query (see
[How it works](how-it-works.md#how-it-works)).

Fetched values are normalized to JSON and the filter then runs entirely
client-side, so its semantics are identical for every backend.

`iq` is inspired by [`sq`](https://sq.io "Command-line tool giving jq-style
access to SQL databases and files like CSV or Excel"), much of its command
surface and the `<source>.<collection>` addressing along with many subcommands
and flags are deliberately similar.

!!! note

    `iq` was built with assistance from AI tools, so its code and the results it
    produces may contain mistakes.

    `--insert`, `iq data clear` and `iq data drop` write to live databases, point
    them at data you can afford to lose first, and use `--explain` to see the
    [query plan](query-plan.md#query-plan) without making changes.

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
`<TAB>` never opens a connection, never reads the OS keyring, and cannot hang.
That is why a collection suffix does not complete, `iq --src shop.<TAB>` offers
nothing, since listing collections would mean connecting.

## Man page

The `.deb`, `.rpm`, `.apk`, and `.pkg.tar.zst` packages also install an `iq(1)` manual page, so `man iq` works
after a package install. For any other install, pipe it into your man path:

```sh
iq man | sudo tee /usr/share/man/man1/iq.1 >/dev/null
```
[^1]: Go is a high-level, general-purpose programming language that is statically typed and compiled. https://go.dev
[^2]: `jq` is a widely-used command-line utility and very high-level, functional, domain-specific programming language designed for processing JSON data. https://jqlang.org
[^3]: NoSQL refers to a type of database design that stores and retrieves data differently from the traditional table-based structure of relational databases. https://en.wikipedia.org/wiki/NoSQL
[^4]: RFC3986 proposes a generic URI syntax and a process for resolving URI references that might be in relative form, along with guidelines and security considerations for the use of URIs on the Internet. https://datatracker.ietf.org/doc/html/rfc3986
[^5]: Cgo enables the creation of Go packages that call C code. https://pkg.go.dev/cmd/cgo
[^6]: SHA-256 is a Secure Hash Algorithm with a message digest size of 256. https://nvlpubs.nist.gov/nistpubs/fips/nist.fips.180-4.pdf
