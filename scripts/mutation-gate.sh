#!/usr/bin/env bash
# Mutation gate. Runs github.com/quality-gates/mutago and lets it exit-code-enforce:
# --fail-on-escaped exits 4 the moment a mutant survives a covered test, so this
# wrapper only shapes the scope and forwards the exit code. Serial workers plus a
# bounded per-mutant timeout keep the container-integration runs stable.
#
# Version-hermetic invocation. mutago is pinned by MUTAGO_VERSION (below) and provisioned
# per run into a throwaway GOBIN via `go install <pinned>@version`, then executed
# directly. This removes any dependency on a mutago binary on PATH and never touches
# this module's go.mod. Direct execution is deliberate over `go run`: `go run` collapses a non-zero program exit to 1, which would
# make an escaped-mutant verdict (exit 4) indistinguishable from a run error, whereas the
# installed binary preserves mutago's exact exit codes and streams output live.
#
# Stable policy lives in the committed .mutago.yml (passed via --config on the gate
# invocation); the invocation-varying and load-bearing flags stay on the command line
# here. The baseline path stays a CLI flag because mutago has no config key for it.
#
# What counts as a failure. --fail-on-escaped fails on any *escaped* mutant that is
# not recorded in the baseline (mutago-baseline.json); an escaped mutant means a
# covered test asserts nothing, so strengthen the test. --coverage keeps uncovered
# lines out of the escaped set (they become "not covered", never a failure), so the
# gate mirrors the old zero-survivor-on-covered-code contract. An errored mutant — most
# often one that timed out — is *not* gated by mutago itself: its MSI arithmetic counts an
# error as a kill, so a hung mutant would pass silently. This wrapper therefore reads
# report.json after a gate run and fails on stats.errorCount > 0, naming each errored
# mutant. That turns the tail from a silent hole into a signal, which is what lets
# --timeout-coefficient stay tight (5; raise it for a legitimately slow package with
# IQ_MUTATION_TIMEOUT_COEFFICIENT, a positive integer multiplier of the instrumented
# baseline). The check is skipped in the non-gating modes (--update-baseline, single-mutant).
# Every gate run also writes mutago-agentic.json (--logger-agentic-json): enriched,
# LLM-consumable data for each escaped mutant, including the stable id used below to
# re-run one mutant. It is regenerated per run and gitignored next to report.json, as is
# mutago-summary.json (--logger-summary-json). The CI `deep-mutate` shards upload these
# files, and `deep-badge` merges them with scripts/mutation-verdict.sh into the stored
# state and the README mutation badge.
#
# Two gate policies. Every package is zero-survivor on covered code: --fail-on-escaped
# against the committed baseline. One exception applies. A full scan of ./cmd holds a
# covered-code MSI floor (cmd_covered_msi_floor below) in place of that flag. cmd holds
# the composition root and the presentation code, and its critical paths move to internal
# packages step by step. mutation-bar-tiering-plan.md records the decision and the
# measurements. The diff-scoped form cannot carry a per-package table: it enumerates
# several packages in one mutago invocation, and one invocation carries one flag set. The
# diff form therefore stays zero-survivor, which keeps every pushed line on the
# zero-survivor bar. For the same reason a target list that holds ./cmd together with
# another package is a hard stop. The floor ignores the baseline. mutago's
# checkCoveredMsiGate reads the report score only, so the cmd entries in
# mutago-baseline.json become documentation of accepted equivalents.
#
# Accepted baseline entries are justified one-per-line in mutago-baseline.notes.md
# (committed): consult it before re-litigating a refactor-resurfaced equivalent, and
# add a line there whenever you extend mutago-baseline.json.
#
# Scope. With no argument it scopes to the branch's diff against the base ref
# (IQ_MUTATION_BASE, default origin/main) via mutago's --git-diff-lines, so a change
# is gated only on the lines it touched — the fast path for a feature branch. The
# base is handed to mutago as the merge-base *commit* with HEAD, so a local base ref
# that has drifted from the remote still yields the true branch point, and it works
# from a linked git worktree (git computes the diff, mutago restricts to it). A
# diff-scoped run also narrows its *targets* to the packages holding changed .go files
# instead of enumerating ./...: changed lines are a subset of changed files, which are a
# subset of changed packages, so the mutant set is identical while the enumeration pass —
# the memory peak of a run, since mutago loads and instruments every target package —
# shrinks to what the branch touched. Only packages with a changed non-test file count:
# mutago mutates source rather than tests, and a mutant is killable only by its own
# package's tests, so a package whose diff is all _test.go contributes nothing and is
# dropped — gate-neutral, and it keeps ./e2e (whose TestMain rebuilds the binary) out of
# a run that would otherwise pay that per mutant. A test-only diff keeps those packages
# as targets rather than widening to ./..., since it yields no mutants either way. An
# empty derivation falls back to ./...; a run never starts with no targets. Pass a
# package path (e.g. ./cmd) for a full mutation
# scan of that package instead: a path drops the diff-scoping flags and mutates the
# whole package. Set IQ_MUTATION_BASE= (empty) to mutate the whole module.
#
# IQ_MUTATION_MUTANT=<id> re-runs a single mutant by its stable id (copy the id field
# from mutago-agentic.json) via mutago's --run-mutant-id: after strengthening a test,
# re-verify one survivor in seconds instead of rerunning the whole gate, and probe an
# order-dependent escape for flakiness by running the same mutant a few times. mutago
# suppresses the gate verdict and summary in this mode, so it is a diagnostic, not a
# gate. It can also report a FALSE KILLED: measured on 2026-09-05, three mutants came
# back killed from this mode while the same mutation, applied by hand, left the suite
# green. Confirm a kill by ground truth instead, `git apply` of the mutant diff plus
# the package suite. An id is also tied to the enumeration that produced it, so an id
# from a `--match` scan does not resolve in a plain package run. Point it at the mutant's package (e.g. ./drivers/file) for a fast enumeration;
# a whole-module ./... target still works but re-enumerates everything.
#
# IQ_MUTATION_DRYRUN=1 is a mutant-count preview whose cost depends on the form: a
# package-arg dry run (e.g. ./internal/numfmt) is instant and runs no tests; a
# diff-scoped or whole-module (./...) dry run first runs the --coverage instrumented
# test pass — whole-target, memory-heavy, buffered until exit — before counting. The
# count is a whole-target upper bound either way. Scope dry runs to one package and
# never launch one alongside a live gate, because the two contend for memory (oomd kill). A dry
# run reads the same config as a real run (.mutago.yml, or the shard config), so the count
# obeys skip_without_test and the shard mutators.
#
# IQ_MUTATION_UPDATE_BASELINE=1 records the current survivors into mutago-baseline.json
# and exits 0 (accept genuine equivalent mutants deliberately, then commit the file
# and add a justification line to mutago-baseline.notes.md). mutago itself *replaces*
# the baseline with the run's survivors, which under diff scoping would silently drop
# every accepted entry outside the diff; this wrapper snapshots the committed file and
# merges it back, so an update is always an append and the diff shows exactly what was
# accepted. The run doubles as the verification: it reruns the same mutants, so the ids
# it prints are the survivors that remain after your test changes. Review that list
# against mutago-baseline.notes.md before committing — an id with no justification line
# is one to re-verify with IQ_MUTATION_MUTANT=<id> (order-dependent escapes are
# flaky-killable) rather than accept.
#
# File targets. A path that ends in .go is a full-scan target, like a package path. mutago
# mutates that one file and runs the tests of its package. The wrapper changes each file
# target to an absolute path before it calls mutago: a target written as ./pkg/file.go gives
# different mutant ids (upstream bug quality-gates/mutago#248), and those ids do not match
# mutago-baseline.json. An absolute path gives the same ids as a package run.
#
# IQ_MUTATION_MUTATORS="a/b c/d" is shard mode. mutago then uses only the named mutators: the
# wrapper writes a temporary config, .mutago.yml plus an enable_mutators list, and passes it
# to --config. Shard mode needs a package or file argument. Each name must be a mutator that
# `mutago --list-mutators` shows, or the wrapper stops. An empty value means all mutators.
# A shard runs with no gate flag. mutago merges identical edits of different mutators into
# one mutant, and the mutator it keeps, and thus the stable id, depends on the enabled set.
# So a shard cannot compare its ids with mutago-baseline.json on its own. The merge of all
# shards of a package is the gate (see scripts/mutation-verdict.sh). A file target of
# ./cmd with all mutators also has no gate flag, because the floor needs the score of the
# full package. The errored-mutant check stays on in both cases.
#
# IQ_MUTATION_INSTALL_DIR=<dir> only installs the pinned mutago into <dir> (with a bounded
# retry) and prints the path of the binary. IQ_MUTATION_MUTAGO_BIN=<path> then gives that
# binary to each gate run, so a caller that runs the gate many times installs once. The
# wrapper accepts the path only for an executable file built from the pinned version.
#
# IQ_MUTATION_WORKERS overrides the serial default (1), forwarded to --workers. Serial is
# right for a container-backed suite rerun per mutant; 2-3 is the useful range when the
# run is already memory-bounded, e.g. inside a systemd-run MemoryHigh=10G unit.
#
# The suite provisions its own containers, but mutago reruns it per mutant, so with ONE
# worker start a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
# The Redis integration tests pin themselves to reserved databases (14 and 15),
# so a shared stack seeded on DB 0 by scripts/seed-redis.sh keeps its data through the run.
#
# A shared stack and parallel workers do not mix. The suites pin fixed state (the Redis
# databases above, the mongo database iq_test), so two test processes on one stack fail on
# each other's data, and mutago scores a failed suite as a kill. Measured 2026-09-03: 2 of 3
# concurrent cmd suites fail, and a scan reported 0 escapes in 758 mutants that a serial run
# had scored at about 9 percent. The wrapper therefore stops when IQ_MUTATION_WORKERS is above
# 1 and any IQ_*_URL is set. For parallel workers unset the URLs; each test process then
# starts its own containers, which costs about 8 s more per mutant and is isolated.
set -uo pipefail

