#!/usr/bin/env bash
# Reports unreachable functions with golang.org/x/tools/cmd/deadcode.
#
#   bash scripts/deadcode.sh
#
# deadcode runs at DEADCODE_VERSION from scripts/tool-versions.env through
# `go run`, so the check needs no binary on PATH. Only standard output lists
# dead code. Standard error carries the "go: downloading" lines of a new pin and
# the build errors, so the check reads a run error from the exit status, never
# from standard error. scripts/check.sh (make check) and the CI deadcode job
# run it.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1
# shellcheck source=tool-versions.env
. scripts/tool-versions.env || exit 1

if ! dead=$(go run "golang.org/x/tools/cmd/deadcode@$DEADCODE_VERSION" -test ./...); then
  [[ -z "$dead" ]] || printf '%s\n' "$dead" >&2
  echo "deadcode: the analysis did not run to the end (see the errors above)" >&2
  exit 1
fi
if [[ -n "$dead" ]]; then
  printf '%s\n' "$dead" >&2
  echo "deadcode: unreachable code found; delete it or wire it up" >&2
  exit 1
fi
echo "deadcode: passed"
