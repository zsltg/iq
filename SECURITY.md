# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: open
[Security → Report a vulnerability](https://github.com/zsltg/iq/security/advisories/new)
on the repository. Do not open a public issue or pull request for a security
problem. You will get an acknowledgement within a week, and the report stays
private until a fix is released.

## Supported versions

Only the latest release receives fixes. Upgrade to the current
[release](https://github.com/zsltg/iq/releases) before reporting.

## What is already checked

Every push and pull request runs, in CI, `govulncheck` (reachable Go
vulnerabilities), `osv-scanner` (the whole dependency graph, with a permissive
license allowlist), `gitleaks` (working tree and history), an SBOM build,
`gosec` through `golangci-lint`, and `capslock` (capability drift of the
dependency tree against a committed baseline). A weekly run repeats the
vulnerability and secret scans against fresh advisory data, and the OpenSSF
Scorecard workflow reports the repository's security posture. Socket reviews
every dependency change on a pull request.
