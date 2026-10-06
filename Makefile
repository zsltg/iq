# iq build and release automation. The developer command catalogue is DEVELOPMENT.md.
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

# The tool versions (GoReleaser, svu, and the rest) live in one file, which bash and
# the workflows read too. Every tool runs through `go run`, so none enters go.mod.
include scripts/tool-versions.env

# GoReleaser builds and publishes the cross-platform release artifacts (see
# .goreleaser.yaml).
GORELEASER := github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

# The README demo (docs/docs/assets/demo.svg) is recorded by expect + asciinema + termsvg.
# Its knobs and recipes live beside the scripts they drive, because every value in
# there changes the recorded pixels and is hashed with that directory: a knob moved
# there is a re-record, and an edit to this file is not. Targets: demo, demo-record,
# demo-check.
#
# Pinned first, because make takes its default goal from the first target it reads and
# the include supplies one: without this, a bare `make` would record the demo.
.DEFAULT_GOAL := build
include scripts/demo/demo.mk

.PHONY: build version changelog release release-tag hooks check cover security sbom e2e bench fuzz docs docs-serve capabilities mutation ci man completions release-check release-snapshot

# build compiles the binary with version metadata embedded, static and
# trimmed exactly like a release artifact (goreleaser mirrors these flags).
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o iq .

# version prints the version the next release would take.
version:
	@go run github.com/caarlos0/svu@$(SVU_VERSION) next

# changelog regenerates CHANGELOG.md in place (no bump, commit, or tag),
# including any unreleased commits under the next version.
changelog:
	bash scripts/release.sh --changelog-only

# release regenerates the changelog and commits it on a chore/release branch, for
# a pull request. release-tag tags the squash-merged release commit on main.
release:
	bash scripts/release.sh

release-tag:
	bash scripts/release.sh --tag

# hooks points core.hooksPath at the committed .githooks, which guard the commit
# identity: the address a repository publishes is the one in its git config, and
# an override is expensive to remove once pushed. Per checkout, worktrees
# included, so run it once in each.
hooks:
	git config core.hooksPath .githooks
	@echo "hooks: core.hooksPath -> .githooks (pre-commit, pre-push)"

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
	go run github.com/anchore/syft/cmd/syft@$(SYFT_VERSION) scan dir:. -q -o spdx-json=dist/sbom.spdx.json -o cyclonedx-json=dist/sbom.cdx.json
	@echo "wrote dist/sbom.spdx.json and dist/sbom.cdx.json"

# e2e runs the black-box smoke tests that build and drive the iq binary.
e2e:
	go test ./e2e/

# bench runs the decode and pre-filter benchmarks (no containers, no network). Pipe
# two runs through benchstat to compare before/after a change:
#   make bench | tee new.txt; benchstat old.txt new.txt.
bench:
	go test -bench=. -benchmem -run='^$$' ./internal/query/... ./internal/numfmt/... ./internal/rawpred/... ./drivers/redis/...

# fuzz runs every Go native fuzz target over the parsers that read untrusted bytes,
# one target at a time (no containers, no network). IQ_FUZZ_TIME sets the budget per
# target (default 20s). A crasher lands in <pkg>/testdata/fuzz/<Name>/. Commit it
# with the fix as the regression seed. Seed inputs also run under `go test -short`.
fuzz:
	bash scripts/fuzz.sh

# docs builds the documentation site (Zensical) into docs/site/; needs uv.
docs:
	bash scripts/docs.sh build

# docs-serve runs the documentation dev server on 0.0.0.0:8000 (LAN-reachable).
docs-serve:
	bash scripts/docs.sh serve

# man regenerates the committed iq(1) man page (deterministic; no date/version)
# into docs/man/iq.1. Packaged and installed by goreleaser/nfpm.
man:
	@mkdir -p docs/man
	go run . man > docs/man/iq.1

# completions regenerates the committed shell completion scripts into
# docs/completions/ for the four shells. zsh is committed as iq.zsh and renamed to
# _iq by nfpm at package time; powershell is documented as a manual install.
completions:
	@mkdir -p docs/completions
	go run . completion bash > docs/completions/iq.bash
	go run . completion zsh > docs/completions/iq.zsh
	go run . completion fish > docs/completions/iq.fish
	go run . completion powershell > docs/completions/iq.ps1

# release-check validates .goreleaser.yaml (no build, no publish).
release-check:
	go run $(GORELEASER) check

# release-snapshot builds the full release into dist/ locally without publishing,
# proving the goreleaser config end to end. Needs network for the go run download.
release-snapshot:
	go run $(GORELEASER) release --snapshot --clean --skip=publish

# capabilities runs the capability-drift gate (capslock) against capslock-baseline.json.
# It runs only when go.mod or go.sum differ from the merge-base with origin/main
# (override with IQ_CAPS_BASE; IQ_CAPS_FORCE=1 runs it regardless), because the analysis
# costs ~7.3 GB peak RSS. A gained or lost capability fails it; record a new set with
# IQ_CAPS_UPDATE_BASELINE=1 and justify it in capslock-baseline.notes.md. IQ_CAPS_GOOS
# re-runs it for darwin or windows as a review aid (the baseline is linux-only). The
# wrapper provisions the pinned capslock itself (go install into a temp dir).
capabilities:
	bash scripts/capabilities.sh

# mutation runs the mutation gate over the branch diff against origin/main (override
# with IQ_MUTATION_BASE; empty for a full-module scan, or pass a package path for a
# full scan of it). Any escaped mutant not in mutago-baseline.json fails the gate.
# The wrapper provisions the pinned mutago itself (go install into a temp dir); needs Go
# toolchain access and the integration services up (Docker, or IQ_*_URL).
mutation:
	bash scripts/mutation-gate.sh

# ci is the full pre-merge gate: fast checks, the covered full suite (which
# includes e2e), the security sweep, the capability gate, then the mutation gate over
# the branch diff. Needs Docker and network. mutago reruns the suite per mutant, so this
# is the slowest target — start a shared stack (docker compose up -d --wait) first. The
# capability step sits between them so its ~7.3 GB analysis never overlaps the mutation
# gate's memory, and it usually skips outright (no go.mod/go.sum change).
ci:
	$(MAKE) check
	$(MAKE) cover
	$(MAKE) security
	$(MAKE) capabilities
	$(MAKE) mutation
