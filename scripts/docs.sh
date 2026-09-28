#!/usr/bin/env bash
# Build or serve the Zensical documentation site under docs/. Sources are plain
# hand-maintained Markdown under docs/docs/ — nothing regenerates them from the
# README, and the developer command catalogue is DEVELOPMENT.md; the site is
# built with uv against the committed docs/uv.lock, so it is reproducible and needs
# no Node. Dispatches one argument: `build` emits docs/site/, `serve` runs the dev
# server on 0.0.0.0:8000 (all interfaces, so the preview is reachable over the LAN).
#
# Both modes first write the gitignored docs/docs/llms-full.txt, every page
# concatenated in navigation order, which Zensical copies into the site verbatim
# as any other non-Markdown file. It is published at
# https://zsltg.github.io/iq/llms-full.txt so an agent reads the whole manual in
# one fetch instead of crawling the pages.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

# nav_entries prints one "<title><tab><page>" line per docs/zensical.toml nav
# entry, in nav order.
nav_entries() {
  awk '/^nav = \[/ { inside = 1; next } inside && /^\]/ { exit } inside' docs/zensical.toml |
    sed -n 's/^[[:space:]]*{[[:space:]]*"\(.*\)"[[:space:]]*=[[:space:]]*"\(.*\)"[[:space:]]*}.*$/\1\t\2/p'
}

# strip_front_matter prints a page without its leading YAML front matter block.
strip_front_matter() {
  awk 'NR == 1 && $0 == "---" { front = 1; next } front && $0 == "---" { front = 0; next } !front' "$1"
}

# write_llms_full concatenates the nav pages into docs/docs/llms-full.txt.
write_llms_full() {
  local out="docs/docs/llms-full.txt" title page count=0

  {
    echo "# iq"
    echo
    echo "jq for NoSQL databases. Every page of https://zsltg.github.io/iq/ concatenated in"
    echo "navigation order, from the Markdown sources under docs/docs/."
    echo
  } >"$out" || return 1

  while IFS=$'\t' read -r title page; do
    [ -n "${page:-}" ] && [ -f "docs/docs/$page" ] || continue
    {
      echo "# $title"
      echo "docs/docs/$page"
      echo
      strip_front_matter "docs/docs/$page" | sed '/./,$!d'
      echo
    } >>"$out" || return 1
    count=$((count + 1))
  done < <(nav_entries)

  if [ "$count" -eq 0 ]; then
    echo "docs: no nav pages found in docs/zensical.toml, llms-full.txt would be empty." >&2
    return 1
  fi
  echo "docs: wrote $out ($count pages)."
}

if ! command -v uv >/dev/null 2>&1; then
  echo "docs: uv is required to build the documentation site (install: https://docs.astral.sh/uv/getting-started/installation/)." >&2
  exit 1
fi

cmd="${1:-}"
case "$cmd" in
  build)
    write_llms_full || exit 1
    cd docs && exec uv run --locked zensical build --clean
    ;;
  serve)
    write_llms_full || exit 1
    cd docs && exec uv run --locked zensical serve -a 0.0.0.0:8000
    ;;
  *)
    echo "usage: scripts/docs.sh {build|serve}" >&2
    exit 2
    ;;
esac
