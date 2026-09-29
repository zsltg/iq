# Review guide

This file tells a code reviewer (human or AI) what to look for in a pull request
to iq. The coding rules are in AGENTS.md, and the developer commands are in
DEVELOPMENT.md. This file does not repeat them. It gives the review priorities
and the areas that need the most care.

## Priorities

Report findings in this order. A finding in a higher group is more important
than a finding in a lower group.

1. Security and data safety.
2. Correctness of query results.
3. Resource and time limits.
4. Test quality.
5. Documentation that must change with the code.

Do not report style that gofumpt, goimports or golangci-lint already enforce.
CI runs them.

## Threat model

iq connects to databases that the user names, and it reads dump files that the
user gives. Treat these inputs as untrusted: command-line arguments, MCP tool
arguments (they come from an AI agent), config files, connection URIs, jq
filters, dump file content, and every database response.

- A secret must never reach a log line or an error message.
- Source listings, config views, inspection headers, and keyring reads redact
  saved connection passwords unless the user supplies `--reveal`. In the first
  three displays, `--expand` resolves keyring passwords but does not reveal them
  without `--reveal`. `iq config edit` opens the actual config file, including
  inline passwords, in the configured editor.
- A change that adds a new formatted display of a saved connection, or a new
  command that prints a secret, must redact by default and reveal only with
  `--reveal`. `iq config edit` is direct access to the file, not a formatted
  display.
- A password is stored in the OS keyring (`--store keyring`, the default) or
  inline in the config file (`--store inline`). Without `--store`, `iq add`
  falls back to the config file with a warning when no keyring is available.
  `Config.Save` writes the config
  file with mode `0600`. It creates a missing directory with mode `0700` (before
  the umask) and keeps the permissions of an existing directory. A change that
  writes the file with a wider mode, or that stores a password anywhere else, is
  a finding.
- Test fixtures use synthetic credentials and URIs only (for example
  `redis://u:p@h:6379/0`), so that tests can verify redaction, storage and
  explicit disclosure. A real credential in the repository is a finding.
- A query or filter must never contain user input as concatenated or templated
  text. Each value is a parameter.
- A raw driver error, stack trace or database internal must not reach the user.
  The error is wrapped with context, and the message is safe to show.
- Queries (the jq read path) must not write. Only `--insert`, `--replace`,
  `iq data clear`, `iq data drop`, `iq data delete` and `iq exec` can write.
  `iq exec` forwards native input verbatim, so it can write or administer the
  database, and it has no preview. `--dry-run` applies to `--insert`,
  `--replace` and the `iq data` commands, and a dry run must not write. A change
  must not add a write path outside this list.

## Critical areas

- `internal/selector`, `internal/pushdown`, `internal/rawpred`: a pushed-down
  filter must select a superset of what the full jq filter selects on the
  client. A pushdown that drops a matching record is a correctness bug, even
  when all tests pass.
- `internal/query`: the core stays driver-agnostic. It imports no driver SDK
  and no cobra or pflag.
- `drivers/*`: each outbound call has a context with a deadline. Retries are
  bounded and only for idempotent operations. Large results are streamed or
  paged with a keyset, not loaded whole. Each resource is released on the
  success path and on the error path.
- `internal/secret`, `internal/config`, and the commands that print sources
  (`cmd/source.go`, `cmd/config.go`, `cmd/inspect.go`, `cmd/ls.go`,
  `cmd/keyring_cmd.go`): the redaction and storage rules in the threat model.
- `cmd/mcp.go`, `cmd/mcp_tools.go`: the rules in the MCP server section.
- `.github/workflows/*`: each action is pinned by commit SHA. A job gets only
  the permissions it needs. A `pull_request_target` job never checks out or
  runs pull request code. A new per-change CI job goes into the `needs` list of
  `ci-ok`.
- `scripts/mutation-*.sh`, `scripts/coverage.sh`, `scripts/release.sh`,
  `.githooks/*`: these are the gates. A change must not let a failing state
  pass. Examples: an error that is counted as a kill, a skipped job that hides
  a failure, a tag on the wrong commit.
- `go.mod`, `go.sum`: a new dependency needs a permissive license, no
  telemetry, and a justified row in `capslock-baseline.notes.md` for each new
  EXEC, ARBITRARY_EXECUTION, MODIFY_SYSTEM_STATE or SYSTEM_CALLS capability.

## MCP server

`iq mcp` has the saved sources and the keyring of the user who starts it.

- `--allow` is the only gate for write, exec and destructive tools. A tool that
  is not allowed must not be registered. The annotations are hints for display
  and enforce nothing.
- `--allow destructive` registers `iq_data_clear`, `iq_data_drop` and
  `iq_data_delete`, and permits the replace mode of `iq_insert`. A real call to
  one of these needs `confirm: true` or the confirmation from the user of the
  client. A call with `dry_run: true` needs no confirmation and must not write.
- `--allow exec` registers `iq_exec`. It forwards native commands with no
  preview and no per-call confirmation, so it can write or delete as far as the
  backend permissions allow. This is a separate permission, not a subset of
  `--allow destructive`.
- A change must not add a write path to a read tool, and must not remove the
  `--allow` gate from `iq_exec`.
- A per-call `max_items` or `max_bytes` can lower the server limits
  (`--max-items`, `--max-bytes`), never raise them.
- Stdout carries only JSON-RPC messages. Tool errors go through the redacted
  error shape (`toolError`).

## Tests

- A test must assert the behavior it names. A test that cannot fail is a
  finding. The mutation gate finds these, but a review is earlier.
- Tests are table-driven, with one behavior per case.
- A container-backed test skips under `go test -short`.

## Documentation that changes with the code

- A new or changed command, flag or environment variable updates DEVELOPMENT.md
  or the docs site in the same pull request.
- A change to the selector, the pushdown mapping or a core port updates the
  README Architecture diagram.
- These pairs change together: the README head and `docs/docs/index.md`, README
  Architecture and `docs/docs/how-it-works.md`, README Guarantees and
  `docs/docs/drivers.md`, README See also and `docs/docs/see-also.md`.
- A command or help text change regenerates `docs/man/iq.1` and the shell
  completions in the same pull request.

## Do not flag

- The subjects of commits in the pull request that fix earlier review findings.
  The pull request is squash-merged: its title becomes the subject on `main`, and
  the commit messages (with their `Co-Authored-By:` trailers) go into the body.
- Accepted equivalent mutants in `mutago-baseline.json`. Each one has a reason
  in `mutago-baseline.notes.md`.
- Known capability false positives listed in `capslock-baseline.notes.md`.
