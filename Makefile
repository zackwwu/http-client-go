.PHONY: test
test: go-test ## Test Go code

.PHONY: test-cov
test-cov: go-test-cov ## Test Go code (with coverage)

.PHONY: cov-view
cov-view: go-cov-view ## View Go code coverage

.PHONY: cov-report
cov-report: go-cov-report ## Print a Go code coverage report to stdout

.PHONY: gen-mocks
gen-mocks: go-gen-mocks ## Generate mocks

.PHONY: lint
lint: go-lint ## Perform lint checks on code, updates files

.PHONY: lint-check
lint-check: go-lint-check ## Returns an error if code is not linted

.PHONY: help

COL_RED=$(shell [ -n "${TERM}" ] && tput setaf 1)
COL_GREEN=$(shell [ -n "${TERM}" ] && tput setaf 2)
COL_CYAN=$(shell [ -n "${TERM}" ] && tput setaf 6)
COL_GREY=$(shell [ -n "${TERM}" ] && tput setaf 8)
COL_RESET=$(shell [ -n "${TERM}" ] && tput sgr0)

help:
	@# Explicitly grep ./Makefile to avoid any included config files
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' ./Makefile | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

go-test:
	@echo "Running tests..."
	@go test -v ./... 2>&1 | \
		sed -E "/(.*)PASS(.*)/s//\1$(COL_GREEN)PASS$(COL_RESET)\2/ ; /(.*)FAIL(.*)/s//\1$(COL_RED)FAIL$(COL_RESET)\2/ ; /=== (RUN.*)/s//=== $(COL_CYAN)\1$(COL_RESET)/ ; /(\?.*)/s//$(COL_GREY)\1$(COL_RESET)/ ; /^(#.*)/s//$(COL_RED)\1$(COL_RESET)/"

go-test-cov:
	@echo "Running tests..."
	go test -cover -covermode=count -coverprofile=coverage.out ./...
	@echo "Coverage report stored to coverage.out. Run 'make cov-view' to view report."

go-cov-view:
	go tool cover -html=coverage.out

go-cov-report:
	go tool cover -func=coverage.out

go-gen-mocks:
	@rm -rf $(shell find . -type d -name mocks) # Clean mocks
	go generate ./...

.PHONY: eval-lint-targets
eval-lint-targets:
	@# Exclude vendor and mock files from linting
	$(eval LINT_TARGETS := $(shell find . -type d \( -path ./vendor -o -name mocks \) -prune -o -name "*.go"))

go-lint:
	golangci-lint run

go-lint-check: eval-lint-targets
	@# Return an error if gofmt or goimports find any files that need formatting
	[ -z "$(shell gofmt -l $(LINT_TARGETS))" ] # gofmt
	[ -z "$(shell goimports -l $(LINT_TARGETS))" ] # goimports
	@# NB: requires go get github.com/golangci/golangci-lint/cmd/golangci-lint
	golangci-lint run

.DEFAULT_GOAL := help
