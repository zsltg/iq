---
icon: lucide/package-open
---

# Get started

`iq` runs `jq`[^2] filters to query, dump, copy, diff and write data across NoSQL
databases, and their dump files, from a single static binary. See
[Drivers](drivers.md#drivers) for supported databases.

<p align="center"><img src="assets/demo.svg" width="100%" alt="iq registers a MongoDB source, reads one document by key, filters a scan with a pushed-down predicate, explains the plan, and prints the result as gron"></p>

## Why iq

- Query any supported store with one language. The same jq filter runs on Redis, MongoDB,
  Cassandra, DynamoDB and the other [drivers](drivers.md#drivers), and gives the same result on
  each. One filter can also
  [join data from several sources](query-data.md#cross-source-queries), with `source()` or
  `iq combine`.
- A filter that names keys fetches only those keys. A `.[]` filter scans the source in
  pages.
- Query and compare dumps without a database server. Restore them to a live store.
- Compare two sources, or a source and a dump, by data, stats, or inferred schema.
  [`iq diff`](sources.md#diff-diff) returns `diff(1)` exit codes, so a CI job can fail on
  drift.
- Infer a schema from a sample of any source with [`iq schema`](sources.md#schema-schema), as
  JSON Schema draft 2020-12 or an Open Data Contract Standard v3.1.0 contract.
- Copy typed data across stores in one command, with no export script and no conversion
  script.
- Give an AI agent access to every store with one
  [Agent Skill and one MCP server](agents.md). The skill works with any agent that reads the
  Agent Skills format. The server is read-only by default.

`iq` normalizes fetched values to JSON and runs the filter on the client. The URI scheme[^4]
chooses the backend. The selector walks the parsed `jq` AST[^ast]. Based on the AST, it
runs a bounded read, a streaming scan (with pushdown[^pushdown]), or a materialized scan.
See [How it works](how-it-works.md#how-it-works).

`iq` is inspired by [`sq`](https://sq.io "Command-line tool giving jq-style
access to SQL databases and files like CSV or Excel"), whose command set it
deliberately follows to make the tool feel familiar. Read
[Comparison](comparison.md) for how `iq` relates to other tools.

!!! note

    `iq` is built with AI assistance. Every change passes the full test suite,
    container-backed integration tests for every backend, and a mutation gate
    before it lands (see
    [CONTRIBUTING.md](https://github.com/zsltg/iq/blob/main/CONTRIBUTING.md)).

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

### Verify a release

Every release signs `checksums.txt` with a keyless [cosign](https://docs.sigstore.dev/)
signature from the release workflow, and carries SLSA build provenance for every
artifact (`multiple.intoto.jsonl`). The install script checks the SHA-256 of the
archive it downloads. To check a download yourself, first make sure that
`checksums.txt` comes from the iq release workflow:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/zsltg/iq/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
```

To check the build provenance of an archive, use
[slsa-verifier](https://github.com/slsa-framework/slsa-verifier):

```sh
slsa-verifier verify-artifact iq_0.37.1_linux_amd64.tar.gz \
  --provenance-path multiple.intoto.jsonl \
  --source-uri github.com/zsltg/iq --source-tag v0.37.1
```

Releases before v0.37.1 carry `checksums.txt` only, with no signature or
provenance.

Each binary archive carries a `third-party-licenses/` tree with the license and
notice texts of every module that iq links. Each Linux package installs the
same texts in `/usr/share/licenses/iq/third-party/`. The source archive does not
contain these texts.

## Building from source

```sh
git clone https://github.com/zsltg/iq
cd iq && make build
```

## Quick start

You need Docker, and `iq` on your `PATH` (see [Installation](#installation)). If the
installer prints a note about `PATH`, add the directory that the note names.

Run each command on its own. If a command fails, stop and read its error.

Start a disposable MongoDB that only this machine can reach:

```sh
docker run -d --name iq-quickstart -p 127.0.0.1:27018:27017 mongo:8.2
```

!!! note

    MongoDB needs a few seconds to start. Wait about 10 seconds before the next step. If
    `iq add` fails with `server selection error`, wait a few seconds and run it again.

Register the database as the active source. `iq add` connects to make sure that the source
works:

```sh
iq add -a 'mongodb://localhost:27018/iq?collection=books'
```

Write two sample books. `iq` reads piped stdin as a source and takes each key from the `id`
field:

```sh
printf '%s\n' '{"id":1,"title":"Dune","year":1965,"price":9.99}' \
              '{"id":2,"title":"Project Hail Mary","year":2021,"price":14.5}' |
  iq --insert books --key-field id
```

Read one book by its key. `iq` fetches only that document:

```sh
iq '.["2"]'
```

The output is similar to the following:

```json
{
  "_id": "2",
  "id": 2,
  "price": 14.5,
  "title": "Project Hail Mary",
  "year": 2021
}
```

Filter a scan, and show the query plan:

```sh
iq '.[] | select(.year > 2015) | {title, price}' --explain
```

The output is similar to the following, where `server filter` shows the part of the filter
that MongoDB runs. The full filter then runs again on the client, so the result is the same
with or without pushdown:

```text
query plan
source: books (mongo)
...
mongo calls:
  find(<filter>): server-side pre-filter, then the full jq re-runs client-side
  cursor streamed in batches of 100

server filter (mongo find):
  {                                 — .year > 2015
    "$or": [
      {"year": {"$gt": 2015}},
      {"year": {"$type": ["string", "array", "object"]}}
    ]
  }
...
```

Without `--explain`, the same command prints `{"price": 14.5, "title": "Project Hail Mary"}`.

When you are done, remove the container and the source:

```sh
docker rm -f iq-quickstart
iq rm books
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

!!! warning "Writes"

    Queries are read-only. `--insert`, `--replace`, `iq data clear`,
    `iq data drop` and `iq data delete` write to the target. `iq exec` forwards
    a native command to the database, so it can write too. Use `--explain` to
    see the [query plan](query-plan.md#query-plan) or `--dry-run` to report the
    effect of a write, without changing anything. `iq exec` has no dry run.

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

In zsh, fish, and PowerShell, a candidate shows a short description next to it.
Bash shows the descriptions when it lists more than one candidate. A source
handle shows its driver, its keyspace, and `active` for the active source, for
example `prod/books  mongo, orders, active`. It never shows the host, the user,
or the URI. A group shows its number of sources, and a config key shows the help
text of its flag. To turn the descriptions off, generate the script with
`iq completion <shell> --no-descriptions`.

`iq inspect --only <TAB>` and `iq diff --section <TAB>` offer the
introspection subcommands of the selected source's backend, worked out from its
saved URI.

`iq add <TAB>` completes the connection URI part by part. It offers the schemes,
then the names of the URI options that `iq` itself reads, then the values of an
option that has a closed set of values. A shell gives `?` and `&` a special
meaning, so type them with a backslash, for example
`iq add cassandra://host/ks\?consistency=lo<TAB>`, which offers `local_quorum`
and `local_one`. Completion does not work inside quotes, because the bash and
zsh completion scripts pass the quote character to `iq`. A quoted URI still runs
correctly. Completion does not offer the host, the path, or the options that a
backend SDK reads, such as `authSource` for MongoDB. It also offers nothing when
the typed text holds a password, so a password never appears in a candidate.

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
[^keyring]: The operating system's credential store (macOS Keychain, Windows Credential Manager, the Secret Service on Linux), where `iq add` keeps source passwords by default (see [Configuration](configuration.md#keyring-keyring)). https://pkg.go.dev/github.com/zalando/go-keyring
