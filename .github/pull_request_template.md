## What and why

<!-- One task per pull request. Say what changes and why. -->

## Checks

Run these locally before you open the pull request (see CONTRIBUTING.md):

- [ ] `make check`
- [ ] `make cover` (needs the backend containers)
- [ ] `make security`
- [ ] End-to-end tests pass on this tree through full `make cover` or `make e2e`, after relevant changes.
  Follow [the end-to-end prerequisites](../DEVELOPMENT.md#end-to-end-validation) and report skipped live flows.
- [ ] `scripts/mutation-gate.sh` (diff-scoped by default: it mutates only the lines you changed), with no new escaped mutant
- [ ] Tests added or updated for the changed behavior; a bug fix includes a test that fails without the fix (CONTRIBUTING.md, Testing)
- [ ] `scripts/capabilities.sh`, if `go.mod` or `go.sum` changed
- [ ] Docs updated in the same change (DEVELOPMENT.md, the docs site, or the README)
