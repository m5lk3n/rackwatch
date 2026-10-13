# grab the latest git tag, remove the 'v' prefix if present, and handle dirty/dev commits
VERSION ?= $(shell git describe --tags --always --dirty | sed 's/^v//')

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
	@go build -ldflags "-X main.version=$(VERSION)"

## test: run tests
.PHONY: test
test:
	@echo "Running tests"
	@go test ./...
	@go test -v -run TestRender .   # prints the dashboard at rack-screen size
