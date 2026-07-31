#!/usr/bin/env bash
# Capability gate. Runs github.com/google/capslock over the module and lets it
# exit-code-enforce against a committed baseline: -output=compare exits 0 when the
# capability set matches, 1 on drift, and 2 on a run error, so this wrapper only decides
# whether a run is warranted, shapes the scope, and forwards the verdict. No output
# parsing. It answers the one question the rest of the sweep does not: govulncheck and
# osv-scanner ask whether a dependency is known-vulnerable and gitleaks whether we leaked
# a secret, while this asks what a dependency can actually *do* — a bump that gains a new
# capability is invisible in a 295-module go.sum diff.
#
# Version-hermetic invocation. capslock is pinned by CAPSLOCK_VERSION (below) and
# provisioned per run into a throwaway GOBIN via `go install <pinned>@version`, then
# executed directly, exactly as scripts/mutation-gate.sh provisions mutago: the gate needs
# no capslock on PATH (`make tools-dev` installs it only for ad-hoc use) and never touches
# this module's go.mod. Direct execution is deliberate over `go run`, which collapses any
# non-zero program exit to 1 and would make a drift verdict (exit 1) indistinguishable
# from a run error (exit 2); the installed binary preserves capslock's exact exit codes
# and streams its output live.
#
# When it runs. The analysis costs ~7.3 GB peak RSS and ~39 s, so the gate is conditional
# rather than unconditional: it runs only when go.mod or go.sum differ from the merge-base
# with the base ref (IQ_CAPS_BASE, default origin/main), and otherwise prints the reason
# and exits 0. A docs-only or code-only branch pays nothing, and `make ci` never runs a
# 7.3 GB analysis beside the container stack it tells you to start. The trigger is also
# aligned with the tool's actual detection strength (limit (a) below): a new
# (package, capability) pair is the shape a dependency bump produces, while a first-party
# exec or network addition is already covered by gosec inside `make check`, plus review.
# The base is resolved to the merge-base *commit* with HEAD, so a local base ref that has
# drifted from the remote still yields the true branch point, and it works from a linked
# git worktree. IQ_CAPS_FORCE=1 runs the gate regardless; IQ_CAPS_BASE= (empty) does too.
#
# Scope is plain ./... : capslock does not load test files, so ./e2e reports zero modules
# and zero capabilities, and testcontainers — imported only from _test.go files — is never
# in the graph. No exclusion is needed and adding one would be a no-op.
#
# Two detection limits, both accepted deliberately.
# (a) Package granularity compares the (package, capability) *set*, so a package that
#     already holds a capability can gain arbitrarily many new call paths into it
#     invisibly — and this tree is already saturated: drivers/redis alone carries
#     ARBITRARY_EXECUTION. Function granularity would catch that at ~37x the rows
#     (~7,000 tree-wide) and churn on every function rename; not viable on a codebase
#     under active development.
# (b) compare is bidirectional, so a benign dependency bump that *drops* a capability
#     fails the gate exactly like one that gains a capability. That is resolved by
#     regenerating the baseline (below), never by an exemption.
#
# The baseline is linux-only. Capability sets are GOOS-specific — internal/secret
# (go-keyring) reports EXEC/FILES/ENV/NETWORK/... on linux, EXEC/FILES/UNANALYZED on
# darwin, and ARBITRARY_EXECUTION/SYSTEM_CALLS/... on windows — and iq ships all three.
# IQ_CAPS_GOOS=linux|darwin|windows re-runs the analysis for another target as a review
# aid at driver admission: it is *expected* to report differences against the linux
# baseline, so treat its output as evidence to read, not as a verdict. A bad value is a
# hard stop, never a silent fallback to the host GOOS. Regenerating the committed
# baseline from a non-linux run is refused for the same reason.
#
# Baseline rows are analysis-scope-dependent, not purely a property of the package:
# adding a package or a dependency that introduces new interface implementations can flip
# rows of unrelated packages (internal/render gains NETWORK in the whole-tree run because
# json.Encoder takes an io.Writer, resolved against every io.Writer in the analyzed set).
# A Go toolchain bump churns the baseline tree-wide for the same reason — stdlib-derived
# paths are part of the rows — and a pinned capslock's embedded x/tools will eventually
# refuse a newer toolchain, so a toolchain bump may require bumping CAPSLOCK_VERSION here
# alongside the regeneration.
#
# IQ_CAPS_UPDATE_BASELINE=1 regenerates capslock-baseline.json instead of gating
# (-granularity=package -omit_paths -output=json, trimmed to the capabilityInfo array
# with jq — compare reads only that array, so version and module churn stays out of the
# committed file) and prints every row it added or removed. Review that list, then justify
# each new EXEC / ARBITRARY_EXECUTION / MODIFY_SYSTEM_STATE / SYSTEM_CALLS row with a line
# in the committed capslock-baseline.notes.md before committing; the bulk rows stay
# unannotated by design. Consult that file before re-litigating a known false positive.
set -uo pipefail

