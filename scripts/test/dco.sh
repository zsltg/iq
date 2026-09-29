#!/usr/bin/env bash
# Fixture tests for scripts/dco.sh.
#
#   bash scripts/test/dco.sh
#
# Each case makes a small git repository in a temporary directory, adds commits,
# runs the check over the range after the first commit, and compares the exit
# status. The test needs no network and finishes in seconds. scripts/check.sh
# (make check) runs it.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
dco="$root/scripts/dco.sh"
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

failures=0

# g runs one git command of the fixture setup and stops the test when it fails,
# so a broken fixture cannot pass a case that expects a failure.
g() {
  git "$@" || {
    echo "dco tests: fixture setup failed: git $*" >&2
    exit 1
  }
}

# setsha <rev>: set sha to the hash of <rev>, or stop the test. It runs in the
# current shell, because an exit in a command substitution would only end that.
setsha() {
  sha=$(git rev-parse "$@") || {
    echo "dco tests: fixture setup failed: git rev-parse $*" >&2
    exit 1
  }
}

# repo <name>: a new repository with one base commit, the current directory.
repo() {
  mkdir -p "$work/$1" && cd "$work/$1" || exit 1
  g init -q
  g config user.name "Ann Author"
  g config user.email "ann@example.com"
  g config commit.gpgsign false
  g commit -q --allow-empty -m "base"
}

# expect <name> <want status>: run the check over base..HEAD.
expect() {
  local got
  bash "$dco" "$(git rev-list --max-parents=0 HEAD)" HEAD >"$work/$1.out" 2>&1
  got=$?
  if [[ "$got" -eq "$2" ]]; then
    echo "PASS $1"
  else
    echo "FAIL $1: exit $got, want $2"
    sed 's/^/  /' "$work/$1.out"
    failures=$((failures + 1))
  fi
}

repo signed
g commit -q -s --allow-empty -m "work"
expect "a signed-off commit passes" 0

repo unsigned
g commit -q --allow-empty -m "work"
expect "a commit with no sign-off fails" 1

repo other-email
g commit -q --allow-empty -m "work" -m "Signed-off-by: Ann Author <someone@example.com>"
expect "a sign-off with another email fails" 1

repo remediated
g commit -q --allow-empty -m "work"
setsha HEAD
g commit -q -s --allow-empty -m "DCO remediation" -m "I, Ann Author <ann@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation with the full hash passes" 0

repo remediated-prefix
g commit -q --allow-empty -m "work"
setsha --short=7 HEAD
g commit -q -s --allow-empty -m "DCO remediation" -m "I, Ann Author <Ann@Example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation with a 7-character prefix and another letter case passes" 0

repo remediation-unsigned
g commit -q --allow-empty -m "work"
setsha HEAD
g commit -q --allow-empty -m "DCO remediation" -m "I, Ann Author <ann@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation that is not signed off fails" 1

repo remediation-other-author
g commit -q --allow-empty -m "work"
setsha HEAD
g -c user.email=bob@example.com commit -q -s --allow-empty -m "DCO remediation" -m "I, Bob <bob@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation by another author fails" 1

repo remediation-other-email
g commit -q --allow-empty -m "work"
setsha HEAD
g commit -q -s --allow-empty -m "DCO remediation" -m "I, Ann Author <someone@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation line with another email fails" 1

repo remediation-other-commit
g commit -q --allow-empty -m "first"
g commit -q --allow-empty -m "second"
setsha HEAD~1
g commit -q -s --allow-empty -m "DCO remediation" -m "I, Ann Author <ann@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation covers only the commit it names" 1

repo short-prefix
g commit -q --allow-empty -m "work"
setsha --short=6 HEAD
g commit -q -s --allow-empty -m "DCO remediation" -m "I, Ann Author <ann@example.com>, hereby add my Signed-off-by to this commit: $sha"
expect "a remediation with a prefix under 7 characters fails" 1

repo bad-range
expect_bad_range() {
  if bash "$dco" no-such-ref HEAD >/dev/null 2>&1; then
    echo "FAIL a range that does not resolve fails: exit 0"
    failures=$((failures + 1))
  else
    echo "PASS a range that does not resolve fails"
  fi
}
expect_bad_range

if [[ "$failures" -ne 0 ]]; then
  echo "dco tests: $failures failure(s)" >&2
  exit 1
fi
echo "dco tests: all passed"
