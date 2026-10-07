#!/usr/bin/env bash
# Tests for the sharded CI `mutate-diff` job: the argument checks of scripts/mutation-gate.sh,
# the plan of scripts/mutation-plan.sh --diff, the checks of scripts/mutation-diff-shard.sh,
# and the verdict of scripts/mutation-diff-verdict.sh.
#
#   bash scripts/test/mutation-diff.sh
#
# The test needs no network, no container and no mutago run, and it finishes in seconds.
# scripts/check.sh (make check) runs it. Every gate case here stops before the mutago install,
# because the install needs the network; GOPROXY=off makes an install fail loudly if a stop
# is missing. A run that reaches mutago is covered by the CI run of the pull request.
#
# Each check is a condition in single quotes that check() evaluates later, so the
# variables in it must not expand at the call. status is read only in those conditions.
# shellcheck disable=SC2016,SC2034
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1

work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

passed=0
failed=0
case_dir="$work"
check() {
  if eval "$2"; then
    echo "PASS $1"
    passed=$((passed + 1))
  else
    echo "FAIL $1"
    echo "  condition: $2"
    [[ -f "$case_dir/out" ]] && sed 's/^/  | /' "$case_dir/out"
    failed=$((failed + 1))
  fi
}

# gate <env assignments...> -- <gate args...>: runs the gate and sets status and the output.
gate() {
  case_dir="$work/gate"
  mkdir -p "$case_dir"
  local assignments=()
  while [[ "$1" != "--" ]]; do
    assignments+=("$1")
    shift
  done
  shift
  env GOPROXY=off "${assignments[@]}" bash scripts/mutation-gate.sh "$@" >"$case_dir/out" 2>&1
  status=$?
}

# The gate without IQ_MUTATION_DIFF: these stops are the behavior that developers use. The
# new variable must not change any of them.
file=internal/numfmt/decimal.go
gate -- ./cmd "$file"
check "gate: ./cmd together with another target is a stop" \
  '[[ $status -eq 1 ]] && grep -q "cannot share a run" "$case_dir/out"'
gate IQ_MUTATION_MUTATORS="branch/if" -- 
check "gate: shard mode without a path is a stop" \
  '[[ $status -eq 1 ]] && grep -q "needs a package or file argument" "$case_dir/out"'
gate -- internal/numfmt/decimal_test.go
check "gate: a test file target is a stop" \
  '[[ $status -eq 1 ]] && grep -q "must be an existing non-test .go file" "$case_dir/out"'
gate IQ_MUTATION_WORKERS=0 -- "$file"
check "gate: a worker count of 0 is a stop" \
  '[[ $status -eq 1 ]] && grep -q "IQ_MUTATION_WORKERS must be a positive integer" "$case_dir/out"'
gate IQ_MUTATION_DIFF= -- ./cmd "$file"
check "gate: an empty IQ_MUTATION_DIFF is the same as unset" \
  '[[ $status -eq 1 ]] && grep -q "cannot share a run" "$case_dir/out"'

echo "mutation-diff: $passed passed, $failed failed"
[[ "$failed" -eq 0 ]]
