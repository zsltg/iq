#!/usr/bin/env bash
# Fixture tests for scripts/mutation-verdict.sh and the rate table of
# scripts/mutation-plan.sh.
#
#   bash scripts/test/mutation-verdict.sh
#
# Each case makes shard artifacts with scripts/test/mutation-verdict-fixtures.py in a
# temporary directory, runs the verdict, and examines the exit status, the output, the
# state files and the badge. The test needs no network, no container and no mutago run,
# and it finishes in seconds. scripts/check.sh (make check) runs it.
#
# The fixtures use real package paths (./internal/numfmt, ./internal/render, ./cmd),
# because the verdict calculates their fingerprints. The accepted escape uses the id of
# the first entry of mutago-baseline.json, so the test does not depend on one entry.
#
# The verdict writes mutago-summary.json in the root of the repository. The test keeps a
# copy of a file that is already there and puts it back at the end.
# Each check is a condition in single quotes that check() evaluates later, so the
# variables in it must not expand at the call. status is read only in those conditions.
# shellcheck disable=SC2016,SC2034
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1
here="$root/scripts/test"

work=$(mktemp -d) || exit 1
summary_backup=""
if [[ -f mutago-summary.json ]]; then
  summary_backup="$work/summary-backup.json"
  cp mutago-summary.json "$summary_backup"
fi
finish() {
  rm -f mutago-summary.json
  [[ -n "$summary_backup" ]] && cp "$summary_backup" mutago-summary.json
  rm -rf "$work"
}
trap finish EXIT

identity=$(jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha256sum .mutago.yml | cut -d' ' -f1)" \
  --arg baseline "$(sha256sum mutago-baseline.json | cut -d' ' -f1)" \
  '{commit: $commit, mutagoVersion: $mutago, goVersion: $go,
    configSha256: $config, baselineSha256: $baseline}')
accepted_id=$(jq -r '.mutants[0].id' mutago-baseline.json)

passed=0
failed=0
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

# prepare <name> <shards-json> <package>...: makes the fixtures of one case in $case_dir.
prepare() {
  local name="$1" shards="$2"
  shift 2
  case_dir="$work/$name"
  mkdir -p "$case_dir/badges/state" "$case_dir/artifacts"
  jq -n --argjson identity "$identity" --argjson shards "$shards" \
    '{identity: $identity, shards: $shards}' >"$case_dir/spec.json"
  python3 "$here/mutation-verdict-fixtures.py" "$case_dir/spec.json" "$case_dir"
  printf '%s\n' "$@" >"$case_dir/packages.txt"
}

# verdict: runs the verdict on $case_dir and sets status.
verdict() {
  rm -f mutago-summary.json
  bash scripts/mutation-verdict.sh "$case_dir/artifacts" "$case_dir/badges" \
    "$case_dir/matrix.json" "$case_dir/packages.txt" >"$case_dir/out" 2>&1
  status=$?
}

cell() { jq -nc --arg f "$1" --arg m "$2" --argjson x "$3" '{file: $f, mutator: $m, mutants: $x}'; }
shard() {
  jq -nc --arg d "$1" --arg p "$2" --arg s "$3" --argjson n "$4" --argjson t "$5" --argjson c "$6" \
    '{dir: $d, package: $p, slug: $s, shard: $n, shards: $t, cells: $c}'
}
# cmd_cell <killed> <escaped>: one ./cmd cell; each escape has an id that is not in the baseline.
cmd_cell() {
  local arr="" i
  for ((i = 0; i < $1; i++)); do arr+="[\"k$i\",\"killed\",\"\"],"; done
  for ((i = 0; i < $2; i++)); do arr+="[\"e$i\",\"escaped\",\"new$i\"],"; done
  cell cmd/driver.go statement/remove "[${arr%,}]"
}
# state_file <slug> <package> <fingerprint>: a stored state with one killed mutant.
state_file() {
  jq -n --arg s "$1" --arg p "$2" --arg f "$3" \
    '{package: $p, slug: $s, fingerprint: $f,
      summary: {totalMutantsCount: 1, killedCount: 1, notCoveredCount: 0,
                escapedCount: 0, errorCount: 0, skippedCount: 0}}' >"$case_dir/badges/state/$1.json"
}

