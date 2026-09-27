#!/usr/bin/env bash
# Reads and writes the `badges` branch that holds the state of the weekly mutation scan.
#
#   scripts/mutation-state.sh fetch <dir>     # copy the tree of the branch into <dir>
#   scripts/mutation-state.sh publish <dir>   # force-push <dir> as a one-commit branch
#
# The branch holds mutation.json (the shields.io endpoint of the README badge) and
# state/<slug>.json (the last result of each package, see scripts/mutation-verdict.sh).
#
# The remote URI comes from the GitHub Actions environment (GITHUB_SERVER_URL,
# GITHUB_REPOSITORY) with the token in GITHUB_TOKEN, because the repository can be
# private and the checkout keeps no credential. IQ_MUTATION_STATE_REMOTE replaces that
# URI, for a local test against a bare repository. The script never prints the URI.
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
  remote="${GITHUB_SERVER_URL%%://*}://x-access-token:${GITHUB_TOKEN}@${GITHUB_SERVER_URL#*://}/${GITHUB_REPOSITORY}.git"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case "$action" in
fetch)
  mkdir -p "$dir"
  set +e
  git ls-remote --exit-code "$remote" "refs/heads/$branch" >/dev/null 2>&1
  status=$?
  set -e
  if [[ "$status" -eq 2 ]]; then
    echo "mutation-state: the $branch branch does not exist yet; the state is empty"
    exit 0
  fi
  if [[ "$status" -ne 0 ]]; then
    echo "mutation-state: cannot read the remote (git ls-remote exit $status)" >&2
    exit 1
  fi
  git init -q --bare "$work/repo"
  git -C "$work/repo" fetch -q --depth=1 "$remote" "refs/heads/$branch" 2>/dev/null ||
    {
      echo "mutation-state: cannot fetch the $branch branch" >&2
      exit 1
    }
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
  git -C "$work/tree" push -q --force "$remote" "$branch" 2>/dev/null ||
    {
      echo "mutation-state: cannot push the $branch branch" >&2
      exit 1
    }
  echo "mutation-state: published $(git -C "$work/tree" ls-files | wc -l) file(s) to the $branch branch"
  ;;
*)
  echo "$usage" >&2
  exit 1
  ;;
esac
