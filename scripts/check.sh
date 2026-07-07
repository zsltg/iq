#!/usr/bin/env bash
# Fast, offline pre-merge gate: format, vet, build, lint, dead code, and the unit
# suite with a coverage report. No Docker, no network — the slower tiers live
# elsewhere: the container suite and coverage floor in scripts/coverage.sh, the
# supply-chain/secrets/SBOM sweep in scripts/security.sh, and the mutation gate
# in scripts/mutation-gate.sh. Install the tools it needs with `make tools-dev`.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

# deadcode has no ubiquitous binary; prefer an installed one, else run it from the
# module cache. Everything else is expected on PATH (see `make tools-dev`).
if command -v deadcode >/dev/null 2>&1; then
  deadcode=(deadcode)
else
  deadcode=(go run golang.org/x/tools/cmd/deadcode@latest)
fi

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
golangci-lint run || note_fail "lint"

step "deadcode"
dead="$("${deadcode[@]}" -test ./... 2>&1)"
if [[ -n "$dead" ]]; then
  printf '%s\n' "$dead" >&2
  note_fail "deadcode"
fi

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
