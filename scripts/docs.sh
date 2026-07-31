#!/usr/bin/env bash
# Build or serve the Zensical documentation site under docs/. Sources are plain
# hand-maintained Markdown under docs/docs/ — nothing regenerates them from the
# README, and the developer command catalogue lives across these pages; the site is
# built with uv against the committed docs/uv.lock, so it is reproducible and needs
# no Node. Dispatches one argument: `build` emits docs/site/, `serve` runs the dev
# server on 0.0.0.0:8000 (all interfaces, so the preview is reachable over the LAN).
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

if ! command -v uv >/dev/null 2>&1; then
  echo "docs: uv is required to build the documentation site (install: https://docs.astral.sh/uv/getting-started/installation/)." >&2
  exit 1
fi

cmd="${1:-}"
case "$cmd" in
  build)
    cd docs && exec uv run --locked zensical build --clean
    ;;
  serve)
    cd docs && exec uv run --locked zensical serve -a 0.0.0.0:8000
    ;;
  *)
    echo "usage: scripts/docs.sh {build|serve}" >&2
    exit 2
    ;;
esac
