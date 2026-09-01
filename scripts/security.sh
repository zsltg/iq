#!/usr/bin/env bash
# Security sweep: dependency audit, OSV scan, secret scan, workflow audit, and
# SBOM generation. Touches the network (vulnerability databases). Install the
# tools with `make tools-dev`. A real vulnerability, a leaked secret, or a zizmor
# workflow finding fails the gate; the SBOMs are written to dist/ (gitignored) and
# never fail it. zizmor runs the offline audits alone. If GH_TOKEN or GITHUB_TOKEN
# is set, zizmor adds the online audits. gosec (Go SAST) runs separately via
# golangci-lint in scripts/check.sh, not here.
set -uo pipefail

# Keep this pin in sync with ZIZMOR_VERSION in .github/workflows/ci.yml.
ZIZMOR_VERSION=1.30.0

cd "$(git rev-parse --show-toplevel)" || exit 1

missing=0
require() { command -v "$1" >/dev/null 2>&1 || { echo "security: $1 not found; run 'make tools-dev'" >&2; missing=1; }; }
require govulncheck
require osv-scanner
require gitleaks
require syft
command -v uvx >/dev/null 2>&1 || { echo "security: uvx not found; install uv" >&2; missing=1; }
[[ "$missing" -eq 1 ]] && exit 1

fail=0
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

step "govulncheck (Go vulnerability audit, call-graph aware)"
govulncheck ./... || { echo "security: govulncheck reported vulnerabilities" >&2; fail=1; }

step "osv-scanner (OSV dependency scan)"
osv-scanner scan source -r . || { echo "security: osv-scanner reported vulnerabilities" >&2; fail=1; }

step "gitleaks (secrets: working tree + git history)"
gitleaks dir . --no-banner || { echo "security: gitleaks found secrets in the working tree" >&2; fail=1; }
gitleaks git . --no-banner || { echo "security: gitleaks found secrets in git history" >&2; fail=1; }

step "zizmor (GitHub Actions workflow audit)"
uvx "zizmor==$ZIZMOR_VERSION" .github/ || { echo "security: zizmor reported workflow findings" >&2; fail=1; }

step "syft (SBOM -> dist/)"
mkdir -p dist
if syft scan dir:. -q -o "spdx-json=dist/sbom.spdx.json" -o "cyclonedx-json=dist/sbom.cdx.json"; then
  echo "wrote dist/sbom.spdx.json and dist/sbom.cdx.json"
else
  echo "security: syft failed to generate an SBOM (non-fatal)" >&2
fi

if [[ "$fail" -ne 0 ]]; then
  echo -e "\nsecurity: FAILED" >&2
  exit 1
fi
echo -e "\nsecurity: passed"
