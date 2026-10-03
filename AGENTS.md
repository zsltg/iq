# iq

These rules bind all work in this repository. Read the linked policies when their task conditions apply.

## Scope and authorization

- Read-only investigation needs no worktree. Trace the relevant behavior, callers, tests, and documentation before you edit.
- Before edits, create or reuse an isolated task worktree branched from `origin/main`.
- Never switch branches in a shared checkout. Never edit `main` unless the user explicitly requests it.
- Use one short-lived branch per atomic task, with a `feat/`, `fix/`, `chore/`, `test/`, `ci/`, or `docs/` prefix.
- An implementation request authorizes local edits and the checks that the work needs.
- A local draft authorizes local edits and needed checks. It ends with a reviewable local diff.
- Commit, push, and publish a pull request only when the user requests those actions.
- Merge and release only within explicit authorization for those actions.
- Existing authorization persists across turns. Workflow instructions describe authorized actions and do not grant authorization.
- Read [the maintainer workflow](.agents/workflow.md) before source-control writes, publication, review fixes, merges, or releases.
- Ask before adding or upgrading a dependency or running a destructive database operation that the command did not request.
- If a choice is unclear and costly to reverse, ask. Otherwise, record a reasonable assumption and proceed.
- Use a small experiment to resolve uncertain behavior. Raise real risks and deviations.

## Project and invariants

`iq` is a Go command-line tool for NoSQL queries, built as one static binary with CGO disabled.
The CLI is a thin delivery layer over a query core that has no dependency on a specific driver.
The jq path uses `KVStore` and optional capability ports. `Store.Query` serves native queries through `iq exec`.
Concrete adapters are wired at the composition root.

Preserve these invariants:

- Pushdown returns a conservative superset of the jq matches. Run the full jq filter again on the client.
- Stream or paginate large results. Loading the full result into memory requires explicit opt-in.
- Keep a missing key distinct from a stored null. A missing key is absent from the returned map.
- A successful write stores the value exactly as supplied. Reject values that the backend cannot store exactly.
- Use permissive licenses, few dependencies, and no telemetry or collection of personal data.
- Use cobra and pflag for the CLI, gojq for jq, and TOML in `internal/config` for configuration.
- Use the OS keyring port in `internal/secret`. Keep one permissive SDK per driver under `drivers/`. Keep Apache Arrow under `internal/parquetout`.
- OpenSearch shares the Elasticsearch adapter.

## Code and safety

- Keep code plain, linear, and readable. Reuse fitting code and dependencies before choosing the standard library or a maintained permissive library.
- Write custom code only for a real gap in determinism, size, or licensing.
- Do not add frameworks, code generation, or heavy patterns unless asked.
- Keep one source of truth per fact. Use closed types instead of sentinel strings or parallel nullable fields.
- Delete unused files, exports, and dependencies.
- Keep domain and query logic independent of drivers and the CLI framework.
- Cross core boundaries with plain request and response structs, never driver rows, framework contexts, or raw flag structs.
- Translate backend types at the adapter boundary.
- Treat arguments, configuration, connection URIs, query fragments, and database responses as untrusted.
- Make sure that input meets the contract at function entry. Reject invalid input immediately.
- Use query parameters or structured builders for values. Allow dynamic identifiers only through explicit character or name whitelists.
- Never interpolate unchecked input into query text.
- Give every outbound call a context with an explicit timeout or deadline.
- Bound retries with backoff and jitter. Retry only idempotent operations, never invalid input or permanent failures.
- Do not hold a connection across a slow call. Release resources on success and failure.
- Wrap standard-library and third-party errors with context where they enter our code, using `fmt.Errorf("...: %w", err)`.
- Wrap errors at the boundaries that they cross. Never ignore a returned error.
- Keep package sentinels as `errors.New` for `errors.Is` and `errors.As`, and wrap them where they are returned.
- Handle errors at a boundary. Show clear, safe messages, never raw driver errors, stack traces, or database internals.
- Never log credentials, tokens, or connection URIs. Print them only with an explicit `--reveal`.
- Never hardcode or commit secrets. Read credentials from the environment, OS keyring, or a user configuration file written with mode `0600`.
- Never hand-edit generated artifacts or vendored code.
- Never strip, hide, or bypass behavior to shrink a diff or pass a test. State deliberate behavior changes.

## Tests and gates

