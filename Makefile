.DEFAULT_GOAL := help

BINARY ?= bin/telecrawl
VERSION ?=

.PHONY: help build test test-coverage test-race run fmt deps lint secrets check snapshot release-check release-pilot release-draft verify-release release release-homebrew

help: ## Print available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the CLI into bin/telecrawl.
	mkdir -p $(dir $(BINARY))
	GOWORK=off go build -o $(BINARY) ./cmd/telecrawl

test: ## Run the full test suite.
	GOWORK=off go test -count=1 ./...

test-coverage: ## Run tests and enforce the 35 percent coverage floor.
	GOWORK=off go test -count=1 ./... -coverprofile=coverage.out
	./scripts/coverage.sh 35.0

test-race: ## Run the full test suite with the race detector.
	GOWORK=off go test -count=1 -race ./...

run: ## Run the CLI with optional ARGS.
	GOWORK=off go run ./cmd/telecrawl $(ARGS)

fmt: ## Check Go formatting with the CI-pinned gofumpt version.
	@set -e; changed="$$(GOWORK=off go run mvdan.cc/gofumpt@v0.10.0 -l .)"; \
	if [ -n "$$changed" ]; then printf 'gofumpt wants changes in:\n%s\n' "$$changed"; exit 1; fi

deps: ## Verify module metadata and known vulnerabilities.
	GOWORK=off go mod verify
	GOWORK=off go mod tidy
	git diff --exit-code -- go.mod go.sum
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@v1.5.0 ./...

lint: ## Run every static analyzer enforced by CI.
	GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
	GOWORK=off go vet ./...
	GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
	GOWORK=off go run golang.org/x/tools/cmd/deadcode@v0.48.0 ./cmd/telecrawl
	GOWORK=off go run github.com/securego/gosec/v2/cmd/gosec@v2.27.1 -exclude=G101,G115,G202,G301,G304 ./...

secrets: ## Scan Git history and the working tree with gitleaks.
	GOWORK=off go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --no-banner --redact
	GOWORK=off go run github.com/zricethezav/gitleaks/v8@v8.30.1 dir . --no-banner --redact

check: ## Run every local gate enforced by CI.
	$(MAKE) deps
	$(MAKE) fmt
	$(MAKE) lint
	$(MAKE) test-coverage
	$(MAKE) test-race
	$(MAKE) build
	$(MAKE) release-check
	$(MAKE) snapshot
	$(MAKE) secrets

snapshot: ## Build credential-free snapshot artifacts without publishing.
	GOWORK=off goreleaser release --snapshot --clean --skip=publish --parallelism=2

release-check: ## Validate the local signing, packaging, and release contracts.
	env -u GOTOOLCHAIN ./scripts/release-local --check

release-pilot: ## Build and verify a notarized, unpublished pilot for VERSION=vX.Y.Z.
	@test -n "$(VERSION)" || (echo "usage: make release-pilot VERSION=v0.3.5" >&2; exit 2)
	./scripts/release-local pilot "$(VERSION)"

release-draft: ## Build, sign, notarize, and upload the verified draft release.
	./scripts/release-local draft

verify-release: ## Verify draft artifacts natively for VERSION=vX.Y.Z.
	@test -n "$(VERSION)" || (echo "usage: make verify-release VERSION=v0.3.5" >&2; exit 2)
	./scripts/release-local verify-draft "$(VERSION)"

release: ## Publish the natively verified draft for VERSION=vX.Y.Z.
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=v0.3.5" >&2; exit 2)
	./scripts/release-local publish "$(VERSION)"

release-homebrew: ## Verify the published release and update Homebrew.
	@test -n "$(VERSION)" || (echo "usage: make release-homebrew VERSION=v0.3.5" >&2; exit 2)
	./scripts/release-local homebrew "$(VERSION)"
