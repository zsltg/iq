#!/usr/bin/env bash
# Reads and writes the `badges` branch that holds the state of the weekly mutation scan.
#
#   scripts/mutation-state.sh fetch <dir>     # copy the tree of the branch into <dir>
#   scripts/mutation-state.sh publish <dir>   # force-push <dir> as a one-commit branch
#
# The branch holds mutation.json (the shields.io endpoint of the README badge) and
# state/<slug>.json (the last result of each package, see scripts/mutation-verdict.sh).
#
# Credentials. The remote URI comes from the GitHub Actions environment
# (GITHUB_SERVER_URL, GITHUB_REPOSITORY) and holds no credential. The token in GITHUB_TOKEN
# goes to git as an HTTP Authorization header through the GIT_CONFIG_COUNT environment
# variables, the same method as actions/checkout. So the token is not in the argument list
# of a process, not in a URI, and not in a git config file. The script never prints the
# token. IQ_MUTATION_STATE_REMOTE replaces the remote URI (with no token), for a local test
# against a bare repository.
#
# Retry. Each network step (read the remote, fetch, push) has three attempts with a
# backoff of 10 s and 20 s plus up to 5 s of jitter. The steps are idempotent.
#
# fetch: when the branch does not exist yet (`git ls-remote --exit-code` status 2), <dir>
# stays empty and the script exits 0. Any other error stops the script, so a network
# error never looks like an empty state (which would scan every package again).
set -euo pipefail

usage="usage: mutation-state.sh fetch|publish <dir>"
action="${1:?$usage}"
dir="${2:?$usage}"
branch=badges

if [[ -n "${IQ_MUTATION_STATE_REMOTE-}" ]]; then
  remote="$IQ_MUTATION_STATE_REMOTE"
else
  : "${GITHUB_SERVER_URL:?mutation-state: GITHUB_SERVER_URL is not set}"
  : "${GITHUB_REPOSITORY:?mutation-state: GITHUB_REPOSITORY is not set}"
  : "${GITHUB_TOKEN:?mutation-state: GITHUB_TOKEN is not set}"
  remote="${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}.git"
  auth=$(printf 'x-access-token:%s' "$GITHUB_TOKEN" | base64 -w0)
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="http.${GITHUB_SERVER_URL}/.extraheader"
  export GIT_CONFIG_VALUE_0="AUTHORIZATION: basic ${auth}"
  unset auth
fi
export GIT_TERMINAL_PROMPT=0

# retry <description> <command...>: run the command up to three times. The exit status of
# the last attempt is returned.
retry() {
  local what="$1" attempt status
  shift
  for attempt in 1 2 3; do
    status=0
    "$@" || status=$?
    [[ "$status" -eq 0 ]] && return 0
    [[ "$attempt" -eq 3 ]] && break
    echo "mutation-state: $what failed (attempt $attempt, exit $status); trying again" >&2
    sleep $((attempt * 10 + RANDOM % 5))
  done
  return "$status"
}

# ls_remote: exit 0 when the branch exists, 2 when it does not, another status on error.
# Status 2 is a definite answer, so it is not tried again.
ls_remote() {
  local attempt status
  for attempt in 1 2 3; do
    status=0
    git ls-remote --exit-code "$remote" "refs/heads/$branch" >/dev/null 2>&1 || status=$?
    [[ "$status" -eq 0 || "$status" -eq 2 ]] && return "$status"
    [[ "$attempt" -eq 3 ]] && break
    echo "mutation-state: reading the remote failed (attempt $attempt, exit $status); trying again" >&2
    sleep $((attempt * 10 + RANDOM % 5))
  done
  return "$status"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case "$action" in
fetch)
  mkdir -p "$dir"
  status=0
  ls_remote || status=$?
  if [[ "$status" -eq 2 ]]; then
    echo "mutation-state: the $branch branch does not exist yet; the state is empty"
    exit 0
  fi
  if [[ "$status" -ne 0 ]]; then
    echo "mutation-state: cannot read the remote (git ls-remote exit $status)" >&2
    exit 1
  fi
  git init -q --bare "$work/repo"
  if ! retry "fetch" git -C "$work/repo" fetch -q --depth=1 "$remote" "refs/heads/$branch" 2>/dev/null; then
    echo "mutation-state: cannot fetch the $branch branch" >&2
    exit 1
  fi
  git -C "$work/repo" archive FETCH_HEAD | tar -x -C "$dir"
  echo "mutation-state: fetched $(find "$dir" -name '*.json' | wc -l) JSON file(s) from the $branch branch"
  ;;
publish)
  [[ -d "$dir" ]] || {
    echo "mutation-state: '$dir' is not a directory" >&2
    exit 1
  }
  mkdir -p "$work/tree"
  cp -R "$dir/." "$work/tree/"
  git -C "$work/tree" init -q -b "$branch"
  git -C "$work/tree" config user.name ci
  git -C "$work/tree" config user.email ci@iq
  git -C "$work/tree" add -A
  git -C "$work/tree" commit -qm "ci: mutation state from ${GITHUB_SHA:-$(git rev-parse HEAD)}"
  if ! retry "push" git -C "$work/tree" push -q --force "$remote" "$branch" 2>/dev/null; then
    echo "mutation-state: cannot push the $branch branch" >&2
    exit 1
  fi
  echo "mutation-state: published $(git -C "$work/tree" ls-files | wc -l) file(s) to the $branch branch"
  ;;
*)
  echo "$usage" >&2
  exit 1
  ;;
esac