- For repeated cases, prefer flat table-driven tests with one behavior and one `t.Run` per row.
- Direct tests are allowed for isolated cases. Keep arrange, act, and assertions clear, without factories or shared mutable fixtures.
- Keep functions pure and easy to isolate. Prefer testify `require`.
- Use `assert` only to report several independent failures in one run.
- Add or update tests for changed behavior. A bug fix needs a test that fails without the fix.
- Before refactoring core boundaries, persisted data or keys, query classification, or connections, identify tests that record current behavior.
- Add characterization tests for gaps before changing that behavior.
- Container-backed tests must call `testing.Short()` and skip in short mode.
- Run tests scoped to the diff first: `go test ./<pkg>/...`.
- Use `go test -short ./...` for the dependency-free suite. The full suite needs the backend services.
- Run `gofumpt -w .`, then `goimports -w .`, before lint and commit.
- Run `make check` for format, vet, build, lint, dead code, and short tests. Fix every reported issue before committing.
- Before running or diagnosing a gate, read its procedure, settings, and prerequisites in [DEVELOPMENT.md](DEVELOPMENT.md#quality-gates).

Before publication and before merge, all required checks must pass:

- Scoped tests and `make check`.
- `make cover`, above the coverage floor, with the integration services available.
- `make security` and `make capabilities`. The capability gate skips itself when the dependency graph is unchanged.
- The end-to-end tests. Full `make cover` includes `e2e`, so an unchanged tree needs no second run solely for this list.
- `scripts/mutation-gate.sh`, with no new surviving mutants on covered code.
- `make ci` runs check, cover, security, capabilities, and mutation in order. Start the shared stack first.
- The mutation floor exception applies only to a full scan of `./cmd`, as defined in the wrapper.
- Errors and timeouts fail the mutation gate. Investigate survivors and strengthen tests for real behavior gaps.
- Accept only genuine equivalent mutants, with a justification in `mutago-baseline.notes.md` and the committed baseline.
- Never weaken the gate to pass. Follow the diagnostic and closing-scan rules in `DEVELOPMENT.md`.
- Run `govulncheck ./...` before an authorized dependency addition or upgrade.
- Report checks that were skipped or failed, assumptions, remaining work, and edge cases accurately.

## Documentation and artifacts

- Keep `DEVELOPMENT.md` as the developer command catalogue and `CONTRIBUTING.md` as the short contributor on-ramp.
- Keep user documentation under `docs/docs/`. Do not add a command catalogue or per-flag reference to the README.
- Update `DEVELOPMENT.md` for changed developer commands, dependencies, and environment variables.
- Update the relevant docs page for user-facing changes.
- Update `.env.example` for environment variables, except `IQ_MUTATION_*`, `IQ_COVER_*`, `IQ_CAPS_*`, and `IQ_FUZZ_*`.
- Document those gate controls only in `DEVELOPMENT.md`.
- For changes to commands or help, run `make man` and `make completions` in the same change.
- If the recorded demo becomes stale, run `make demo` and include both the SVG and `docs/demo.stamp`.
- Never hand-edit the stamp or bypass the recording assertions. Read [the demo procedure](DEVELOPMENT.md#recorded-demo).
- For docs site changes, run `make docs`.
- For a new datastore, delivery mechanism, or connection contract, update the README Architecture section.
- For selector classification, pushdown-to-predicate mapping, or core port changes, update the existing Architecture Mermaid diagram.
- For adapter-only changes, update Architecture prose and the driver table, not the diagram.

Keep these pairs synchronized in the same change:

- README intro paragraphs and NOTE with `docs/docs/index.md`.
- README Architecture prose and diagrams with `docs/docs/how-it-works.md`.
- README Guarantees with `docs/docs/drivers.md`.
- README See also with `docs/docs/see-also.md`.

## Task-specific reading

- Read [the driver contract](.agents/driver-contract.md) before designing, changing, or reviewing a backend adapter or driver plan document.
- A new datastore must pass all four admission gates there before adapter code starts. A failed gate stops the work.
- The registry rules for options, host ports, and exec families bind adapters and plans.

Read the applicable books before the related work:

- For substantial code or test redesign, read [Clean Code](.agents/books/clean-code.mini.md).
- For interfaces, module boundaries, or substantial decomposition, read [A Philosophy of Software Design](.agents/books/a-philosophy-of-software-design.mini.md).
- For dependencies, ports, adapters, or composition, read [Clean Architecture](.agents/books/clean-architecture.mini.md).
- For debugging, automation, shared state, or duplicated facts, read [The Pragmatic Programmer](.agents/books/the-pragmatic-programmer.mini.md).
- For queries, data models, or connections, read [Designing Data-Intensive Applications](.agents/books/designing-data-intensive-applications.mini.md).
- For database or network calls, read [Release It!](.agents/books/release-it.mini.md).
- Before restructuring code without a behavior change, read [Refactoring](.agents/books/refactoring.mini.md).

The books are adapted from ciembor/agent-rules-books under MIT. Keep the attribution in [.agents/books/LICENSE](.agents/books/LICENSE).
