## What and why

<!-- One task per pull request. Say what changes and why. -->

## Checks

Run these locally before you open the pull request (see CONTRIBUTING.md):

- [ ] `make check`
- [ ] `make cover` (needs the backend containers)
- [ ] `make security`
- [ ] `make e2e`
- [ ] `scripts/mutation-gate.sh` (diff-scoped by default: it mutates only the lines you changed), with no new escaped mutant
- [ ] Tests added or updated for the changed behavior; a bug fix includes a test that fails without the fix (CONTRIBUTING.md, Testing)
- [ ] `scripts/capabilities.sh`, if `go.mod` or `go.sum` changed
- [ ] Docs updated in the same change (DEVELOPMENT.md, the docs site, or the README)

<!-- Until the mutago v2.10.16 re-baseline is done, the mutation gate can fail on a line
you changed only because an accepted escape there has a new ID. If you think that is the
case, say so here, and the maintainer handles it. -->
