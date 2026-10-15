# grab the latest git tag, remove the 'v' prefix if present, and handle dirty/dev commits
VERSION ?= $(shell git describe --tags --always --dirty | sed 's/^v//')
# who built the binary, shown by `rackwatch version`; CI overrides this
BUILT_BY ?= $(shell whoami)

.DEFAULT_GOAL := help

## help: print this help message
.PHONY: help
help:
	@echo 'usage: make <target>'
	@echo
	@echo '  where <target> is one of the following:'
	@echo
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

.PHONY: needs-golangci-lint
needs-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo >&2 "golangci-lint is required but it's not installed. Aborting."; exit 1; }

.PHONY: needs-govulncheck
needs-govulncheck:
	@command -v govulncheck >/dev/null 2>&1 || { echo >&2 "govulncheck is required but it's not installed. Aborting."; exit 1; }

## build: build the rackwatch binary
.PHONY: build
build:
	@echo "Building rackwatch version $(VERSION)"
	@go build -ldflags "-X main.version=$(VERSION) -X 'main.builtBy=$(BUILT_BY)'"

## test: run tests
.PHONY: test
test:
	@echo "Running tests"
	@go test ./...
	@go test -v -run TestRender .   # prints the dashboard at rack-screen size

## check-security: run golangci-lint (including gosec) and govulncheck to check for security vulnerabilities
.PHONY: check-security
check-security: needs-golangci-lint needs-govulncheck
	golangci-lint run ./...
	govulncheck ./...
