#!/usr/bin/env bash
# Release automation. Computes the next semantic version from Conventional
# Commits (svu), regenerates CHANGELOG.md (git-chglog), then commits and tags.
# Forge-agnostic and local: it never pushes. Pass --dry-run to preview the
# version and changelog diff without touching the working tree.
#
# Tools: install with `make tools` (svu, git-chglog).
set -euo pipefail

DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
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

if [[ "$DRY_RUN" -eq 0 ]]; then
  branch="$(git symbolic-ref --short HEAD)"
  if [[ "$branch" != "main" ]]; then
    echo "release: must run on main, on '$branch'" >&2
    exit 1
  fi
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "release: working tree not clean; commit or stash first" >&2
    exit 1
  fi
fi

current="$(svu current)"
next="$(svu next)"
if [[ "$next" == "$current" ]]; then
  echo "release: no releasable commits since $current" >&2
  exit 1
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  git-chglog --next-tag "$next" -o "$tmp"
  echo "release: $current -> $next (dry run)"
  echo "--- CHANGELOG.md diff ---"
  base=/dev/null
  [[ -f CHANGELOG.md ]] && base=CHANGELOG.md
  diff -u "$base" "$tmp" || true
  exit 0
fi

git-chglog --next-tag "$next" -o CHANGELOG.md
git add CHANGELOG.md
git commit -m "chore(release): $next"
git tag -a "$next" -m "$next"

echo "release: committed and tagged $next"
echo "push it with: git push --follow-tags"
