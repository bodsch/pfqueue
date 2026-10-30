# pfqueue - Go port

BINARY   := pfqueue

BIN_DIR  := bin
DIST_DIR := dist

PKG      := ./cmd/pfqueue

# Version metadata, overridable from the environment / CI.
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# BUILD_DATE is a UTC day, not a timestamp: it is what the release workflow
# stamps, it keeps two builds of the same release comparable, and it is coarse
# enough not to make an otherwise identical build differ by the second.
BUILD_DATE ?= $(shell date -u +%Y-%m-%d)

# Files packed next to the binary into every release archive.
DIST_FILES := README.md

# sha256sum on Linux (CI), shasum on macOS; both write the same format.
SHA256SUM := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

GO       ?= go
GOLANGCI := golangci-lint

# The version CI installs, and the reason it lives here rather than in the
# workflow: it did live there, the workflow pinned v2.12.2 while this machine
# had v2.13.2, and the pipeline failed on something no local run could
# reproduce. v2.12.2 bundles staticcheck v0.7.0, whose IR builder panics
# building Go 1.27's internal/poll — on Linux, where that file is the epoll
# variant, so a macOS run never saw it. One pin, one place.
GOLANGCI_VERSION := v2.14.0

# Pinned like golangci-lint, and the same version as in the CI workflow, so
# a new release cannot turn a pipeline red that passed locally.
GOVULNCHECK_VERSION := v1.9.0

.DEFAULT_GOAL := build

GOFLAGS  ?= -trimpath
LDFLAGS  := -s -w -X main.Version=$(VERSION)

export CGO_ENABLED ?= 0

# Linux only: pfqueue runs on the mail server next to Postfix/Exim.
PLATFORMS := linux/amd64 linux/arm64

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/.
	@mkdir -p $(BIN_DIR)
	@for b in $(BINARY); do \
		echo "building $(BIN_DIR)/$$b"; \
		$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$$b ./cmd/$$b/ || exit 1; \
	done

# The race detector requires cgo and a C compiler on every platform except
# darwin, where it also links without one. Overriding CGO_ENABLED here rather
# than dropping the export above keeps the binaries static while letting the
# tests run: without it, `go test -race` fails on Linux with
# "-race requires cgo; enable cgo by setting CGO_ENABLED=1".
# -timeout 3m: a hanging test fails with a stack dump that names it,
# instead of blocking for go test's default of 10 minutes.
.PHONY: test
test: ## Run the test suite with the race detector (needs a C compiler).
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout 3m ./...

.PHONY: cover
cover: ## Run tests and write a coverage profile.
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout 3m -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: bench
bench: ## Run micro-benchmarks (no tests) with allocation stats.
	CGO_ENABLED=1 $(GO) test -run '^$$' -bench . -benchmem ./...

# The directory list comes from `go list` rather than being spelled out: a fixed
# `gofmt -l cmd internal` reports a missing directory on stderr and then exits
# 0, so it passes while checking nothing.
.PHONY: fmt
fmt: ## Fail if anything is not gofmt-clean.
	@dirs="$$($(GO) list -f '{{.Dir}}' ./... 2>/dev/null)"; \
	if [ -z "$$dirs" ]; then echo "no Go packages found"; exit 1; fi; \
	out="$$(gofmt -l $$dirs)"; \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; echo "run 'gofmt -w $$dirs'."; exit 1; fi

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint (uses .golangci.yml if present).
	@command -v $(GOLANGCI) >/dev/null 2>&1 || { \
		echo "$(GOLANGCI) not found — install it from https://golangci-lint.run/welcome/install/"; \
		exit 1; \
	}
	$(GOLANGCI) run ./...

.PHONY: lint-install
lint-install: ## Install the pinned golangci-lint (what CI runs).
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

.PHONY: vuln
vuln: ## Scan deps and reachable code for known vulnerabilities (govulncheck).
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: sec
sec: lint vuln ## Security checks: golangci-lint (incl. gosec) + govulncheck.

.PHONY: tidy
tidy: ## Tidy and verify go.mod/go.sum.
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: ci
ci: fmt vet lint vuln test build ## Full CI pipeline: fmt, vet, lint, vuln, test (race), build.

.PHONY: clean
clean: ## Remove build artefacts.
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out

.PHONY: release
release: ## Cross-compile static release binaries into dist/.
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		for b in $(BINARY); do \
			out=$(DIST_DIR)/$$b-$$os-$$arch; \
			echo "building $$out"; \
			GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/$$b/ || exit 1; \
		done; \
	done

# One archive per platform, named <binary>-<version>-<os>-<arch>.tar.gz, with
# the binary under its plain name, plus SHA256SUMS over all archives. This is
# what the release workflow uploads, so a local `make dist` shows exactly what
# a release will contain.
.PHONY: dist
dist: clean release ## Build release archives and SHA256SUMS into dist/.
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		for b in $(BINARY); do \
			name=$$b-$(VERSION)-$$os-$$arch; \
			stage=$(DIST_DIR)/$$name; \
			mkdir -p $$stage || exit 1; \
			cp $(DIST_DIR)/$$b-$$os-$$arch $$stage/$$b && cp $(DIST_FILES) $$stage/ || exit 1; \
			tar -C $(DIST_DIR) -czf $(DIST_DIR)/$$name.tar.gz $$name || exit 1; \
			rm -rf $$stage $(DIST_DIR)/$$b-$$os-$$arch; \
			echo "packed $(DIST_DIR)/$$name.tar.gz"; \
		done; \
	done
	@cd $(DIST_DIR) && $(SHA256SUM) *.tar.gz > SHA256SUMS
