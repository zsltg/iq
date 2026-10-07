#!/usr/bin/env bash
# Fixture tests for scripts/commit-emails.sh.
#
#   bash scripts/test/commit-emails.sh
#
# Each case makes a small git repository in a temporary directory, runs the check
# on a message file or on a commit range, and compares the exit status. All
# addresses are invented. The test needs no network and finishes in seconds.
# scripts/check.sh (make check) runs it.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
script="$root/scripts/commit-emails.sh"
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

failures=0
noreply="1234+ann@users.noreply.github.com"

# g runs one git command of the fixture setup and stops the test when it fails,
# so a broken fixture cannot pass a case that expects a failure.
g() {
  git "$@" || {
    echo "commit-emails tests: fixture setup failed: git $*" >&2
    exit 1
  }
}

# repo <name> <author email>: a new repository, the current directory.
repo() {
  mkdir -p "$work/$1" && cd "$work/$1" || exit 1
  g init -q
  g config user.name "Ann Author"
  g config user.email "$2"
  g config commit.gpgsign false
  g commit -q --allow-empty -m "base"
}

# result <name> <got> <want> [text]: report one case. The optional text must
# appear in the output of the run (in $work/<name>.out), and it must be the only
# address named when the case fails.
result() {
  if [[ "$2" -ne "$3" ]]; then
    echo "FAIL $1: exit $2, want $3"
    sed 's/^/  /' "$work/$1.out"
    failures=$((failures + 1))
  elif [[ -n "${4:-}" ]] && ! grep -qF -- "$4" "$work/$1.out"; then
    echo "FAIL $1: the output does not name $4"
    sed 's/^/  /' "$work/$1.out"
    failures=$((failures + 1))
  else
    echo "PASS $1"
  fi
}

# message <name> <want status> [text]: run mode A on the message file
# $work/message.
message() {
  local got
  bash "$script" --message "$work/message" >"$work/$1.out" 2>&1
  got=$?
  result "$1" "$got" "$2" "${3:-}"
}

# range <name> <want status> <text> <rev-list arguments>: run mode B.
range() {
  local name=$1 want=$2 text=$3 got
  shift 3
  bash "$script" "$@" >"$work/$name.out" 2>&1
  got=$?
  result "$name" "$got" "$want" "$text"
}

repo mode-a "$noreply"
printf 'work\n\nSigned-off-by: Ann Author <%s>\nCo-Authored-By: Claude <noreply@anthropic.com>\n' "$noreply" >"$work/message"
message "the author address and an allowed address pass" 0

printf 'work\n\nSigned-off-by: Ann <someone@example.com>\n' >"$work/message"
message "a sign-off with another address fails and names it" 1 "someone@example.com"

printf 'work\n\nCo-Authored-By: Bot <NoReply@Anthropic.com>\nSigned-off-by: Ann <%s>\n' "$(tr '[:lower:]' '[:upper:]' <<<"$noreply")" >"$work/message"
message "the letter case of an address does not matter" 0

printf 'work\n\nSee noreply@github.com and 99+bob@users.noreply.github.com.\n' >"$work/message"
message "the allowlist patterns pass" 0

printf 'work\n\nCo-Authored-By: Bot <noreply@example.com>\n' >"$work/message"
message "a no-reply address outside the allowlist fails" 1 "noreply@example.com"

printf 'work\n# Signed-off-by: Ann <someone@example.com>\n' >"$work/message"
message "an address in a comment line fails and names it" 1 "someone@example.com"

printf 'work\n# Author: Ann Author <%s>\n' "$noreply" >"$work/message"
message "the author address in a comment line passes" 0

printf 'work\n# ------------------------ >8 ------------------------\nSigned-off-by: Ann <someone@example.com>\n' >"$work/message"
message "an address after the scissors line fails and names it" 1 "someone@example.com"

printf 'work\n\nCc: Ana <ana@bücher.de>\n' >"$work/message"
message "a non-ASCII address fails and names it" 1 "ana@bücher.de"

repo non-ascii "ana@bücher.de"
printf 'work\n\nSigned-off-by: Ana <ana@bücher.de>\n' >"$work/message"
message "a non-ASCII author address passes" 0

printf 'work\n\nSigned-off-by: Ann <one@example.com>\nCo-Authored-By: Bob <two@example.com>\n' >"$work/message"
message "each bad address is named" 1 "two@example.com"

bash "$script" --message "$work/no-such-file" >"$work/missing-file.out" 2>&1
result "a message file that does not exist exits 2" $? 2

bash "$script" >"$work/no-args.out" 2>&1
result "no arguments exit 2" $? 2

repo outside ann@example.org
printf 'work\n\nSigned-off-by: Ann Author <ann@example.org>\n' >"$work/message"
message "an outside contributor with the own address passes" 0

repo committer "$noreply"
printf 'work\n\nSigned-off-by: Bob <bob@example.net>\n' >"$work/message"
GIT_COMMITTER_EMAIL=bob@example.net message "the committer address passes" 0

repo mode-b "$noreply"
g commit -q --allow-empty -m "good one" -m "Signed-off-by: Ann Author <$noreply>"
g commit -q --allow-empty -m "bad one" -m "Signed-off-by: Ann Author <someone@example.com>"
g commit -q --allow-empty -m "good two"
range "a range with one bad commit fails" 1 "someone@example.com" HEAD~3..HEAD
if grep -q -e 'good one' -e 'good two' "$work/a range with one bad commit fails.out"; then
  echo "FAIL only the bad commit is named"
  failures=$((failures + 1))
else
  echo "PASS only the bad commit is named"
fi
range "a range of good commits passes" 0 "1 commit(s) checked" HEAD~1..HEAD

repo merge "$noreply"
g commit -q --allow-empty -m "main work"
g checkout -q -b side HEAD~1
g commit -q --allow-empty -m "side work"
g checkout -q -
g merge -q --no-ff side -m "merge" -m "Signed-off-by: Ann <someone@example.com>"
range "a merge commit is checked" 1 "someone@example.com" HEAD~2..HEAD

repo non-ascii-range "$noreply"
g commit -q --allow-empty -m "umlaut" -m "Cc: Ana <ana@bücher.de>"
range "a non-ASCII address in a range fails and names it" 1 "ana@bücher.de" HEAD~1..HEAD

range "a range that does not resolve exits 2" 2 "cannot read the commit range" no-such-ref..HEAD

if [[ "$failures" -ne 0 ]]; then
  echo "commit-emails tests: $failures failure(s)" >&2
  exit 1
fi
echo "commit-emails tests: all passed"
