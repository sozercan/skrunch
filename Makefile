GO ?= go
GOFMT ?= gofmt
BINARY ?= skrunch
CMD_DIR := ./cmd/skrunch
GOFILES := $(shell find . -name '*.go' -type f -not -path './vendor/*')

.PHONY: help build run test test-e2e fmt fmt-check vet check install clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  build      Build $(BINARY)' \
		'  run        Run the CLI with go run' \
		'  test       Run unit tests with go test ./...' \
		'  test-e2e   Run end-to-end CLI tests' \
		'  fmt        Format Go source files in place' \
		'  fmt-check  Fail if any Go files need formatting' \
		'  vet        Run go vet ./...' \
		'  check      Run fmt-check, vet, unit tests, and e2e tests' \
		'  install    Install the CLI with go install' \
		'  clean      Remove $(BINARY)'

build:
	@mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) $(CMD_DIR)

run:
	$(GO) run $(CMD_DIR)

test:
	$(GO) test ./...

test-e2e:
	$(GO) test -tags=e2e ./internal/cli

fmt:
	$(GOFMT) -w $(GOFILES)

fmt-check:
	@out="$$( $(GOFMT) -l $(GOFILES) )"; \
	if [ -n "$$out" ]; then \
		printf 'unformatted files:\n%s\n' "$$out"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

check: fmt-check vet test test-e2e

install:
	$(GO) install $(CMD_DIR)

clean:
	rm -f $(BINARY)
