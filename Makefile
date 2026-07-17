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

.PHONY: build version changelog release tools tools-dev check cover security sbom e2e bench mutation ci

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
	go install github.com/quality-gates/mutago/v2/cmd/mutago@v2.7.7
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

# bench runs the decode and pre-filter benchmarks (no containers, no network). Pipe
# two runs through benchstat to compare before/after a change:
#   make bench | tee new.txt; benchstat old.txt new.txt.
bench:
	go test -bench=. -benchmem -run='^$$' ./internal/query/... ./internal/numfmt/... ./drivers/redis/...

# mutation runs the mutation gate over the branch diff against origin/main (override
# with IQ_MUTATION_BASE; empty for a full-module scan, or pass a package path for a
# full scan of it). Any escaped mutant not in mutago-baseline.json fails the gate.
# Needs mutago (make tools-dev) and the integration services up (Docker, or IQ_*_URL).
mutation:
	bash scripts/mutation-gate.sh

# ci is the full pre-merge gate: fast checks, the covered full suite (which
# includes e2e), the security sweep, then the mutation gate over the branch diff.
# Needs Docker and network. mutago reruns the suite per mutant, so this is the
# slowest target — start a shared stack (docker compose up -d --wait) first.
ci:
	$(MAKE) check
	$(MAKE) cover
	$(MAKE) security
	$(MAKE) mutation
