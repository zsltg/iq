#!/usr/bin/env bash
# Test the capability wrapper with simulated Go and Capslock commands.
# Run with bash scripts/test/capabilities.sh. No network or containers are needed.
set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo 'capability tests: jq not found; install it and retry' >&2; exit 1; }

root=$(git rev-parse --show-toplevel)
wrapper="$root/scripts/capabilities.sh"
# The simulated Go accepts only the pinned capslock, so a pin bump needs no test edit.
caps_version=$(sed -n 's/^CAPSLOCK_VERSION=//p' "$wrapper")
[[ -n "$caps_version" ]] || { echo 'capability tests: no CAPSLOCK_VERSION in the wrapper' >&2; exit 1; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/repo"
git -C "$work/repo" init -q

cat >"$work/bin/go" <<'GO'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  'env GOOS')
    [[ "$TEST_GOOS" != error ]] || exit 1
    printf '%s\n' "$TEST_GOOS"
    ;;
  "install github.com/google/capslock/cmd/capslock@$TEST_CAPS_VERSION")
    touch "$TEST_INSTALL"
    cp "$TEST_CAPSLOCK" "$GOBIN/capslock"
    ;;
  *) echo "unexpected Go command: $*" >&2; exit 1 ;;
esac
GO
cat >"$work/bin/capslock" <<'CAPSLOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >"$TEST_ARGS"
if [[ " $* " == *' -output=json '* ]]; then
  printf '%s\n' '{"capabilityInfo":[{"packageDir":"fixture","capabilityName":"FILES"}]}'
fi
exit "$TEST_CAPS_STATUS"
CAPSLOCK
chmod +x "$work/bin/go" "$work/bin/capslock"
export PATH="$work/bin:$PATH"
export TEST_CAPSLOCK="$work/bin/capslock"
export TEST_INSTALL="$work/install"
export TEST_ARGS="$work/args"
export TEST_CAPS_VERSION="$caps_version"
cd "$work/repo"

failures=0
while IFS='|' read -r name target override update caps_status want_status message installed; do
  printf '%s\n' '{"capabilityInfo":[]}' >capslock-baseline.json
  cp capslock-baseline.json "$work/before.json"
  rm -f "$TEST_INSTALL" "$TEST_ARGS"
  status=0
  env -u IQ_CAPS_GOOS TEST_GOOS="$target" TEST_CAPS_STATUS="$caps_status" \
    IQ_CAPS_UPDATE_BASELINE="$update" IQ_CAPS_FORCE=1 \
    ${override:+IQ_CAPS_GOOS=$override} \
    bash "$wrapper" >"$work/output" 2>&1 || status=$?
  passed=1
  [[ "$status" == "$want_status" ]] || passed=0
  grep -Fq -- "$message" "$work/output" || passed=0
  if [[ "$installed" == no ]]; then
    [[ ! -e "$TEST_INSTALL" ]] || passed=0
    cmp -s "$work/before.json" capslock-baseline.json || passed=0
  else
    [[ -f "$TEST_ARGS" ]] || passed=0
    if [[ -n "$override" ]]; then
      grep -Fxq -- "-goos=$override" "$TEST_ARGS" || passed=0
    elif grep -q -- '-goos=' "$TEST_ARGS"; then
      passed=0
    fi
    if [[ "$update" == 1 && "$want_status" == 0 ]]; then
      jq -e '.capabilityInfo == [{"packageDir":"fixture","capabilityName":"FILES"}]' capslock-baseline.json >/dev/null || passed=0
    else
      cmp -s "$work/before.json" capslock-baseline.json || passed=0
    fi
  fi
  if [[ "$passed" == 1 ]]; then
    echo "PASS $name"
  else
    echo "FAIL $name: exit $status, expected $want_status"
    cat "$work/output"
    failures=$((failures + 1))
  fi
done <<'CASES'
implicit Darwin update|darwin||1|0|1|Set IQ_CAPS_GOOS=linux|no
implicit Windows update|windows||1|0|1|Set IQ_CAPS_GOOS=linux|no
implicit Linux update|linux||1|0|0|baseline regenerated|yes
explicit Linux update from Darwin|darwin|linux|1|0|0|baseline regenerated|yes
explicit Darwin update from Linux|linux|darwin|1|0|1|Set IQ_CAPS_GOOS=linux|no
explicit Windows update from Linux|linux|windows|1|0|1|Set IQ_CAPS_GOOS=linux|no
invalid explicit target|linux|linuz|1|0|1|IQ_CAPS_GOOS must be|no
failed target lookup|error||1|0|1|could not read the Go target|no
empty target lookup|||1|0|1|Set IQ_CAPS_GOOS=linux|no
matching baseline|linux|||0|0|passed, no capability drift|yes
capability drift|linux|||1|1|capability drift against|yes
run error|linux|||2|1|no capability verdict was reached|yes
implicit Darwin comparison|darwin|||1|1|capability drift against|yes
explicit Darwin comparison|linux|darwin||1|1|this is not a gate verdict|yes
CASES

# The trigger: without IQ_CAPS_FORCE, the wrapper runs only when a gate input differs
# from the base commit. The inputs are go.mod, go.sum, the wrapper and the baseline.
mkdir -p "$work/trigger/scripts"
cd "$work/trigger"
git init -q
printf '%s\n' 'module fixture' >go.mod
printf '%s\n' 'fixture v1.0.0 h1:x' >go.sum
printf '%s\n' 'CAPSLOCK_VERSION=old' >scripts/capabilities.sh
printf '%s\n' '{"capabilityInfo":[]}' >capslock-baseline.json
printf '%s\n' 'fixture' >README.md
git add -A
git -c user.name=test -c user.email=test@example.invalid commit -q -m base
base=$(git rev-parse HEAD)
while IFS='|' read -r name file message installed; do
  git checkout -q -f "$base"
  if [[ -n "$file" ]]; then
    printf '%s\n' 'changed' >>"$file"
  fi
  rm -f "$TEST_INSTALL" "$TEST_ARGS"
  status=0
  env -u IQ_CAPS_FORCE -u IQ_CAPS_UPDATE_BASELINE -u IQ_CAPS_GOOS \
    TEST_GOOS=linux TEST_CAPS_STATUS=0 IQ_CAPS_BASE="$base" \
    bash "$wrapper" >"$work/output" 2>&1 || status=$?
  passed=1
  [[ "$status" == 0 ]] || passed=0
  grep -Fq -- "$message" "$work/output" || passed=0
  if [[ "$installed" == yes ]]; then
    [[ -e "$TEST_INSTALL" ]] || passed=0
  else
    [[ ! -e "$TEST_INSTALL" ]] || passed=0
  fi
  if [[ "$passed" == 1 ]]; then
    echo "PASS trigger: $name"
  else
    echo "FAIL trigger: $name: exit $status"
    cat "$work/output"
    failures=$((failures + 1))
  fi
done <<'CASES'
no change||no gate input changed|no
unrelated file|README.md|no gate input changed|no
go.mod change|go.mod|gate inputs changed against|yes
go.sum change|go.sum|gate inputs changed against|yes
capslock pin change|scripts/capabilities.sh|gate inputs changed against|yes
baseline change|capslock-baseline.json|gate inputs changed against|yes
CASES

if [[ "$failures" != 0 ]]; then
  echo "capability tests: $failures failure(s)" >&2
  exit 1
fi
echo 'capability tests: passed'
