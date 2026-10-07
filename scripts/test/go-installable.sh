#!/usr/bin/env bash
# Fixture tests for scripts/go-installable.sh.
#
#   bash scripts/test/go-installable.sh
#
# Each case writes a go.mod in a temporary directory, runs the check on that
# directory, and compares the exit status. The test needs no network and
# finishes in seconds. scripts/check.sh (make check) runs it.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
check="$root/scripts/go-installable.sh"
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

failures=0

# expect <name> <want status> <go.mod text>: run the check on a module with that
# go.mod.
expect() {
  local got
  mkdir -p "$work/$1" || exit 1
  printf '%s\n' "$3" >"$work/$1/go.mod" || exit 1
  bash "$check" "$work/$1" >"$work/$1.out" 2>&1
  got=$?
  if [[ "$got" -eq "$2" ]]; then
    echo "PASS $1"
  else
    echo "FAIL $1: exit $got, want $2"
    sed 's/^/  /' "$work/$1.out"
    failures=$((failures + 1))
  fi
}

expect plain 0 'module example.com/m

go 1.24

require example.com/dep v1.0.0'

expect retract 0 'module example.com/m

go 1.24

retract v0.1.0'

expect replace 1 'module example.com/m

go 1.24

require example.com/dep v1.0.0

replace example.com/dep => example.com/fork v1.0.1'

expect replace-block 1 'module example.com/m

go 1.24

replace (
	example.com/dep => ../dep
)'

expect exclude 1 'module example.com/m

go 1.24

exclude example.com/dep v1.0.0'

if [[ "$failures" -ne 0 ]]; then
  echo "go-installable tests: $failures failed" >&2
  exit 1
fi
echo "go-installable tests: passed"