# Pinned mutago version. Renovate reads the line below. mutago stays here and not in
# scripts/tool-versions.env, because scripts/mutation-shard.sh and mutation-verdict.sh
# read this exact line.
# renovate: datasource=go depName=github.com/quality-gates/mutago/v2
MUTAGO_VERSION=v2.10.23
mutago_pkg=github.com/quality-gates/mutago/v2/cmd/mutago

# Covered-code MSI floor for a full scan of ./cmd. The value comes from the finished cmd
# scan, per mutation-bar-tiering-plan.md: the measured covered-code MSI, minus 2 points for
# run-to-run noise, with a cap of 90. It is a committed policy literal. It is never an
# environment variable and never a caller flag.
cmd_covered_msi_floor=90

# Per-mutant timeout, as a multiplier of the instrumented baseline. 5 is tight enough that
# an infinite-loop mutant dies in minutes rather than tens of them; the errored-mutant check
# after the run is what keeps that bound from hiding a legitimately slow suite. Raise it when
# a real suite needs the room. Validated before any work: a non-positive or non-numeric
# value is a stop, never a silent fallback to the default.
timeout_coefficient="${IQ_MUTATION_TIMEOUT_COEFFICIENT-5}"
if [[ ! "$timeout_coefficient" =~ ^[1-9][0-9]*$ ]]; then
  echo "mutation gate: IQ_MUTATION_TIMEOUT_COEFFICIENT must be a positive integer, got '$timeout_coefficient'" >&2
  exit 1
