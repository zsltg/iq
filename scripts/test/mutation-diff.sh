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


# IQ_MUTATION_DIFF=1 (file targets keep the diff scope): the stops, all before the install.
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE= -- "$file"
check "diff mode: no base is a stop" \
  '[[ $status -eq 1 ]] && grep -q "needs IQ_MUTATION_BASE" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main --
check "diff mode: no file argument is a stop" \
  '[[ $status -eq 1 ]] && grep -q "needs one or more non-test .go file arguments" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main -- ./internal/numfmt
check "diff mode: a package argument is a stop" \
  '[[ $status -eq 1 ]] && grep -q "no package argument" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main -- ./cmd "$file"
check "diff mode: a package argument next to a file is a stop" \
  '[[ $status -eq 1 ]] && grep -q "no package argument" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main IQ_MUTATION_MUTATORS="branch/if" -- "$file"
check "diff mode: shard mutators are a stop" \
  '[[ $status -eq 1 ]] && grep -q "cannot combine" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main IQ_MUTATION_UPDATE_BASELINE=1 -- "$file"
check "diff mode: a baseline update is a stop" \
  '[[ $status -eq 1 ]] && grep -q "cannot combine" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main IQ_MUTATION_MUTANT=abc -- "$file"
check "diff mode: a single-mutant run is a stop" \
  '[[ $status -eq 1 ]] && grep -q "cannot combine" "$case_dir/out"'
gate IQ_MUTATION_DIFF=yes IQ_MUTATION_BASE=origin/main -- "$file"
check "diff mode: a value other than 1 is a stop" \
  '[[ $status -eq 1 ]] && grep -q "IQ_MUTATION_DIFF must be 1 or unset" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main -- internal/numfmt/decimal_test.go
check "diff mode: a test file is still a stop" \
  '[[ $status -eq 1 ]] && grep -q "must be an existing non-test .go file" "$case_dir/out"'
gate IQ_MUTATION_DIFF=1 IQ_MUTATION_BASE=origin/main -- cmd/root.go "$file"
check "diff mode: files of cmd and another package pass the target check (the install is the next step)" \
  '! grep -q "cannot share a run" "$case_dir/out" && grep -q "mutago" "$case_dir/out"'

# The plan: pack_diff on prepared files, no git needed.
# pack_case <name> <rows>: rows are tab-separated package, file, lines, code lines, s per mutant, backend.
pack_case() {
  case_dir="$work/dpack-$1"
  mkdir -p "$case_dir"
  printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' >"$case_dir/base.txt"
  printf '%b' "$2" >"$case_dir/files.tsv"
  bash scripts/mutation-plan.sh --diff-pack "$case_dir" >"$case_dir/plan.json" 2>"$case_dir/out"
  status=$?
}
pack_case empty ''
check "plan pack: no file gives an empty plan" '[[ $status -eq 0 && "$(cat "$case_dir/plan.json")" == "[]" ]]'
pack_case small './internal/a\tinternal/a/a.go\t40\t30\t4\t0\n./internal/b\tinternal/b/b.go\t20\t15\t4\t0\n'
check "plan pack: small files of two packages share one shard" \
  '[[ $status -eq 0 && "$(jq -c "map(.groups | length)" "$case_dir/plan.json")" == "[2]" && "$(jq -r ".[0].backend" "$case_dir/plan.json")" == "" ]]'
check "plan pack: every entry holds the merge-base" \
  '[[ "$(jq -r ".[0].mergeBase" "$case_dir/plan.json")" == aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ]]'
