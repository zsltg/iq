# Contributing

Thank you for your interest in `iq`. This page tells you how to get a change
merged. The full developer guide (every gate and its settings, CI, releasing,
the architecture) is [DEVELOPMENT.md](DEVELOPMENT.md).

- A bug or a driver request: open an issue with the matching
  [issue form](https://github.com/zsltg/iq/issues/new/choose).
- A security problem: do not open an issue. Report it privately, see
  [SECURITY.md](SECURITY.md).

## Before you start

- Go 1.27.0 or newer (matches `go.mod`).
- Docker, for the integration tests. `go test -short ./...` needs no Docker.
- [gofumpt](https://github.com/mvdan/gofumpt),
  [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports) and
  [golangci-lint](https://golangci-lint.run) v2.13 or newer on `PATH`.
- [uv](https://docs.astral.sh/uv/), only for the docs site and `make security`.
- Run `make hooks` once in your checkout. The hooks make sure that your commits
  carry the email address that is set in your git config.

## Build and test

```bash
go build -o iq .       # build the binary
go test -short ./...   # fast unit tests, no external services
go test ./...          # full suite, the backends start in containers
make e2e               # black-box tests of the built binary
```

The full suite starts each backend in a throwaway container. HBase is the
exception: its tests run only when `IQ_HBASE_URL` points at the compose cluster
(`docker compose up -d --wait hbase`), and they skip without it. To use a
running server for any other backend, set `IQ_<BACKEND>_URL` (see
`.env.example` and [Test backends](DEVELOPMENT.md#test-backends)).

## Testing

Test policy: a pull request that adds or changes behavior adds or updates the
tests for it in the same pull request. A bug fix adds a test that fails without
the fix. The mutation gate checks that these tests assert the behavior on the
changed lines that tests cover: a mutant that survives on such a line fails the
pull request, unless it is an accepted equivalent in `mutago-baseline.json`. A
changed line that no test covers is outside the gate, so reviewers treat a
behavior change without a test as a finding (see `REVIEW.md`).

## Before you open a pull request

Run the gates locally. CI runs them again, but a pull request that fails them
locally costs review time.

```bash
make check                      # format, vet, build, lint, dead code, short tests
make cover                      # full suite and the coverage floor (needs Docker)
make security                   # vulnerabilities, licenses, secrets, workflow audit, SBOM
make e2e                        # black-box tests
bash scripts/mutation-gate.sh   # mutation gate on the lines you changed
make capabilities               # only when go.mod or go.sum changed
```

The mutation gate mutates only the lines that your branch changed. If a mutant
survives, a test does not check that line: make the test stronger. What each
gate does, and its settings, is in [DEVELOPMENT.md](DEVELOPMENT.md#quality-gates).

Until the mutago v2.10.16 re-baseline is done, some accepted escapes in
`mutago-baseline.json` carry IDs from the older version. The diff-scoped gate can
then fail on a line you changed, even though the escape there was accepted
before. Say so in the pull request, and the maintainer re-checks and handles the
entry. This note goes away with the re-baseline.

## Commits

- Trunk-based: `main` always buildable; short-lived branches prefixed
  `feat/`, `fix/`, `chore/`, `test/`, `ci/`, `docs/`; one atomic task per branch.
- Code follows the Coding Conventions in [AGENTS.md](AGENTS.md). `make check`
  enforces formatting (`gofumpt`, `goimports`) and lint (`golangci-lint`), and
  [REVIEW.md](REVIEW.md) lists what reviewers look for.
- Open a pull request against `main`, fill in the template, and give it a
  Conventional Commits title: the pull request is squash-merged, and its title
  becomes the commit message on `main`. Open it as a draft: the reviews run, and
  CI runs the fast checks only. Mark it ready for review when the review rounds
  settle, which starts the full CI. Push review fixes as new commits, do not
  force-push, and push the fixes of one review round together. Answer each review comment, with a fix commit or a reply. The pull
  request merges when the `ci-ok` check passes and every review thread is
  resolved.
- A pull request from a fork: GitHub runs its CI only after a maintainer
  approves the first run of a new contributor. After your first merged pull
  request, CI starts by itself. A fork pull request runs with a read-only token
  and no repository secrets, so the Codecov upload can fail for it without
  failing CI. CodeRabbit reviews it, and a workflow posts the Devin Review link
  as a comment. Only the maintainer merges.
- Developer Certificate of Origin: sign off every commit with `git commit -s`.
  The `Signed-off-by:` line certifies that you may contribute the change under
  the project license, as the [DCO](https://developercertificate.org/) says. The
  email address in it must be the commit's author address. The CI `dco` job
  checks each commit of a pull request (`bash scripts/dco.sh origin/main HEAD`
  runs the same check locally). The check covers only the commits of a pull
  request, so the older commits on `main`, from before this rule, need none.
- [Conventional Commits](https://www.conventionalcommits.org/):
  `<type>(<scope>): <description>`, lowercase imperative.
- If AI contributed to a commit, end its message with a `Co-Authored-By:`
  trailer naming the model and version, for example
  `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## AI-assisted contributions

`iq` itself is built with AI assistance, so AI-assisted pull requests are
welcome. They meet the same bar as any other change, and three rules apply:

- Disclose it. Every commit that an AI tool helped with ends with the
  `Co-Authored-By:` trailer above. The squash merge keeps the trailers in the
  commit on `main`.
- Run the gates locally before you open the pull request (the list above). The
  mutation gate is diff-scoped by default, and a full package scan is not
  required.
- Own the diff. You can explain every line, why the tests prove it, and why a
  surviving mutant is a real equivalent before it enters the baseline.

A pull request that breaks one of these rules is closed with a pointer to this
section, not reviewed line by line.

## More

- [DEVELOPMENT.md](DEVELOPMENT.md): the developer guide and command catalogue:
  the local stack, the gates, CI, the docs and demo, releasing, the architecture.
- [REVIEW.md](REVIEW.md): what reviewers check.
- [AGENTS.md](AGENTS.md): the coding rules, binding for humans and AI agents.
- [SECURITY.md](SECURITY.md): how to report a vulnerability.
