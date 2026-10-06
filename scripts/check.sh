#!/usr/bin/env bash
# Fast pre-merge gate: format, vet, build, lint, dead code, and the unit suite with
# a coverage report. No Docker. The first run needs network access, because deadcode
# runs through `go run` and the Go toolchain fetches it from the module proxy once.
# The slower tiers live elsewhere: the container suite and coverage floor in
# scripts/coverage.sh, the supply-chain/secrets/SBOM sweep in scripts/security.sh,
# and the mutation gate in scripts/mutation-gate.sh. gofumpt, goimports, and
# golangci-lint must be on PATH (see CONTRIBUTING.md, Before you start).
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

# deadcode runs at the version in scripts/tool-versions.env, the version that CI runs.
# A deadcode binary on PATH is not used, because its version can differ from CI.
. scripts/tool-versions.env
deadcode=(go run "golang.org/x/tools/cmd/deadcode@$DEADCODE_VERSION")

fail=0
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
note_fail() { echo "check: $1 FAILED" >&2; fail=1; }

step "format (gofumpt + goimports)"
unformatted="$( { gofumpt -l . ; goimports -l -local github.com/zsltg/iq . ; } | sort -u)"
if [[ -n "$unformatted" ]]; then
  echo "unformatted files (fix with: gofumpt -w . && goimports -w .):" >&2
  printf '%s\n' "$unformatted" >&2
  note_fail "format"
fi

step "go vet"
go vet ./... || note_fail "vet"

step "go build"
go build ./... || note_fail "build"

step "golangci-lint"
# golangci-lint caches analysis results, and entries survive the worktree they were
# produced in. Remove a sibling worktree and its cached findings resurface here as
# issues in ../<worktree>/... files that this tree does not contain — a failure the
# diff cannot explain and no edit can fix. Clearing the cache cures it, but doing so
# unconditionally costs the whole cache: lint is ~3s warm against ~65s cold, which
# would undo this gate's reason to exist. So retry only on that exact shape — a
# reported path outside the repo, which a run rooted at the repo root can never
# legitimately produce.
lint_out="$(golangci-lint run 2>&1)"
lint_status=$?
if [[ "$lint_status" -ne 0 ]] && grep -qE '^\.\./' <<<"$lint_out"; then
  echo "lint reported issues outside this tree (stale cache from a removed worktree); clearing it and retrying" >&2
  golangci-lint cache clean
  lint_out="$(golangci-lint run 2>&1)"
  lint_status=$?
fi
if [[ -n "$lint_out" ]]; then
  printf '%s\n' "$lint_out"
fi
if [[ "$lint_status" -ne 0 ]]; then
  note_fail "lint"
fi

step "deadcode"
dead="$("${deadcode[@]}" -test ./... 2>&1)"
if [[ -n "$dead" ]]; then
  printf '%s\n' "$dead" >&2
  note_fail "deadcode"
fi

step "demo stamp"
# The README GIF is a recording of code; the stamp is the hash of the sources it was
# recorded from. Re-hashing them needs git and sha256sum only, no expect, asciinema,
# agg or patched font, so a stale recording fails here rather than being noticed
# months later. Re-record with `make demo`.
bash scripts/demo/stamp.sh check || note_fail "demo stamp"

step "mutation verdict tests"
# The weekly mutation scan merges its shards in scripts/mutation-verdict.sh. The fixture
# tests need no network, no container and no mutago run, and finish in seconds.
bash scripts/test/mutation-verdict.sh || note_fail "mutation verdict tests"

step "dco tests"
# The fixture tests of scripts/dco.sh make small git repositories in a temporary
# directory. They need no network and finish in seconds.
bash scripts/test/dco.sh || note_fail "dco tests"

step "capability wrapper tests"
bash scripts/test/capabilities.sh || note_fail "capability wrapper tests"

step "unit tests (-short) + coverage report"
if go test -short -covermode=atomic -coverprofile=coverage.out ./...; then
  total="$(go tool cover -func=coverage.out | tail -1 | awk '{print $NF}')"
  echo "coverage (-short, report only): $total"
  echo "note: -short skips container-backed driver paths; run 'make cover' for the full number and floor."
else
  note_fail "unit tests"
fi

if [[ "$fail" -ne 0 ]]; then
  echo -e "\ncheck: FAILED" >&2
  exit 1
fi
echo -e "\ncheck: passed"