numfmt=./internal/numfmt
nslug=internal-numfmt
file=internal/numfmt/convert.go
# Edit c3 escapes in both shards, with a different mutator and so a different id. One id
# is in the baseline, so the escape is not new, and the merge counts the edit once.
cell1=$(cell $file branch/if "[[\"c1\",\"killed\",\"\"],[\"c2\",\"killed\",\"\"],[\"c3\",\"escaped\",\"$accepted_id\"]]")
cell2=$(cell $file statement/return '[["c4","killed",""],["c5","notCovered",""],["c3","escaped","otherid"]]')
shard1=$(shard a $numfmt $nslug 1 2 "[$cell1]")
shard2=$(shard b $numfmt $nslug 2 2 "[$cell2]")

prepare two-shards "[$shard1,$shard2]" $numfmt
verdict
check "two shards: the gate passes" '[[ $status -eq 0 ]]'
check "two shards: each edit counts once" \
  '[[ "$(jq -c ".summary | [.totalMutantsCount, .killedCount, .escapedCount, .notCoveredCount]" "$case_dir/badges/state/$nslug.json")" == "[5,3,1,1]" ]]'
check "two shards: seconds per mutant is the shard time over the mutants" \
  '[[ "$(jq .secondsPerMutant "$case_dir/badges/state/$nslug.json")" == "40" ]]'
check "two shards: the badge shows killed / (killed + escaped)" \
  '[[ "$(jq -r .message "$case_dir/badges/mutation.json")" == "75% covered" ]]'
check "two shards: the module summary has the same score" \
  '[[ "$(jq .coveredCodeMsi mutago-summary.json)" == "0.75" ]]'

prepare missing-shard "[$shard1]" $numfmt
jq --argjson c "[$cell2]" \
  '. + [{package: "./internal/numfmt", slug: "internal-numfmt", shard: 2, shards: 2, cells: ($c | map({file, mutator}))}]' \
  "$case_dir/matrix.json" >"$case_dir/matrix.tmp" && mv "$case_dir/matrix.tmp" "$case_dir/matrix.json"
verdict
check "missing shard: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "no report for ./internal/numfmt shard 2" "$case_dir/out"'
check "missing shard: no state is written" '[[ ! -f "$case_dir/badges/state/$nslug.json" ]]'

copy=$(shard a2 $numfmt $nslug 1 2 "[$cell1]" | jq -c '. + {not_in_matrix: true}')
prepare duplicate-shard "[$shard1,$copy,$shard2]" $numfmt
verdict
check "duplicate shard: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "two reports for ./internal/numfmt shard 1" "$case_dir/out"'

prepare identity-mismatch "[$shard1,$(jq -c '. + {identity_patch: {commit: "0000000"}}' <<<"$shard2")]" $numfmt
verdict
check "other commit: the verdict fails" '[[ $status -eq 1 ]] && grep -q "has the identity" "$case_dir/out"'

prepare mutago-mismatch "[$shard1,$(jq -c '. + {identity_patch: {mutagoVersion: "v0.0.0"}}' <<<"$shard2")]" $numfmt
verdict
check "other mutago version: the verdict fails" '[[ $status -eq 1 ]] && grep -q "has the identity" "$case_dir/out"'

extra=$(shard extra $numfmt $nslug 3 2 "[$cell2]" | jq -c '. + {not_in_matrix: true}')
prepare extra-shard "[$shard1,$shard2,$extra]" $numfmt
verdict
check "shard not in the matrix: the verdict fails" '[[ $status -eq 1 ]] && grep -q "not in the matrix" "$case_dir/out"'

prepare new-escape "[$(shard a $numfmt $nslug 1 1 "[$(cell $file statement/remove '[["n1","escaped","notinbaseline"]]')]")]" $numfmt
verdict
check "new escape: the verdict fails" '[[ $status -eq 1 ]] && grep -q "1 new escaped" "$case_dir/out"'

prepare errored "[$(shard a $numfmt $nslug 1 1 "[$(cell $file statement/remove '[["e1","errored",""],["e2","killed",""]]')]")]" $numfmt
verdict
check "errored mutant: the verdict fails" '[[ $status -eq 1 ]] && grep -q "errored or timed out" "$case_dir/out"'

