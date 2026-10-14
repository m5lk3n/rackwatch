# grab the latest git tag, remove the 'v' prefix if present, and handle dirty/dev commits
VERSION ?= $(shell git describe --tags --always --dirty | sed 's/^v//')
# who built the binary, shown by `rackwatch version`; CI overrides this
BUILT_BY ?= $(shell whoami)

## help: print this help message
.PHONY: help
help:
	@echo 'usage: make <target>'
	@echo
	@echo '  where <target> is one of the following:'
	@echo
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

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
