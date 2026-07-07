# iq build and release automation. See README "Common commands".
# Release logic lives in scripts/release.sh; this Makefile is a thin wrapper and
# the single source of truth for build-time version embedding.

# Version metadata embedded via ldflags. DATE uses the commit time, not the wall
# clock, so a build from a given commit is reproducible.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/zsltg/iq/cmd.version=$(VERSION) \
           -X github.com/zsltg/iq/cmd.commit=$(COMMIT) \
           -X github.com/zsltg/iq/cmd.date=$(DATE)

.PHONY: build version changelog release tools tools-dev check cover security sbom e2e ci

# build compiles the binary with version metadata embedded.
build:
	go build -ldflags "$(LDFLAGS)" -o iq .

# version prints the version the next release would take.
version:
	@svu next

# changelog regenerates CHANGELOG.md in place (no bump, commit, or tag),
# including any unreleased commits under the next version.
changelog:
	bash scripts/release.sh --changelog-only

# release bumps the version, regenerates the changelog, commits, and tags.
release:
	bash scripts/release.sh

# tools installs the release toolchain (svu, git-chglog) into GOPATH/bin.
tools:
	go install github.com/caarlos0/svu@latest
	go install github.com/git-chglog/git-chglog/cmd/git-chglog@latest

# tools-dev installs the quality and security toolchain into GOPATH/bin. gofumpt,
# goimports, and golangci-lint are expected already (see README).
tools-dev:
	go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
	go install golang.org/x/tools/cmd/deadcode@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/google/osv-scanner/v2/cmd/osv-scanner@latest
	go install github.com/zricethezav/gitleaks/v8@latest
	go install github.com/anchore/syft/cmd/syft@latest

# check is the fast, offline gate: format, vet, build, lint, dead code, and the
# unit suite with a coverage report. No Docker, no network.
check:
	bash scripts/check.sh

# cover runs the full suite and enforces the coverage floor (IQ_COVER_MIN, default
# 80); needs Docker, or point IQ_*_URL at a running stack. IQ_COVER_SHORT=1 gives
# the fast report-only path.
cover:
	bash scripts/coverage.sh

# security runs the supply-chain + secrets sweep (govulncheck, osv-scanner,
# gitleaks) and writes SBOMs to dist/; touches the network.
security:
	bash scripts/security.sh

# sbom writes SPDX + CycloneDX SBOMs of the module to dist/.
sbom:
	@mkdir -p dist
	syft scan dir:. -q -o spdx-json=dist/sbom.spdx.json -o cyclonedx-json=dist/sbom.cdx.json
	@echo "wrote dist/sbom.spdx.json and dist/sbom.cdx.json"

# e2e runs the black-box smoke tests that build and drive the iq binary.
e2e:
	go test ./e2e/

# ci is the full pre-merge gate: fast checks, then the covered full suite (which
# includes e2e), then the security sweep. Needs Docker and network. The mutation
# gate (scripts/mutation-gate.sh) is a separate, slower step.
ci:
	$(MAKE) check
	$(MAKE) cover
	$(MAKE) security
