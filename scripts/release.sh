#!/usr/bin/env bash
# Release automation. Computes the next semantic version from Conventional
# Commits (svu) and regenerates CHANGELOG.md (git-chglog). main accepts only
# squash-merged pull requests, so a release has two steps. The first step
# commits the changelog on a release branch. After the pull request of that
# branch is squash-merged, the second step tags the merged commit on main.
# Local: it never pushes.
#
#   (no args)          on a clean chore/release branch, started from the latest
#                      main in its own worktree: regenerate CHANGELOG.md and
#                      commit it
#   --tag              on clean main, after the squash merge: tag HEAD when HEAD
#                      is the release commit of the next version
#   --dry-run          preview the next version and CHANGELOG.md diff; no changes
#   --changelog-only   regenerate CHANGELOG.md in place; no commit, tag, or checks
#
# Tools: install with `make tools` (svu, git-chglog).
set -euo pipefail

MODE=release
for arg in "$@"; do
  case "$arg" in
    --dry-run) MODE=dry-run ;;
    --changelog-only) MODE=changelog-only ;;
    --tag) MODE=tag ;;
    *) echo "release: unknown argument: $arg" >&2; exit 2 ;;
  esac
done

for tool in svu git-chglog; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "release: $tool not found on PATH; run 'make tools'" >&2
    exit 1
  fi
done

cd "$(git rev-parse --show-toplevel)"

current="$(svu current)"
next="$(svu next)"

# regen writes the changelog to $1. It generates into a temp file and moves it
# into place only on success, so a git-chglog failure never truncates the target.
# It passes --next-tag only for a genuinely new version: a pending bump whose tag
# does not yet exist. git-chglog rejects a bare run when no tag exists yet, and
# rejects a --next-tag that duplicates an existing tag (which happens when the
# checkout sits behind a published tag, so svu's "next" equals that tag); this
# picks the form that works and degrades to a plain run instead of erroring.
regen() {
  local out="$1" tmp rc=0
  tmp="$(mktemp)"
  if [[ "$next" != "$current" ]] && ! git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    git-chglog --next-tag "$next" -o "$tmp" || rc=$?
  else
    git-chglog -o "$tmp" || rc=$?
  fi
  if [[ "$rc" -ne 0 ]]; then
    rm -f "$tmp"
    echo "release: git-chglog failed (exit $rc)" >&2
    exit 1
  fi
  mv "$tmp" "$out"
}

if [[ "$MODE" == "changelog-only" ]]; then
  regen CHANGELOG.md
  echo "release: regenerated CHANGELOG.md (current $current, next $next)"
  exit 0
fi

if [[ "$next" == "$current" ]]; then
  echo "release: no releasable commits since $current" >&2
  exit 1
fi

if [[ "$MODE" == "dry-run" ]]; then
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  regen "$tmp"
  echo "release: $current -> $next (dry run)"
  echo "--- CHANGELOG.md diff ---"
  base=/dev/null
  [[ -f CHANGELOG.md ]] && base=CHANGELOG.md
  diff -u "$base" "$tmp" || true
  exit 0
fi

branch="$(git symbolic-ref --short HEAD)"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "release: tracked changes present; commit or stash first" >&2
  exit 1
fi

if [[ "$MODE" == "tag" ]]; then
  if [[ "$branch" != "main" ]]; then
    echo "release: --tag must run on main, on '$branch'" >&2
    exit 1
  fi
  # The squash merge gives the release commit the pull request title, with the
  # pull request number appended. Tag only that commit, and only for the
  # version that svu computes, so a tag never lands on another commit.
  subject="$(git log -1 --format=%s)"
  if [[ ! "$subject" =~ ^chore\(release\):\ (v[0-9]+\.[0-9]+\.[0-9]+)(\ \(#[0-9]+\))?$ ]]; then
    echo "release: HEAD is not a release commit: $subject" >&2
    echo "release: pull the squash merge of the release pull request first" >&2
    exit 1
  fi
  if [[ "${BASH_REMATCH[1]}" != "$next" ]]; then
    echo "release: HEAD releases ${BASH_REMATCH[1]}, but the next version is $next" >&2
    exit 1
  fi
  if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "release: tag $next exists already" >&2
    exit 1
  fi
  git tag -a "$next" -m "$next"
  echo "release: tagged $next on $(git rev-parse --short HEAD)"
  echo "push only this tag: git push github $next && git push origin $next"
  exit 0
fi

# The release commit goes through a pull request like every other change, so it
# is made on a release branch, never on main. Start the branch from the latest
# main, in its own worktree.
if [[ "$branch" != chore/release* ]]; then
  echo "release: run on a chore/release branch started from the latest main, on '$branch'" >&2
  echo "release: git worktree add -b chore/release-$next .claude/worktrees/release github/main" >&2
  exit 1
fi
regen CHANGELOG.md
git add CHANGELOG.md
git commit -m "chore(release): $next"

echo "release: committed CHANGELOG.md for $next on $branch"
echo "next: git push github $branch, then open a pull request titled 'chore(release): $next'"
echo "after the squash merge, in the main checkout: git pull --ff-only github main && make release-tag"