pack_case backend './cmd\tcmd/a.go\t40\t30\t25\t1\n./internal/a\tinternal/a/a.go\t40\t30\t4\t0\n./drivers/redis\tdrivers/redis/r.go\t10\t9\t8\t1\n'
check "plan pack: a backend package never shares a shard with another package" \
  '[[ $status -eq 0 && "$(jq -c "map(.backend) | sort" "$case_dir/plan.json")" == "[\"\",\"./cmd\",\"./drivers/redis\"]" ]] &&
   [[ "$(jq "[.[] | select(.backend != \"\") | .groups | length] | add" "$case_dir/plan.json")" == 2 ]]'
# 250 lines x 0.3 x 25 s = 31 min for each file: no two fit in 45 min.
rows=''
for i in 1 2 3 4 5 6; do rows+="./cmd\tcmd/f$i.go\t250\t200\t25\t1\n"; done
pack_case cmd6 "$rows"
check "plan pack: six cmd files of 250 lines make six shards" \
  '[[ $status -eq 0 && "$(jq length "$case_dir/plan.json")" == 6 && "$(jq -r "[.[].backend] | unique | .[]" "$case_dir/plan.json")" == ./cmd ]]'
rows=''
for i in $(seq 1 14); do rows+="./internal/p$i\tinternal/p$i/f.go\t200\t150\t40\t0\n"; done
pack_case cap "$rows"
check "plan pack: more than 12 shards grow the budget until the plan fits the cap" \
  '[[ $status -eq 0 ]] && (( $(jq length "$case_dir/plan.json") <= 12 )) && grep -q "budget of a shard grows" "$case_dir/out"'
pack_case big './internal/a\tinternal/a/a.go\t500\t400\t25\t0\n'
check "plan pack: a file over the budget gets its own shard and a warning" \
  '[[ $status -eq 0 && "$(jq length "$case_dir/plan.json")" == 1 ]] && grep -q "WARNING" "$case_dir/out"'
pack_case huge './internal/a\tinternal/a/a.go\t2000\t1500\t25\t0\n'
check "plan pack: a file over the step limit stops the plan" \
  '[[ $status -ne 0 ]] && grep -q "more than the step limit of 165 min" "$case_dir/out"'
rows=''
for i in $(seq 1 13); do rows+="./drivers/d$i\tdrivers/d$i/f.go\t10\t9\t5\t1\n"; done
pack_case backends13 "$rows"
check "plan pack: more than 12 backend packages stop the plan" \
  '[[ $status -ne 0 ]] && grep -q "13 backend packages" "$case_dir/out"'

# The plan from a diff: a small Go module in a temporary git repository.
repo="$work/repo"
mkdir -p "$repo/internal/a" "$repo/cmd"
git_in() { git -C "$repo" -c user.email=t@example.com -c user.name=t -c commit.gpgsign=false "$@"; }
printf 'module example.com/t\n\ngo 1.21\n' >"$repo/go.mod"
printf 'package a\n\nfunc One() int { return 1 }\n' >"$repo/internal/a/a.go"
printf 'package main\n\nfunc main() {}\n' >"$repo/cmd/c.go"
printf 'package a\n\nfunc Gone() int { return 0 }\n' >"$repo/internal/a/gone.go"
git -C "$repo" init -q -b main
git_in add -A
git_in commit -q -m base
base_sha=$(git -C "$repo" rev-parse HEAD)
git -C "$repo" checkout -q -b feat
diff_plan() {
  case_dir="$work/diffplan-$1"
  mkdir -p "$case_dir"
  (cd "$repo" && GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY=off bash "$root/scripts/mutation-plan.sh" --diff main) >"$case_dir/plan.json" 2>"$case_dir/out"
  status=$?
}
printf 'package a\n\nfunc One() int { return 1 }\n' >"$repo/internal/a/a_test.go"
git_in add -A && git_in commit -q -m "test only"
diff_plan testonly
check "plan diff: a test-only change gives an empty plan" '[[ $status -eq 0 && "$(cat "$case_dir/plan.json")" == "[]" ]]'
printf 'package a\n\nfunc One() int { return 1 }\n\nfunc Two(x int) int {\n\tif x > 1 {\n\t\treturn x\n\t}\n\treturn 0\n}\n' >"$repo/internal/a/a.go"
printf 'package main\n\nfunc main() { _ = 1 + 2 }\n' >"$repo/cmd/c.go"
git -C "$repo" rm -q -f internal/a/gone.go
git_in add -A && git_in commit -q -m "change"
diff_plan two
check "plan diff: two packages give a plain shard and a cmd shard" \
  '[[ $status -eq 0 && "$(jq -c "map(.backend)" "$case_dir/plan.json")" == "[\"./cmd\",\"\"]" ]]'
check "plan diff: the merge-base is the base commit" \
  '[[ "$(jq -r ".[0].mergeBase" "$case_dir/plan.json")" == "$base_sha" ]]'
check "plan diff: a deleted file and a test file are not planned" \
  '! grep -q "gone.go\|a_test.go" "$case_dir/plan.json" && grep -q "internal/a/a.go" "$case_dir/plan.json"'
check "plan diff: the plan counts the changed lines" \
  '[[ "$(jq "[.[].groups[].files[] | select(.file == \"internal/a/a.go\") | .lines] | add" "$case_dir/plan.json")" == 7 ]]'
long=$(head -c 70000 /dev/zero | tr '\0' 'x')
printf 'package a\n\n// %s\nfunc Three() int { return 3 }\n' "$long" >>"$repo/internal/a/a.go"
git_in add -A && git_in commit -q -m "long line"
diff_plan long
check "plan diff: a diff line over 60000 bytes stops the plan and names the file" \
  '[[ $status -ne 0 ]] && grep -q "internal/a/a.go" "$case_dir/out" && grep -q "over 60000 bytes" "$case_dir/out"'
git_in reset -q --hard HEAD~1
mkdir -p "$repo/tools"
printf '//go:build ignore\n\npackage main\n\nfunc main() {}\n' >"$repo/tools/gen.go"
git_in add -A && git_in commit -q -m "ignored file"
diff_plan nopkg
check "plan diff: a package that go list rejects stops the plan" \
  '[[ $status -ne 0 ]] && grep -q "go list rejects the package of tools/gen.go" "$case_dir/out"'

# The shard runner: the stops before the mutago install.
head_sha=$(git rev-parse HEAD)
shard_case() {
  # shard_case <name> <plan-json> [env assignments...]
  case_dir="$work/shard-$1"
  mkdir -p "$case_dir"
  printf '%s\n' "$2" >"$case_dir/plan.json"
  shift 2
  env -i PATH="$PATH" HOME="$HOME" GOPROXY=off "$@" bash scripts/mutation-diff-shard.sh "$case_dir/out-dir" "$case_dir/plan.json" 1 >"$case_dir/out" 2>&1
  status=$?
}
good_plan="[{\"shard\":1,\"shards\":1,\"backend\":\"\",\"mergeBase\":\"$head_sha\",\"groups\":[{\"package\":\"./internal/numfmt\",\"files\":[{\"file\":\"internal/numfmt/decimal.go\",\"lines\":3,\"codeLines\":3}]}]}]"
shard_case workers "$good_plan" IQ_MUTATION_WORKERS=2
check "shard: more than one worker is a stop" \
  '[[ $status -eq 1 ]] && grep -q "uses one worker" "$case_dir/out"'
shard_case backend "$(jq -c '.[0].backend = "./cmd"' <<<"$good_plan")"
check "shard: a backend with no IQ_*_URL variable is a stop" \
  '[[ $status -eq 1 ]] && grep -q "no IQ_\*_URL variable is set" "$case_dir/out"'
shard_case commit "$(jq -c '.[0].mergeBase = "0000000000000000000000000000000000000000"' <<<"$good_plan")"
check "shard: a merge-base that is not a commit is a stop" \
  '[[ $status -eq 1 ]] && grep -q "is not a commit of this checkout" "$case_dir/out"'
shard_case file "$(jq -c '.[0].groups[0].files[0].file = "internal/numfmt/../../etc/x.go"' <<<"$good_plan")"
check "shard: a file path with .. is a stop" \
  '[[ $status -eq 1 ]] && grep -q "groups of the plan entry are not valid" "$case_dir/out"'
shard_case dir "$(jq -c '.[0].groups[0].files[0].file = "internal/render/json.go"' <<<"$good_plan")"
check "shard: a file outside the directory of its package is a stop" \
  '[[ $status -eq 1 ]] && grep -q "groups of the plan entry are not valid" "$case_dir/out"'
shard_case testfile "$(jq -c '.[0].groups[0].files[0].file = "internal/numfmt/decimal_test.go"' <<<"$good_plan")"
check "shard: a test file is a stop" \
  '[[ $status -eq 1 ]] && grep -q "groups of the plan entry are not valid" "$case_dir/out"'
shard_case entry "$(jq -c '.[0].shard = 2' <<<"$good_plan")"
check "shard: a shard that the plan does not have is a stop" \
  '[[ $status -eq 1 ]] && grep -q "no single entry for shard 1" "$case_dir/out"'

# The verdict, on fixture artifacts.
accepted_id=$(jq -r '.mutants[0].id' mutago-baseline.json)
identity=$(jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg config "$(sha256sum .mutago.yml | cut -d' ' -f1)" \
  --arg baseline "$(sha256sum mutago-baseline.json | cut -d' ' -f1)" \
  '{commit: $commit, mutagoVersion: $mutago, goVersion: "go1.0", configSha256: $config,
    baselineSha256: $baseline, mergeBase: "bbbb"}')
# vshard <dir> <number> <total> <mutants-json> <escaped ids> [jq patch]: one shard artifact.
vshard() {
  local dir="$case_dir/artifacts/$1" ids="${5:-}" patch="${6:-.}"
  mkdir -p "$dir/groups/001"
  jq -n --argjson n "$2" --argjson t "$3" --argjson id "$identity" --argjson m "$4" \
    '{shard: $n, shards: $t, backend: "", seconds: 60, identity: $id, suspectZero: [], failedGroups: 0,
      groups: [{package: "./internal/numfmt", status: 0, report: true,
                totals: {mutants: $m, killed: $m, escaped: 0, notCovered: 0, skipped: 0, errored: 0},
                files: [{file: "internal/numfmt/decimal.go", lines: 3, codeLines: 3, mutants: $m}]}]}' |
    jq "$patch" >"$dir/shard.json"
  jq -n --arg ids "$ids" '{mutants: [($ids | split(" ")[] | select(. != "") | {id: ., file: "internal/numfmt/decimal.go", mutator: "branch/if", line: 1})]}' >"$dir/groups/001/mutago-agentic.json"
}
vplan() {
  jq -n --argjson total "$1" '[range(1; $total + 1) | {shard: ., shards: $total, backend: "", mergeBase: "bbbb", groups: []}]' >"$case_dir/plan.json"
}
vrun() {
  # vrun [env assignments...]
  env "$@" bash scripts/mutation-diff-verdict.sh "$case_dir/artifacts" "$case_dir/plan.json" "$case_dir/merged" >"$case_dir/out" 2>&1
  status=$?
}
vcase() { case_dir="$work/verdict-$1"; mkdir -p "$case_dir/artifacts"; }

vcase pass; vplan 2
vshard a 1 2 5; vshard b 2 2 7 "$accepted_id"
vrun PLAN_RESULT=success SHARDS_RESULT=success
check "verdict: good shards and an accepted escape pass" \
  '[[ $status -eq 0 && ! -f "$case_dir/merged/mutago-baseline.candidate.json" && "$(jq ".mutants | length" "$case_dir/merged/mutago-agentic.json")" == 1 ]] && grep -q "Total mutants: 12" "$case_dir/merged/summary.md"'
vcase missing; vplan 2
vshard a 1 2 5
vrun
check "verdict: a shard with no shard.json fails" \
  '[[ $status -eq 1 ]] && grep -q "shard 2/2 has no shard.json" "$case_dir/out"'
vcase newescape; vplan 2
vshard a 1 2 5 "newid1 newid2"; vshard b 2 2 7 "newid1"
vrun
check "verdict: a new escape fails and the candidate lists it once" \
  '[[ $status -eq 1 && "$(jq "[.mutants[] | select(.id == \"newid1\")] | length" "$case_dir/merged/mutago-baseline.candidate.json")" == 1 ]] &&
   [[ "$(jq "[.mutants[] | select(.id == \"newid2\")] | length" "$case_dir/merged/mutago-baseline.candidate.json")" == 1 ]] && grep -q "2 new escape" "$case_dir/out"'
vcase failedgroup; vplan 1
vshard a 1 1 5 "" '.failedGroups = 1'
vrun
check "verdict: a failed group fails" '[[ $status -eq 1 ]] && grep -q "group(s) failed" "$case_dir/out"'
vcase errored; vplan 1
vshard a 1 1 5 "" '.groups[0].totals.errored = 1'
vrun
check "verdict: an errored mutant fails" '[[ $status -eq 1 ]] && grep -q "errored or timed out" "$case_dir/out"'
vcase commit; vplan 1
vshard a 1 1 5 "" '.identity.commit = "0000"'
vrun
check "verdict: a shard of another commit fails" '[[ $status -eq 1 ]] && grep -q "identity commit" "$case_dir/out"'
vcase mergebase; vplan 1
vshard a 1 1 5 "" '.identity.mergeBase = "cccc"'
vrun
check "verdict: a shard with another merge-base fails" '[[ $status -eq 1 ]] && grep -q "differs from the plan" "$case_dir/out"'
vcase goversion; vplan 2
vshard a 1 2 5; vshard b 2 2 5 "" '.identity.goVersion = "go2.0"'
vrun
check "verdict: shards with different Go versions fail" '[[ $status -eq 1 ]] && grep -q "different Go versions" "$case_dir/out"'
vcase planfailed; vplan 1
vshard a 1 1 5
vrun PLAN_RESULT=failure
check "verdict: a failed plan job fails" '[[ $status -eq 1 ]] && grep -q "the plan job ended as failure" "$case_dir/out"'
vcase shardsfailed; vplan 1
vshard a 1 1 5
vrun PLAN_RESULT=success SHARDS_RESULT=cancelled
check "verdict: cancelled shard jobs fail" '[[ $status -eq 1 ]] && grep -q "the shard jobs ended as cancelled" "$case_dir/out"'
vcase emptyplan; echo '[]' >"$case_dir/plan.json"
vrun PLAN_RESULT=success SHARDS_RESULT=skipped
check "verdict: an empty plan passes" '[[ $status -eq 0 ]] && grep -q "nothing to mutate" "$case_dir/merged/summary.md"'
vcase suspect; vplan 1
vshard a 1 1 0 "" '.suspectZero = ["internal/numfmt/decimal.go"]'
vrun
check "verdict: a file with no mutant warns and the run still passes" \
  '[[ $status -eq 0 ]] && grep -q "^::warning::" "$case_dir/out" && grep -q "Warning:" "$case_dir/merged/summary.md"'
vcase unexpected; vplan 1
vshard a 1 1 5; vshard z 9 9 5
vrun
check "verdict: a shard that the plan does not have fails" '[[ $status -eq 1 ]] && grep -q "shard 9 is not in the plan" "$case_dir/out"'
vcase badplan; echo 'not json' >"$case_dir/plan.json"
vrun
check "verdict: a plan that is not valid stops the verdict" '[[ $status -eq 2 ]]'

echo "mutation-diff: $passed passed, $failed failed"
[[ "$failed" -eq 0 ]]
