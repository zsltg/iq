#!/usr/bin/env bash
# Security sweep: dependency audit, OSV scan, third-party license texts, secret
# scan, workflow audit, and SBOM generation. Touches the network (vulnerability
# databases). The Go tools run through `go run` at the versions in
# scripts/tool-versions.env, the versions that CI runs. The first run fetches
# them from the module proxy. zizmor runs through `uvx`, so the `uv` tool must
# be installed. A real vulnerability, a leaked secret, a module without a license
# file, or a zizmor workflow finding fails the gate; the SBOMs are written to
# dist/ (gitignored) and never fail it. zizmor runs the offline audits alone. If
# GH_TOKEN or GITHUB_TOKEN is set, zizmor adds the online audits. gosec (Go SAST)
# runs separately via golangci-lint in scripts/check.sh, not here.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

. scripts/tool-versions.env
govulncheck() { go run "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION" "$@"; }
osv-scanner() { go run "github.com/google/osv-scanner/v2/cmd/osv-scanner@$OSV_SCANNER_VERSION" "$@"; }
gitleaks() { go run "github.com/zricethezav/gitleaks/v8@$GITLEAKS_VERSION" "$@"; }

command -v uvx >/dev/null 2>&1 || { echo "security: uvx not found; install uv" >&2; exit 1; }

fail=0
step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

step "govulncheck (Go vulnerability audit, call-graph aware)"
govulncheck ./... || { echo "security: govulncheck reported vulnerabilities" >&2; fail=1; }

step "osv-scanner (OSV dependency scan)"
# Skip .claude: it holds local git worktrees of other branches, and their
# older go.mod files are not part of this tree. Without the skip, a stale
# worktree fails this scan on advisories that this tree has already fixed.
osv-scanner scan source -r --experimental-exclude .claude . || { echo "security: osv-scanner reported vulnerabilities" >&2; fail=1; }

step "osv-scanner (license allowlist, Go modules)"
# The same check as the CI osv job: every Go module against the permissive
# allowlist in scripts/license-allowlist.txt. Only go.mod, because the docs
# site's Python lockfile is not part of the shipped binary.
allow="$(grep -Ev '^(#|$)' scripts/license-allowlist.txt | paste -sd, -)"
# An empty list turns --licenses into a summary with no verdict, so stop.
[ -n "$allow" ] || { echo "security: scripts/license-allowlist.txt lists no license" >&2; exit 1; }
osv-scanner scan source --licenses="$allow" -L go.mod || { echo "security: osv-scanner found a license outside the allowlist" >&2; fail=1; }

step "third-party licenses (texts of every linked module)"
# The release ships these texts. Run it here so that a module without a license
# file fails a pull request and not the release. The output is discarded.
bash scripts/third-party-licenses.sh || { echo "security: the third-party license collection failed" >&2; fail=1; }
rm -rf third-party-licenses

step "gitleaks (secrets: working tree + git history)"
gitleaks dir . --no-banner || { echo "security: gitleaks found secrets in the working tree" >&2; fail=1; }
gitleaks git . --no-banner || { echo "security: gitleaks found secrets in git history" >&2; fail=1; }

step "zizmor (GitHub Actions workflow audit)"
uvx "zizmor==$ZIZMOR_VERSION" .github/ || { echo "security: zizmor reported workflow findings" >&2; fail=1; }

step "syft (SBOM -> dist/)"
# The Makefile target sbom holds the syft options. It lists the Go module graph only.
if ! make --no-print-directory sbom; then
  echo "security: syft failed to generate an SBOM (non-fatal)" >&2
fi

if [[ "$fail" -ne 0 ]]; then
  echo -e "\nsecurity: FAILED" >&2
  exit 1
fi
echo -e "\nsecurity: passed"
