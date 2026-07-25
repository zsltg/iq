---
icon: lucide/package-open
---

# Get started

!!! warning "Pre-1.0"

    Flags, output shapes and the config format can still change between
    releases.

    `--insert`, `iq data clear` and `iq data drop` write to live
    databases, point them at data you can afford to lose first.

    Use `--explain` to see the query plan without making changes.


`iq` is a Go[^1] command-line tool that runs
`jq`[^2] filters against
NoSQL[^3] databases. The backend is chosen by the URI
scheme[^4], and the query core is driver-agnostic, so further backends slot in
behind the same port (see
[Drivers](drivers.md#drivers){ data-preview }).

Normally, `jq` would read the whole top level JSON value into memory before
parsing. In case of `iq`, the filter is both the transform and the key
selector, the selector walks the parsed `jq` AST and based on that execute
a **bounded read**, **streaming scan** (with **pushdown**) or **materialized
scan** to optimize the query (see [Architecture](architecture.md)).

Fetched values are normalized to JSON and the filter then runs entirely
client-side, so its semantics are identical for every backend.

`iq` is inspired by [`sq`](https://sq.io), much of its command surface and the
`<source>.<collection>` addressing along with many subcommands and flags
deliberately similar.

## Installation

`iq` ships as a single static binary (no runtime dependencies, no CGO[^5]).

=== ":fontawesome-brands-linux: Linux"

    ```sh
    curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
    ```

    !!! note

        The script downloads the release for your OS/arch, verifies its SHA-256
        against the release checksums, and installs the binary; `IQ_VERSION`
        pins a version and `IQ_INSTALL_DIR` picks the target directory. Or grab
        a `.deb`, `.rpm`, or `.apk` from the
        [releases](https://github.com/zsltg/iq/releases).

=== ":fontawesome-brands-apple: macOS"

    ```sh
    brew install zsltg/tap/iq
    ```

    The [Linux one-liner](#__tabbed_1_1) works on macOS too.

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

## Shell completions

The `.deb`, `.rpm` and `.apk` packages install
[Bash](https://tiswww.case.edu/php/chet/bash/bashtop.html),
[Zsh](https://www.zsh.org/) and [fish](https://fishshell.com/) completions for
you.

For a [brew](https://brew.sh/), [scoop](https://scoop.sh/),
[go-install](https://go.dev/ref/mod#go-install) or source build,
`iq completion <shell>` prints a script to install by hand.

=== ":simple-gnubash: Bash"

    ```sh title="load in the current session, or drop it on the completion path"
    eval "$(iq completion bash)"
    iq completion bash | sudo tee /usr/share/bash-completion/completions/iq >/dev/null
    ```

=== ":simple-zsh: Zsh"

    ```sh title="write to a directory on your $fpath, then restart the shell"
    iq completion zsh > ~/.zsh/completions/_iq
    ```

=== ":simple-fishshell: fish"

    ```sh
    iq completion fish > ~/.config/fish/completions/iq.fish
    ```

=== ":material-powershell: PowerShell"

    ```sh title="append to your profile"
    iq completion powershell >> $PROFILE
    ```

Completions cover the commands, their sub-subcommands and flags, and — read live from your
config — your saved source handles, groups, and config-option keys, so `iq --src <TAB>` offers
the sources `iq ls` lists. A flag that takes a closed set completes its values (`--format`,
`--from-format`, `--format.decimal`, `--log.level`, `--log.format`, `--error.format`,
`--debug.pprof`, `iq add --driver/--store`, `iq schema --format`), and `iq config set <option>
<TAB>` offers that option's own values. `iq inspect --only <TAB>` and `iq diff --section <TAB>`
offer the introspection subcommands of the selected source's backend, worked out from its saved
URL. The jq filter itself is a program, not a completable value, so `iq` offers no candidates
there (and never falls back to filenames) — nor do `iq exec`'s backend verb and its operands.

Every completion is offline: it reads your config file and nothing else, so a `<TAB>` never
opens a connection, never reads the OS keyring, and cannot hang. That is why a collection
suffix does not complete — `iq --src shop.<TAB>` offers nothing, since listing collections
would mean connecting.

## Man page

The packages also install an `iq(1)` manual page, so `man iq` works after a package install. For
a non-package install, pipe it into your man path:

```sh
iq man | sudo tee /usr/share/man/man1/iq.1 >/dev/null
```
## Requirements

- Go 1.26+
- Docker (for the integration tests, which start ephemeral Redis + MongoDB + Cassandra + DynamoDB Local + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch containers; not needed for `go test -short`. HBase integration tests run only against a `docker compose` cluster named by `IQ_HBASE_URL`)
- [uv](https://docs.astral.sh/uv/) (optional, docs-only) — builds and serves the documentation site under `docs/` (`make docs` / `make docs-serve`); not needed to build or use `iq` itself

## Build

No issues found
```bash
go build -o iq .          # plain build
make build                # build with version metadata embedded
```

`make build` injects the version, commit, and build date via ldflags; a plain `go build` still
reports a version recovered from Go's embedded build info. Check it with `iq version` or
`iq --version`.

## Releasing

Versioning is driven by [Conventional Commits](https://www.conventionalcommits.org/): the release
tooling reads the commit log, computes the next [semantic version](https://semver.org/), and
regenerates `CHANGELOG.md`. It is all-Go and local — no CI service or GitHub required.

```bash
make tools                        # one-time: install svu + git-chglog into GOPATH/bin
bash scripts/release.sh --dry-run # preview the next version and CHANGELOG.md diff, no changes
make release                      # bump, regenerate CHANGELOG.md, commit, and tag
git push --follow-tags            # publish the tag (release.sh never pushes for you)
```

`make release` must run on a clean `main`. `svu` picks the bump from the commit types since the
last tag (`feat` → minor, `fix` → patch, a `!`/`BREAKING CHANGE` → major); with no tags yet the
first release comes out as `v0.1.0`.

[^1]: Go is a high-level, general-purpose programming language that is statically typed and compiled. https://go.dev
[^2]: `jq` is a widely-used command-line utility and very high-level, functional, domain-specific programming language designed for processing JSON data. https://jqlang.org
[^3]: NoSQL refers to a type of database design that stores and retrieves data differently from the traditional table-based structure of relational databases. https://en.wikipedia.org/wiki/NoSQL
[^4]: RFC3986 proposes a generic URI syntax and a process for resolving URI references that might be in relative form, along with guidelines and security considerations for the use of URIs on the Internet. https://datatracker.ietf.org/doc/html/rfc3986
[^5]: Cgo enables the creation of Go packages that call C code. https://pkg.go.dev/cmd/cgo