killed=$(cell $file statement/remove '[["k1","killed",""]]')
escaped=$(cell $file branch/if "[[\"k1\",\"escaped\",\"$accepted_id\"]]")
prepare conflict "[$(shard a $numfmt $nslug 1 2 "[$killed]"),$(shard b $numfmt $nslug 2 2 "[$escaped]")]" $numfmt
verdict
check "kill in one shard, escape in another: a warning, and the escape stays" \
  '[[ $status -eq 0 ]] && grep -q "WARNING: ./internal/numfmt: 1 edit" "$case_dir/out" &&
   [[ "$(jq .summary.escapedCount "$case_dir/badges/state/$nslug.json")" == 1 ]]'

prepare cmd-under "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 8 2)]")]" ./cmd
verdict
check "./cmd under the floor (80%): the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "under the floor of 90" "$case_dir/out"'

prepare cmd-over "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 19 1)]")]" ./cmd
verdict
check "./cmd over the floor (95%) with a new escape: the verdict passes" \
  '[[ $status -eq 0 && -f "$case_dir/badges/state/cmd.json" ]]'

prepare cmd-equal "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 9 1)]")]" ./cmd
verdict
check "./cmd at the floor (90%): the verdict passes" '[[ $status -eq 0 ]]'

prepare removed-package "[$shard1,$shard2]" $numfmt
state_file drivers-gone ./drivers/gone x
verdict
check "state of a removed package: the verdict removes it" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/state/drivers-gone.json" ]] &&
   grep -q "remove the state of .\./drivers/gone" "$case_dir/out"'

prepare partial "[$shard1,$shard2]" $numfmt ./internal/render
echo '{"old":true}' >"$case_dir/badges/mutation.json"
verdict
check "partial state: the gate passes" '[[ $status -eq 0 ]]'
check "partial state: no new badge, the previous badge stays" \
  '[[ "$(jq -c . "$case_dir/badges/mutation.json")" == "{\"old\":true}" && ! -f mutago-summary.json ]] &&
   grep -q "internal/render (no state)" "$case_dir/out"'

prepare stale-reused "[$shard1,$shard2]" $numfmt ./internal/render
state_file internal-render ./internal/render stale
verdict
check "reused state with an old fingerprint: no badge" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/mutation.json" ]] && grep -q "state is not current" "$case_dir/out"'

prepare current-reused "[$shard1,$shard2]" $numfmt ./internal/render
state_file internal-render ./internal/render "$(bash scripts/mutation-fingerprint.sh ./internal/render)"
verdict
check "reused current state: the badge covers both packages (4 of 5)" \
  '[[ $status -eq 0 && "$(jq -r .message "$case_dir/badges/mutation.json")" == "80% covered" ]]'

prepare empty-matrix "[]" $numfmt
verdict
check "nothing to scan and no state: the gate passes, no badge" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/mutation.json" ]]'

# The rate table of the plan. A package with no state uses its starting rate, any other
# package 15, and the state always has priority. Each backend package of the module has a
# starting rate, so no package reaches the 180 s backend default today.
case_dir="$work/rates"
mkdir -p "$case_dir/state"
rate() { bash scripts/mutation-plan.sh --rate "$case_dir" "$1"; }
check "rate: no state, table rate (./drivers/couchbase 156)" '[[ "$(rate ./drivers/couchbase)" == 156 ]]'
check "rate: no state, measured table rate (./drivers/hbase 26)" '[[ "$(rate ./drivers/hbase)" == 26 ]]'
check "rate: no state, other default (./internal/numfmt 15)" '[[ "$(rate ./internal/numfmt)" == 15 ]]'
echo '{"secondsPerMutant": 42.5}' >"$case_dir/state/drivers-couchbase.json"
echo '{"secondsPerMutant": null}' >"$case_dir/state/cmd.json"
check "rate: the state has priority over the table (42.5)" '[[ "$(rate ./drivers/couchbase)" == 42.5 ]]'
check "rate: a state with no rate uses the table (./cmd 25)" '[[ "$(rate ./cmd)" == 25 ]]'

echo "mutation-verdict tests: $passed passed, $failed failed"
[[ "$failed" -eq 0 ]]
