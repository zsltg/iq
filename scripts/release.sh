#!/usr/bin/env bash
# Release automation. Computes the next semantic version from Conventional
# Commits (svu), regenerates CHANGELOG.md (git-chglog), then commits and tags.
# Forge-agnostic and local: it never pushes.
#
#   (no args)          bump, regenerate CHANGELOG.md, commit, and tag on clean main
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
if [[ "$branch" != "main" ]]; then
  echo "release: must run on main, on '$branch'" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "release: tracked changes present; commit or stash first" >&2
  exit 1
fi

regen CHANGELOG.md
git add CHANGELOG.md
git commit -m "chore(release): $next"
git tag -a "$next" -m "$next"

echo "release: committed and tagged $next"
echo "push it with: git push --follow-tags"
