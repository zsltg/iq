#!/usr/bin/env bash
# Fixture tests for scripts/deadcode.sh.
#
#   bash scripts/test/deadcode.sh
#
# Each case puts a simulated go command first on PATH and compares the exit
# status of the check. The test needs no network and finishes in seconds.
# scripts/check.sh (make check) runs it.
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
check="$root/scripts/deadcode.sh"
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" || exit 1
cat >"$work/bin/go" <<'GO'
#!/usr/bin/env bash
printf '%s' "$TEST_STDERR" >&2
printf '%s' "$TEST_STDOUT"
exit "$TEST_STATUS"
GO
chmod +x "$work/bin/go" || exit 1

failures=0
while IFS='|' read -r name stdout stderr status want message; do
  got=0
  PATH="$work/bin:$PATH" TEST_STDOUT="${stdout//\\n/$'\n'}" TEST_STDERR="${stderr//\\n/$'\n'}" \
    TEST_STATUS="$status" bash "$check" >"$work/out" 2>&1 || got=$?
  if [[ "$got" == "$want" ]] && grep -Fq -- "$message" "$work/out"; then
    echo "PASS $name"
  else
    echo "FAIL $name: exit $got, want $want"
    sed 's/^/  /' "$work/out"
    failures=$((failures + 1))
  fi
done <<'CASES'
no dead code|||0|0|deadcode: passed
download lines on stderr||go: downloading golang.org/x/tools v0.52.0\n|0|0|deadcode: passed
dead code on stdout|cmd/x.go:1:6: unreachable func: f\n||0|1|unreachable func: f
run error with empty stdout||main.go:1:1: syntax error\n|1|1|did not run to the end
run error with partial stdout|cmd/x.go:1:6: unreachable func: f\n|exit status 2\n|2|1|did not run to the end
CASES

if [[ "$failures" != 0 ]]; then
  echo "deadcode tests: $failures failure(s)" >&2
  exit 1
fi
echo 'deadcode tests: passed'