# Pinned capslock version — keep in step with AGENTS.md and `make tools-dev` (Makefile).
CAPSLOCK_VERSION=v0.3.2
capslock_pkg=github.com/google/capslock/cmd/capslock

cd "$(git rev-parse --show-toplevel)" || exit 1

baseline_file=capslock-baseline.json
notes_file=capslock-baseline.notes.md

fail() { echo "capabilities: $*" >&2; exit 1; }
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
require() { command -v "$1" >/dev/null 2>&1 || fail "$1 not found; install it and retry"; }

update="${IQ_CAPS_UPDATE_BASELINE-}"
force="${IQ_CAPS_FORCE-}"

# Target GOOS. Validated before any work against a strict whitelist: a typo must stop the
# run, never fall back to the host GOOS and report differences that are an artifact of the
# fallback. Empty means the host target, which is what the committed baseline records.
goos="${IQ_CAPS_GOOS-}"
goos_flag=()
if [[ -n "$goos" ]]; then
  case "$goos" in
    linux | darwin | windows) goos_flag=(-goos="$goos") ;;
    *) fail "IQ_CAPS_GOOS must be linux, darwin, or windows, got '$goos'" ;;
  esac
fi

# The committed baseline is linux-only, so a non-linux analysis cannot be allowed to
# overwrite it: that would silently retarget the gate for every later run.
if [[ "$update" == "1" && -n "$goos" && "$goos" != "linux" ]]; then
  fail "refusing to regenerate $baseline_file from a ${goos} analysis; the committed baseline is linux-only (drop IQ_CAPS_GOOS)"
fi

# Trigger. The gate is warranted by a dependency-graph change, so unless it is forced (or
# this is a baseline regeneration, which is always deliberate) skip a run whose inputs
# cannot have moved. merge-base is robust to a base ref that has advanced on the remote;
# fall back to the ref itself, then to running unconditionally — never to a silent skip,
# since a skip is the permissive outcome.
base="${IQ_CAPS_BASE-origin/main}"
if [[ "$update" != "1" && "$force" != "1" ]]; then
  if [[ -z "$base" ]]; then
    echo "capabilities: IQ_CAPS_BASE is empty; running unconditionally"
  elif ref=$(git merge-base "$base" HEAD 2>/dev/null) || ref=$(git rev-parse --verify --quiet "$base"); then
    changed=$(git diff --name-only "$ref" -- go.mod go.sum)
    if [[ -z "$changed" ]]; then
      echo "capabilities: dependency graph unchanged against ${base} (no go.mod or go.sum diff); skipping the ~7.3 GB analysis. IQ_CAPS_FORCE=1 runs it anyway."
      exit 0
    fi
    echo "capabilities: dependency graph changed against ${base}: ${changed//$'\n'/, }"
  else
    echo "capabilities: base ref '$base' not found; running unconditionally" >&2
  fi
fi

# Provision the pinned capslock into a throwaway GOBIN and run that binary directly, so
# the gate needs no capslock on PATH and preserves its exact exit codes (see header).
# Cleaned on any exit. `go install pkg@version` is module-independent: it neither reads
# nor writes this module's go.mod.
capslock_bindir=$(mktemp -d) || fail "could not create temp dir"
cleanup() {
  rm -rf "$capslock_bindir"
  return 0
}
trap cleanup EXIT
if ! GOBIN="$capslock_bindir" go install "${capslock_pkg}@${CAPSLOCK_VERSION}"; then
  fail "could not install capslock ${CAPSLOCK_VERSION}"
