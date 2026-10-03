# Maintainer workflow

This file describes the maintainer process for source control, review, merge, and release.
It does not authorize those actions. Follow the scope and authorization rules in [AGENTS.md](../AGENTS.md).
Existing user authorization persists across turns. A request for a local draft ends with a local diff.

## Branches and commits

- Keep `main` buildable. Use one short-lived branch per atomic task.
- Use a `feat/`, `fix/`, `chore/`, `test/`, `ci/`, or `docs/` prefix.
- Before edits, create or reuse an isolated task worktree branched from `origin/main`, except for release work.
- For release work, branch from `github/main` as described in [the release procedure](../DEVELOPMENT.md#releasing).
- Never switch branches in a shared checkout. Never edit `main` unless the user explicitly requests it.
- Read-only investigation needs no worktree.
- Before an authorized commit, run formatters and fix all findings from `make check`.
- Use a Conventional Commits subject: `<type>(<scope>): <description>`, with a lowercase imperative description.
  The allowed types are `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, and `chore`.
- Sign off every commit for its author with `git commit -s`.
  The `Signed-off-by:` trailer certifies the Developer Certificate of Origin (DCO).
  The CI `dco` job checks each pull request commit, except merge commits.
- End each agent-authored commit message with a `Co-Authored-By:` trailer that names the model and version.
- For missing sign-offs, use the signed-off empty corrective commit described in [CONTRIBUTING.md](../CONTRIBUTING.md#commits).
  That document also permits an author-only DCO repair with a rebase and force-push.
  Agents need explicit authorization for that exception. It does not authorize rewriting review fixes or rebasing onto newer `main`.

## Publication and review

- Before publication, run every required local gate in [AGENTS.md](../AGENTS.md#tests-and-gates).
- Read [DEVELOPMENT.md](../DEVELOPMENT.md#quality-gates) for the procedures and prerequisites.
- Report failures or skipped checks accurately. Do not bypass a failed gate.

1. When publication is authorized, push the branch to `github`.
2. Open a draft pull request with `gh pr create --draft` and `.github/pull_request_template.md`.
   Use a Conventional Commits title. The title becomes the squash commit subject on `main`.
   Every change reaches `main` through a GitHub pull request and squash merge.

- Address review and CI findings with new commits. Never amend published fixes or force-push them.
- Collect the fixes from one review round and push them together.
- To take in newer `main`, merge it into the task branch. Do not rebase or hide a conflict.

A draft runs reviews and fast CI jobs only. Its `ci-ok` check fails.

- When the review rounds settle, run `gh pr ready <n>` to start full CI once.

A new push cancels the preceding run for the same pull request.

1. Before merge, read every inline and summary review comment from bots and humans.
2. Answer each with a fix commit or a reply that explains why no fix is needed.
3. Resolve every thread. CodeRabbit resolves its own threads after it confirms a fix.

An unresolved thread blocks merge.

## Merge and remote synchronization

Before an authorized merge, all local gates must pass and the pull request must pass `ci-ok`.
Every review thread must be resolved.

1. Run `gh pr merge <n> --squash`. Repository settings enable GitHub to delete the remote branch automatically.
2. In the clean `main` checkout, run `git pull --ff-only github main`, then `git push origin main`.

Both remotes must hold the same commit. The pre-push hook skips commits that a remote already holds.

The `main` ruleset has no bypass. It requires a pull request, squash merge, resolved threads, and `ci-ok`.
It also requires linear history and prohibits force-pushes and deletion of `main`.

- When a per-change CI job is added or renamed, update the `needs` list of `ci-ok`, not the ruleset.

## Release

- Read [the release procedure](../DEVELOPMENT.md#releasing) before an authorized release.

`make release` creates a changelog commit on a clean `chore/release` branch for a pull request.
After the squash merge, `make release-tag` tags the release commit on clean `main`.
Neither command pushes. Push only the release tag when that publication is authorized.

- Use `bash scripts/release.sh --dry-run` for a preview.