fi

# Worker count. Serial (1) is the default because a container-backed suite rerun per mutant
# contends for the same backends, and because the run is memory-dominant. Raise it only when
# the run is memory-bounded by something else — inside the systemd-run MemoryHigh=10G unit,
# 2-3 is the useful range. Validated like the coefficient: a bad value is a stop, never a
# silent fallback, so a typo cannot quietly halve the gate's rigour by starving it.
workers="${IQ_MUTATION_WORKERS-1}"
if [[ ! "$workers" =~ ^[1-9][0-9]*$ ]]; then
  echo "mutation gate: IQ_MUTATION_WORKERS must be a positive integer, got '$workers'" >&2
  exit 1
fi
# Parallel workers must not share a backend (see the header). A shared-stack URL with more
# than one worker is a stop: the verdicts would be false kills, not a slower run.
if [[ "$workers" -gt 1 ]]; then
  shared_urls=$(env | grep -oE '^IQ_[A-Z0-9_]+_URL=' | tr -d '=' | paste -sd' ')
  if [[ -n "$shared_urls" ]]; then
    echo "mutation gate: IQ_MUTATION_WORKERS=$workers with a shared backend ($shared_urls) scores false kills; unset the URL(s) so each worker starts its own containers, or use 1 worker" >&2
    exit 1
  fi
fi