fi
capslock="$capslock_bindir/capslock"

# Baseline regeneration. Not a gate: it records the current capability set, prints what
# moved, and exits 0. jq trims the report to the capabilityInfo array, the only part
# -output=compare reads, so module versions and analysis metadata stay out of the diff.
if [[ "$update" == "1" ]]; then
  require jq
  step "capslock ${CAPSLOCK_VERSION} (regenerating ${baseline_file}; ~7.3 GB peak RSS, ~39 s)"
  # Scratch files live in the same private mktemp dir as the binary, so nothing lands on a
  # predictable /tmp path and the existing trap cleans all of it.
  raw_output="$capslock_bindir/report.json"
  trimmed="$capslock_bindir/baseline.json"
  before="$capslock_bindir/before.txt"
  after="$capslock_bindir/after.txt"
  if ! "$capslock" -packages=./... -granularity=package -omit_paths -output=json "${goos_flag[@]}" >"$raw_output"; then
    fail "capslock failed to analyse the module; $baseline_file is unchanged"
  fi
  jq '{capabilityInfo}' "$raw_output" >"$trimmed" || fail "could not trim the capslock report; $baseline_file is unchanged"
  # A report with no rows means the analysis silently found nothing — writing it would
  # disarm the gate, so treat it as a run failure.
  rows=$(jq -r '.capabilityInfo | length' "$trimmed")
  [[ "$rows" =~ ^[1-9][0-9]*$ ]] || fail "capslock reported no capabilities at all; $baseline_file is unchanged"

  key() { jq -r '.capabilityInfo[] | "\(.packageDir) \(.capabilityName)"' "$1" | sort -u; }
  if [[ -f "$baseline_file" ]]; then key "$baseline_file" >"$before"; else : >"$before"; fi
  key "$trimmed" >"$after"

  cp "$trimmed" "$baseline_file" || fail "could not write $baseline_file"

  added=$(comm -13 "$before" "$after")
  removed=$(comm -23 "$before" "$after")
  if [[ -z "$added" && -z "$removed" ]]; then
    echo "capabilities: baseline unchanged (${rows} rows)"
  else
    echo "capabilities: baseline regenerated (${rows} rows)"
    [[ -n "$added" ]] && { echo "  gained:"; sed 's/^/    + /' <<<"$added"; }
    [[ -n "$removed" ]] && { echo "  lost:"; sed 's/^/    - /' <<<"$removed"; }
    echo "capabilities: justify every new EXEC / ARBITRARY_EXECUTION / MODIFY_SYSTEM_STATE / SYSTEM_CALLS row in ${notes_file} before committing"
  fi
  exit 0
fi

[[ -f "$baseline_file" ]] || fail "$baseline_file not found; regenerate it with IQ_CAPS_UPDATE_BASELINE=1"

step "capslock ${CAPSLOCK_VERSION} (capability drift vs ${baseline_file}; ~7.3 GB peak RSS, ~39 s)"
"$capslock" -packages=./... -granularity=package -output=compare "${goos_flag[@]}" "$baseline_file"
status=$?

# capslock exits 0 (the capability set matches the baseline), 1 (drift, in either
# direction) or 2 (a run error). Keep the three apart: collapsing 2 into 1 would report a
# build failure as a capability regression and send the reader to the baseline flow.
if [[ "$status" -eq 0 ]]; then
  echo "capabilities: passed, no capability drift against $baseline_file"
  exit 0
fi
if [[ "$status" -eq 1 ]]; then
  if [[ -n "$goos" && "$goos" != "linux" ]]; then
    echo "capabilities: ${goos} differs from the linux baseline (expected — the committed baseline is linux-only). Read the paths above as review evidence; this is not a gate verdict." >&2
    exit 1
  fi
  echo "capabilities FAILED: capability drift against $baseline_file (see the call paths above). A gained capability needs a justification line in ${notes_file}; a benign loss from a dependency bump needs none. Either way, record the new set with IQ_CAPS_UPDATE_BASELINE=1 once you have read the paths." >&2
  exit 1
fi
echo "capabilities: capslock failed to run (exit $status); no capability verdict was reached" >&2
exit 1
