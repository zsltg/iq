#!/usr/bin/env bash
# Prints one sha256 fingerprint for the mutation state of a package.
#
#   scripts/mutation-fingerprint.sh <package>   # ./internal/numfmt, ./cmd, or . for the root
#
# The weekly scan (scripts/mutation-plan.sh) scans a package again only when this value
# changes, and reuses the stored result of the last scan when it does not change. The
# value is the hash of a sorted manifest, in the same format as scripts/demo/stamp.sh:
#
#   - Each tracked file directly in the package directory, tests included, and each tracked
#     file under its testdata directory. Subdirectories are other packages, so they are not
#     part of it. For the root package (.) that is the files at the root of the repository.
#   - The tooling that decides the verdict: go.mod, go.sum, .mutago.yml, and
#     scripts/mutation-gate.sh (it holds MUTAGO_VERSION and the ./cmd floor).
#   - The Go version (`go env GOVERSION`).
#   - The entries of mutago-baseline.json for files of this package, sorted by id.
#
# A change in another package does not change the value (decision 1a of the plan). A
# monthly full scan catches the effects across packages.
set -euo pipefail

export LC_ALL=C

pkg="${1:?usage: mutation-fingerprint.sh <package>}"
if [[ "$pkg" != "." && ! "$pkg" =~ ^\./[A-Za-z0-9_][A-Za-z0-9_./-]*$ ]] || [[ "$pkg" == *..* ]]; then
  echo "mutation-fingerprint: '$pkg' is not a package path (. or ./dir)" >&2
  exit 1
fi

cd "$(git rev-parse --show-toplevel)" || exit 1

dir="${pkg#./}"
[[ "$pkg" == "." ]] && dir=""
dir="${dir%/}"
if [[ -n "$dir" && ! -d "$dir" ]]; then
  echo "mutation-fingerprint: no directory for package '$pkg'" >&2
  exit 1
fi

# A path belongs to the package when the part after the package directory has no slash,
# or starts with testdata/.
owned() {
  local rest="$1"
  [[ "$rest" != */* || "$rest" == testdata/* ]]
}

hash_file() {
  printf '%s  %s\n' "$(tr -d '\r' <"$1" | sha256sum | cut -d' ' -f1)" "$1"
}

manifest() {
  local path rest
  while IFS= read -r path; do
    [[ -n "$path" && -f "$path" ]] || continue
    rest="$path"
    [[ -n "$dir" ]] && rest="${path#"$dir"/}"
    owned "$rest" && hash_file "$path"
  done < <(git ls-files -z -- ${dir:+"$dir"} | tr '\0' '\n' | sort -u)
  for path in go.mod go.sum .mutago.yml scripts/mutation-gate.sh; do
    hash_file "$path"
  done
  printf 'goversion  %s\n' "$(go env GOVERSION)"
  printf '%s  baseline\n' "$(jq -c --arg d "$dir" '
    [.mutants[]
      | select((.file | split("/") | .[:-1] | join("/")) == $d)]
    | sort_by(.id)' mutago-baseline.json | sha256sum | cut -d' ' -f1)"
}

manifest | sha256sum | cut -d' ' -f1