# A package-path argument (one starting with . or /) selects a full scan of that
# package: the git-diff flags are dropped so the whole package is mutated, not just
# a diff. Otherwise the run is scoped to the branch diff via --git-diff-lines.
#
# A path that ends in .go is a file target. It is changed to an absolute path here (see the
# header, mutago#248), and it must be a non-test Go file inside this repository.
repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "mutation gate: run it inside the iq repository" >&2
  exit 1
}
has_path=0
normalized=()
cmd_package=0
cmd_files=0
other_targets=0
for arg in "$@"; do
  if [[ "$arg" == *.go ]]; then
    if [[ "$arg" == *_test.go || ! -f "$arg" ]]; then
      echo "mutation gate: file target '$arg' must be an existing non-test .go file" >&2
      exit 1
    fi
    abs="$(cd "$(dirname "$arg")" && pwd -P)/$(basename "$arg")"
    rel="${abs#"$repo_root"/}"
    if [[ "$rel" == "$abs" ]]; then
      echo "mutation gate: file target '$arg' is not inside $repo_root" >&2
      exit 1
    fi
    if [[ "$(dirname "$rel")" == "cmd" ]]; then
      cmd_files=$((cmd_files + 1))
    else
      other_targets=$((other_targets + 1))
    fi
    normalized+=("$abs")
    has_path=1
    continue
  fi
  [[ "$arg" == .* || "$arg" == /* ]] && has_path=1
  if [[ "$arg" == "./cmd" || "$arg" == "./cmd/..." ]]; then
    cmd_package=$((cmd_package + 1))
  else
    other_targets=$((other_targets + 1))
  fi
  normalized+=("$arg")
done
set -- "${normalized[@]}"

# Shard mode (see the header). The names are examined here for their form, and again after
# the mutago install against `mutago --list-mutators`. An empty value means all mutators.
shard_mutators=()
read -ra shard_mutators <<<"${IQ_MUTATION_MUTATORS-}"
if [[ ${#shard_mutators[@]} -gt 0 ]]; then
  if [[ "$has_path" -eq 0 ]]; then
    echo "mutation gate: IQ_MUTATION_MUTATORS needs a package or file argument; a diff-scoped run cannot be a shard" >&2
    exit 1
  fi
  for name in "${shard_mutators[@]}"; do
    if [[ ! "$name" =~ ^[a-z_]+/[a-z_-]+$ ]]; then
      echo "mutation gate: IQ_MUTATION_MUTATORS holds '$name', which is not a mutator name (family/name)" >&2
      exit 1
    fi
  done
  if grep -qE '^(enable|disable)_mutators:' .mutago.yml; then
    echo "mutation gate: .mutago.yml sets a mutator list, so shard mode cannot add one" >&2
    exit 1
  fi
fi

# Gate policy. Every target keeps the zero-survivor contract on covered code
# (--fail-on-escaped against the baseline). A full scan of ./cmd is the one exception: it
# passes on the covered-code MSI floor above. The two policies cannot share a run, because
# one mutago invocation carries one flag set, so a target list that mixes ./cmd (or a file
# under cmd/) with another target stops here. A part of ./cmd (a shard of the package, or
# files under cmd/) cannot judge the floor, so it runs with no gate flag. This selection
# runs before the mutago install, so a bad target list fails in a second and installs
# nothing.
gate=(--fail-on-escaped)
cmd_policy=0
if [[ "$has_path" -eq 1 ]]; then
  cmd_targets=$((cmd_package + cmd_files))
  if [[ "$cmd_targets" -gt 0 && "$other_targets" -gt 0 ]] || [[ "$cmd_package" -gt 1 ]] ||
    [[ "$cmd_package" -gt 0 && "$cmd_files" -gt 0 ]]; then
    echo "mutation gate: ./cmd holds the covered-code MSI floor and every other target holds the zero-survivor policy; one mutago invocation carries one flag set, so the two policies cannot share a run. Scan ./cmd on its own." >&2
    exit 1
  fi
  if [[ "$cmd_package" -eq 1 && ${#shard_mutators[@]} -eq 0 ]]; then
    gate=(--min-covered-msi "$cmd_covered_msi_floor")
    cmd_policy=1
    echo "mutation gate: policy for ./cmd is covered-code MSI >= ${cmd_covered_msi_floor} (full scans only; the diff gate stays zero-survivor)"
  elif [[ "$cmd_targets" -gt 0 ]]; then
    gate=()
    echo "mutation gate: this is a part of ./cmd, so it has no gate flag; the floor of ${cmd_covered_msi_floor} applies to the merged ./cmd shards (errored mutants still fail)"
  fi
fi
# A shard has no gate flag (see the header): the merge of all shards of a package decides.
if [[ ${#shard_mutators[@]} -gt 0 ]]; then
  gate=()
  cmd_policy=0
fi

# Install the pinned mutago into the directory $1 with a bounded retry. The install reads
# the module proxy, so a short network or DNS error must not stop a long shard job. Three
# attempts, with a backoff of 10 s and 20 s plus up to 5 s of jitter. The install is
# idempotent, so a retry is safe.
install_mutago() {
  local dir="$1" attempt
  for attempt in 1 2 3; do
    if GOBIN="$dir" go install "${mutago_pkg}@${MUTAGO_VERSION}"; then
      return 0
    fi
    [[ "$attempt" -eq 3 ]] && break
    echo "mutation gate: mutago install attempt $attempt failed; trying again" >&2
    sleep $((attempt * 10 + RANDOM % 5))
  done
  echo "mutation gate: could not install mutago ${MUTAGO_VERSION}" >&2
  return 1
}

# IQ_MUTATION_INSTALL_DIR=<dir> installs the pinned mutago into <dir>, prints the path of
# the binary, and exits. A caller that runs the gate many times (the shard runner, the
# shard plan) installs once and gives the path to each gate run in IQ_MUTATION_MUTAGO_BIN.
if [[ -n "${IQ_MUTATION_INSTALL_DIR-}" ]]; then
  mkdir -p "$IQ_MUTATION_INSTALL_DIR" || exit 1
  install_mutago "$IQ_MUTATION_INSTALL_DIR" || exit 1
  echo "$(cd "$IQ_MUTATION_INSTALL_DIR" && pwd -P)/mutago"
  exit 0
fi

# Provision the pinned mutago into a throwaway GOBIN and run that binary directly, so the
# gate needs no mutago on PATH and preserves exact exit codes (see header). Cleaned on any
# exit. `go install pkg@version` is module-independent: it does not read or write go.mod.
# IQ_MUTATION_MUTAGO_BIN names a binary that is already installed. The wrapper uses it only
# when it is an executable file whose build information (`go version -m`) shows the pinned
# module version. Any other value stops the run, so a stale binary never runs the gate.
mutago_bindir=$(mktemp -d) || {
  echo "mutation gate: could not create temp dir" >&2
  exit 1
}
baseline_snapshot=""
shard_config=""
cleanup() {
  rm -rf "$mutago_bindir"
  [[ -n "$baseline_snapshot" ]] && rm -f "$baseline_snapshot"
  [[ -n "$shard_config" ]] && rm -f "$shard_config"
  return 0
}
trap cleanup EXIT
if [[ -n "${IQ_MUTATION_MUTAGO_BIN-}" ]]; then
  if [[ ! -f "$IQ_MUTATION_MUTAGO_BIN" || ! -x "$IQ_MUTATION_MUTAGO_BIN" ]]; then
    echo "mutation gate: IQ_MUTATION_MUTAGO_BIN='$IQ_MUTATION_MUTAGO_BIN' is not an executable file" >&2
    exit 1
  fi
  built=$(go version -m "$IQ_MUTATION_MUTAGO_BIN" 2>/dev/null | awk '$1 == "mod" { print $2 " " $3 }')
  if [[ "$built" != "github.com/quality-gates/mutago/v2 ${MUTAGO_VERSION}" ]]; then
    echo "mutation gate: IQ_MUTATION_MUTAGO_BIN is not mutago ${MUTAGO_VERSION} (build information: '${built:-none}')" >&2
    exit 1
  fi
  mutago="$IQ_MUTATION_MUTAGO_BIN"
else
  install_mutago "$mutago_bindir" || exit 1
  mutago="$mutago_bindir/mutago"
fi

# The config for this run: the committed .mutago.yml, or in shard mode a temporary copy
# with an enable_mutators list (the same format as a chunk-NN.yml of the chunk scan). Each
# name must be a mutator of the pinned mutago, so a typo stops the run here with a clear
# message and never becomes a run with fewer mutants.
config=.mutago.yml
if [[ ${#shard_mutators[@]} -gt 0 ]]; then
  known=$("$mutago" --list-mutators) || {
    echo "mutation gate: could not list the mutators of mutago ${MUTAGO_VERSION}" >&2
    exit 1
  }
  for name in "${shard_mutators[@]}"; do
    if ! grep -qxF "$name" <<<"$known"; then
      echo "mutation gate: '$name' is not a mutator of mutago ${MUTAGO_VERSION} (see mutago --list-mutators)" >&2
      exit 1
    fi
  done
  shard_config=$(mktemp --suffix=.yml) || {
    echo "mutation gate: could not create temp file" >&2
    exit 1
  }
  {
    cat .mutago.yml
    printf 'enable_mutators:\n'
    for name in "${shard_mutators[@]}"; do printf '  - "%s"\n' "$name"; done
  } >"$shard_config"
  config="$shard_config"
  echo "mutation gate: shard mode, mutators: ${shard_mutators[*]}"
fi

scope=()
diff_ref=""
base="${IQ_MUTATION_BASE-origin/main}"
if [[ "$has_path" -eq 0 && -n "$base" ]]; then
  # Diff against the branch point. merge-base is robust to a base ref that has
  # advanced on the remote; fall back to the ref itself, then to a full scan.
  if ref=$(git merge-base "$base" HEAD 2>/dev/null) || ref=$(git rev-parse --verify --quiet "$base"); then
    scope=(--git-diff-lines --git-diff-base "$ref")
    diff_ref="$ref"
  else
    echo "mutation gate: base ref '$base' not found; scanning full scope" >&2
  fi
fi

# Targets. A path argument narrows to that package; otherwise the default is the whole
# module. A diff-scoped run instead enumerates only the packages holding changed .go
# files: --git-diff-lines already restricts *mutation* to changed lines, and changed
# lines ⊆ changed files ⊆ changed packages, so the mutant set is provably identical —
# only the enumeration shrinks, and enumeration is what pins memory on a whole-module
# target. Deleted files are excluded (--diff-filter=d); go list drops any directory that
# is not a package (testdata, fixtures) so a stray path cannot abort the run.
targets=("$@")
if [[ "$has_path" -eq 0 ]]; then
  targets=(./...)
  if [[ -n "$diff_ref" ]]; then
    mapfile -t changed_go < <(git diff --name-only --diff-filter=d "$diff_ref" -- '*.go')
    changed_dirs=()
    if [[ ${#changed_go[@]} -gt 0 ]]; then
      # Only packages with a changed *non-test* file can contribute: mutago mutates
      # source, not tests, and a mutant is killable only by its own package's tests, so
      # a package whose diff is all _test.go adds no mutants and kills nothing. Dropping
      # it is gate-neutral and can be a large saving — ./e2e in the target list makes
      # every mutant pay its TestMain binary rebuild.
      mapfile -t changed_dirs < <(
        printf '%s\n' "${changed_go[@]}" | grep -v '_test\.go$' |
          while IFS= read -r file; do [[ -n "$file" ]] && dirname "$file"; done | sort -u
      )
      if [[ ${#changed_dirs[@]} -eq 0 ]]; then
        # A test-only diff yields no mutants whatever the targets are, so keep the
        # enumeration narrow rather than letting the empty derivation fall through to
        # the whole module below.
        mapfile -t changed_dirs < <(
          printf '%s\n' "${changed_go[@]}" |
            while IFS= read -r file; do [[ -n "$file" ]] && dirname "$file"; done | sort -u
        )
        echo "mutation gate: only test files changed; enumerating their packages (no mutants either way)"
      fi
    fi
    changed_pkgs=()
    for dir in "${changed_dirs[@]}"; do
      [[ -n "$dir" ]] || continue
      # A repo-root file yields ".", which is already a valid package path.
      pkg="./$dir"
      [[ "$dir" == "." ]] && pkg="."
      go list "$pkg" >/dev/null 2>&1 && changed_pkgs+=("$pkg")
    done
    if [[ ${#changed_pkgs[@]} -gt 0 ]]; then
      targets=("${changed_pkgs[@]}")
      echo "mutation gate: enumerating ${#changed_pkgs[@]} changed package(s): ${changed_pkgs[*]}"
    elif [[ ${#changed_go[@]} -eq 0 ]]; then
      # The branch changes no Go file at all (docs, scripts, config). --git-diff-lines
      # restricts mutation to changed lines, so there is provably nothing to mutate and
      # the only possible verdict is a pass — but the ./... fallback below would spend a
      # whole-module enumeration proving it. Report and pass instead.
      echo "mutation gate: no Go files changed against ${base}; nothing to mutate"
      exit 0
    else
      echo "mutation gate: no changed Go packages resolved; enumerating the whole module"
    fi
  fi
fi

# Dry run is a mutant-count preview whose cost depends on the form. A package-arg dry
# run is instant and runs no tests; a diff-scoped or whole-module (./...) dry run first
# runs the --coverage instrumented test pass (whole-target, memory-heavy, buffered)
# before counting. The count is an upper bound. Since mutago v2.10.14 it applies
# --git-diff-lines. The dry run must read .mutago.yml like the real run, because the
# skip keys in that file change which source files mutago mutates. In shard mode it reads
# the shard config, so the count obeys the shard mutators too. Scope dry runs to one
# package and never run one beside a live gate.
if [[ "${IQ_MUTATION_DRYRUN-}" == "1" ]]; then
  if [[ "$has_path" -eq 1 ]]; then
    echo "mutation gate: dry run (instant mutant-count preview, no tests)"
  else
    echo "mutation gate: dry run (runs the --coverage instrumented pass first; whole-target, memory-heavy)"
  fi
  "$mutago" --dry-run --config "$config" "${scope[@]}" "${targets[@]}"
  exit $?
fi

baseline_file=mutago-baseline.json
mode=()
if [[ "${IQ_MUTATION_UPDATE_BASELINE-}" == "1" ]]; then
  echo "mutation gate: updating baseline (accepting current survivors, no gate)"
  mode=(--update-baseline)
  # mutago *replaces* the baseline with the current run's survivors. Under the default
  # diff scoping that is destructive: every previously accepted entry outside the diff
  # disappears, silently un-accepting equivalents this branch never looked at. Snapshot
  # the committed file so the merge below can put them back.
  if [[ -f "$baseline_file" ]]; then
    baseline_snapshot=$(mktemp) || {
      echo "mutation gate: could not create temp file" >&2
      exit 1
    }
    cp "$baseline_file" "$baseline_snapshot"
  fi
fi

# Single-mutant diagnostic: mutago runs only this id and suppresses the gate verdict.
mutant=()
if [[ -n "${IQ_MUTATION_MUTANT-}" ]]; then
  echo "mutation gate: re-running single mutant ${IQ_MUTATION_MUTANT} (diagnostic, no gate)"
  mutant=(--run-mutant-id="${IQ_MUTATION_MUTANT}")
fi

# A candidate from an earlier run lists escapes that this run did not find, so remove it
# before the run. Only a gate run that fails on an escape writes a new one (see below).
rm -f mutago-baseline.candidate.json

"$mutago" \
  --config "$config" \
  --coverage \
  "${gate[@]}" \
  --baseline mutago-baseline.json \
  --logger-agentic-json \
  --logger-summary-json \
  --ignore-msi-with-no-mutations \
  --workers "$workers" \
  --timeout-coefficient "$timeout_coefficient" \
  "${mode[@]}" "${mutant[@]}" "${scope[@]}" "${targets[@]}"
status=$?

# Restore what a diff-scoped --update-baseline just dropped: merge the snapshot back,
# keeping its entries in their original order so the commit diff is a pure append, and
# name the ids this run actually accepted. Those ids are what the reviewer checks against
# mutago-baseline.notes.md — an id here that has no justification line is the signal to
# re-verify it (IQ_MUTATION_MUTANT=<id>) rather than commit it.
if [[ ${#mode[@]} -gt 0 && -n "$baseline_snapshot" && -f "$baseline_file" ]]; then
  if ! python3 - "$baseline_snapshot" "$baseline_file" <<'PY'; then
import json
import sys

snapshot_path, baseline_path = sys.argv[1], sys.argv[2]
try:
    with open(snapshot_path, encoding="utf-8") as handle:
        kept = json.load(handle)
    with open(baseline_path, encoding="utf-8") as handle:
        written = json.load(handle)
except (OSError, ValueError) as err:
    print("mutation gate: could not merge baseline: {}".format(err), file=sys.stderr)
    sys.exit(1)

entries = list(kept.get("mutants") or [])
seen = {entry.get("id") for entry in entries}
added = [entry for entry in (written.get("mutants") or []) if entry.get("id") not in seen]
entries.extend(added)
kept["mutants"] = entries

with open(baseline_path, "w", encoding="utf-8") as handle:
    handle.write(json.dumps(kept, indent=1) + "\n")

if added:
    print("mutation gate: accepted {} new baseline entr{}:".format(
        len(added), "y" if len(added) == 1 else "ies"))
    for entry in added:
        print("  {} {}:{} {}".format(
            entry.get("id", "?"),
            entry.get("file", "?"),
            entry.get("line", "?"),
            entry.get("mutator", "?"),
        ))
else:
    print("mutation gate: baseline unchanged (no new survivors accepted)")
PY
    echo "mutation gate: baseline merge failed; restoring the committed file" >&2
    cp "$baseline_snapshot" "$baseline_file"
    exit 1
  fi
  echo "mutation gate: justify each new entry in mutago-baseline.notes.md before committing"
fi

# Errored mutants are not gated by mutago: its MSI arithmetic counts an error as a kill, so
# a mutant that hung or crashed the suite would pass silently — exactly the hole a tight
# --timeout-coefficient would otherwise widen. In gate mode only (--update-baseline and the
# single-mutant diagnostic are deliberately non-gating), read report.json and fail on any
# error, naming each one. A missing or unreadable report is a warning, never a crash: the
# code mutago already returned stays the verdict. python3 over jq: it ships with the box.
if [[ ${#mode[@]} -eq 0 && ${#mutant[@]} -eq 0 ]]; then
  errored=$(
    python3 - <<'PY'
import json
import sys

try:
    with open("report.json", encoding="utf-8") as handle:
        report = json.load(handle)
except (OSError, ValueError):
    sys.exit(2)

if int(report.get("stats", {}).get("errorCount", 0)) <= 0:
    sys.exit(0)

for mutant in report.get("errored") or []:
    mutator = mutant.get("mutator") or {}
    print("  {}:{} {}".format(
        mutator.get("originalFilePath", "?"),
        mutator.get("originalStartLine", "?"),
        mutator.get("mutatorName", "?"),
    ))
sys.exit(1)
PY
  )
  report_status=$?
  if [[ "$report_status" -eq 1 ]]; then
    echo "mutation gate FAILED: a mutant errored or timed out. mutago does not gate these (it scores an error as a kill), so the wrapper fails the run — an errored mutant is unverified, not killed. Fix the hang, or raise IQ_MUTATION_TIMEOUT_COEFFICIENT (default 5) if the suite is legitimately slow:" >&2
    [[ -n "$errored" ]] && echo "$errored" >&2
    [[ "$status" -eq 0 ]] && exit 1
  elif [[ "$report_status" -ne 0 ]]; then
    echo "mutation gate: could not read report.json; errored mutants were not checked" >&2
  fi
fi

# A gate run that fails on an escape names the id of each new escape, and writes
# mutago-baseline.candidate.json: the committed baseline plus those escapes, in the
# format an update run writes. The CI job uploads it with report.json and
# mutago-agentic.json, so a failed run on CI gives the ids without a local re-run.
# Copy the candidate over mutago-baseline.json only for escapes that are genuine
# equivalents, each with its line in mutago-baseline.notes.md; kill the others.
if [[ ${#mode[@]} -eq 0 && ${#mutant[@]} -eq 0 && "$status" -eq 4 && "$cmd_policy" -eq 0 ]]; then
  if ! python3 - "$baseline_file" <<'PY'; then
import json
import sys

baseline_path = sys.argv[1]
try:
    with open("mutago-agentic.json", encoding="utf-8") as handle:
        escaped = json.load(handle).get("mutants") or []
    with open(baseline_path, encoding="utf-8") as handle:
        baseline = json.load(handle)
except (OSError, ValueError) as err:
    print("mutation gate: could not list the new escapes: {}".format(err), file=sys.stderr)
    sys.exit(1)

entries = list(baseline.get("mutants") or [])
seen = {entry.get("id") for entry in entries}
added = []
for mutant in escaped:
    if mutant.get("id") in seen:
        continue
    seen.add(mutant.get("id"))
    added.append({key: mutant.get(key) for key in ("id", "file", "mutator", "line")})
if not added:
    sys.exit(0)
baseline["mutants"] = entries + added
with open("mutago-baseline.candidate.json", "w", encoding="utf-8") as handle:
    handle.write(json.dumps(baseline, indent=1) + "\n")
print("mutation gate: new escapes (id file:line mutator), also in mutago-baseline.candidate.json:")
for entry in added:
    print("  {} {}:{} {}".format(entry["id"], entry["file"], entry["line"], entry["mutator"]))
PY
    echo "mutation gate: could not write mutago-baseline.candidate.json" >&2
  fi
fi

# mutago exits 0 (gate passed), 4 (a mutant escaped), or another code for a run
# error; --dry-run and --update-baseline always exit 0. Forward the verdict.
if [[ "$status" -eq 0 ]]; then
  if [[ ${#mode[@]} -eq 0 && ${#gate[@]} -eq 0 ]]; then
    echo "mutation gate: run finished with no gate flag; the merged verdict of all shards decides (scripts/mutation-verdict.sh)"
  elif [[ ${#mode[@]} -eq 0 ]]; then
    echo "mutation gate passed: no new escaped mutants"
  fi
  exit 0
fi
if [[ "$status" -eq 4 ]]; then
  if [[ "$cmd_policy" -eq 1 ]]; then
    echo "mutation gate FAILED: the covered-code MSI of ./cmd is below the floor of ${cmd_covered_msi_floor} (see the diffs above); kill escaped mutants until the score is above the floor. Never lower the floor." >&2
  else
    echo "mutation gate FAILED: a mutant escaped (see the diffs above); kill it or, if it is a genuine equivalent mutant, accept it with IQ_MUTATION_UPDATE_BASELINE=1" >&2
  fi
  exit 1
fi
echo "mutation gate: mutago failed to run (exit $status)" >&2
exit 1
