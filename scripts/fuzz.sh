#!/usr/bin/env bash
# Go native fuzzing over the parsers that read untrusted bytes: the jq formatter,
# the raw-byte predicate prefilter, the dump readers and their cache codec, the
# typed dump round trip, number conversion, schema inference and the two diff
# engines. Each target bounds its own work, so the time budget is honest.
#
# `go test -fuzz` takes ONE target per invocation, so this script enumerates the
# targets and runs them one after the other. IQ_FUZZ_TIME sets the budget for each
# target (default 20s); CI gives the scheduled deep run 5m. Seed inputs and every
# committed crasher run as ordinary subtests under `go test -short`, so `make check`
# and `make cover` cover them with no extra step.
#
# `bash scripts/fuzz.sh --affected <base> <head>` fuzzes nothing. It prints `true`
# when a change in <base>...<head> can affect a fuzz target, and `false` if not.
# The CI `fuzz` job uses it to skip the fuzzing on other changes. A change can
# affect a target when it touches one of these files:
#   - a non-test .go file in a package that the fuzz packages import, in this module
#   - a test file or a testdata/fuzz seed in a fuzz package
#   - this script, go.mod or go.sum
# A deleted file counts as changed. If the diff or `go list` fails, the answer is
# `true`. The weekly deep-fuzz job always fuzzes every target.
#
# A crasher is a real bug. Go writes the input to <pkg>/testdata/fuzz/<Name>/, and
# that file is committed with the fix as the regression seed.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

# The packages that hold fuzz targets. None of them starts a container, so this
# script needs no compose stack and no network.
packages=(
  ./internal/jqfmt
  ./internal/rawpred
  ./drivers/file
  ./internal/query
  ./internal/numfmt
  ./internal/shape
  ./internal/diff
)

# Print true when the change from $1 to $2 can affect a fuzz target, else false.
affected() {
  local base="$1" head="$2" root files dirs dir file rel pkg
  root="$(pwd -P)"
  if ! files="$(git diff --name-only --no-renames "$base...$head")"; then
    echo true
    return
  fi
  # Directories of the fuzz packages and of the module packages that they import.
  # -test also follows the imports of the test files, where the fuzz targets live.
  if ! dirs="$(go list -deps -test -f '{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}' "${packages[@]}")"; then
    echo true
    return
  fi
  while IFS= read -r file; do
    case "$file" in
      scripts/fuzz.sh | go.mod | go.sum)
        echo true
        return
        ;;
    esac
    for pkg in "${packages[@]}"; do
      pkg="${pkg#./}"
      if [[ "$file" == "$pkg"/testdata/fuzz/* ]]; then
        echo true
        return
      fi
      if [[ "$file" == *_test.go && "$(dirname "$file")" == "$pkg" ]]; then
        echo true
        return
      fi
    done
    if [[ "$file" == *.go && "$file" != *_test.go ]]; then
      while IFS= read -r dir; do
        rel="${dir#"$root"/}"
        if [[ "$(dirname "$file")" == "$rel" ]]; then
          echo true
          return
        fi
      done <<<"$dirs"
    fi
  done <<<"$files"
  echo false
}

if [[ "${1:-}" == "--affected" ]]; then
  if [[ $# -ne 3 ]]; then
    echo "usage: fuzz.sh --affected <base> <head>" >&2
    exit 2
  fi
  affected "$2" "$3"
  exit 0
fi

fuzztime="${IQ_FUZZ_TIME:-20s}"

fail=0
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
note_fail() { echo "fuzz: $1 FAILED" >&2; fail=1; }

for pkg in "${packages[@]}"; do
  targets="$(go test -list '^Fuzz' "$pkg/" 2>/dev/null | grep '^Fuzz')"
  if [[ -z "$targets" ]]; then
    note_fail "$pkg (no fuzz target found)"
    continue
  fi
  while read -r target; do
    step "$pkg $target ($fuzztime)"
    # -run='^$' skips the ordinary tests, so the whole budget goes to fuzzing.
    if ! go test -run='^$' -fuzz="^${target}\$" -fuzztime="$fuzztime" "$pkg/"; then
      echo "fuzz: $target found an input that breaks $pkg" >&2
      echo "fuzz: Go wrote it to ${pkg#./}/testdata/fuzz/$target/" >&2
      echo "fuzz: read it, fix the code, and commit that file as the regression seed" >&2
      note_fail "$pkg $target"
    fi
  done <<<"$targets"
done

if [[ "$fail" -ne 0 ]]; then
  echo -e "\nfuzz: FAILED" >&2
  exit 1
fi
echo -e "\nfuzz: passed"
