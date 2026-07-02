# Copyright (c) 2025 BVK Chaitanya

export GO ?= go
export GOBIN ?= $(CURDIR)
export PATH := $(PATH):$(HOME)/go/bin
export GOTESTFLAGS ?=

.PHONY: help
help: ## List documented targets (mainly docker-etrade-*).
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  %-28s %s\n", $$1, $$2}'

.PHONY: all
all: go-all go-test go-test-long;

.PHONY: clean
clean:
	git clean -f -X

.PHONY: check
check: all
	$(MAKE) go-test

.PHONY: go-all
go-all: go-generate
	GOOS=linux GOARCH=amd64 $(GO) build -o tradebot.linux .
	GOOS=darwin GOARCH=arm64 $(GO) build -o tradebot.mac .
	$(GO) build -o tradebot .

.PHONY: go-generate
go-generate:
	$(GO) generate ./...

.PHONY: go-test
go-test: go-all
	$(GO) test -fullpath -count=1 -coverprofile=coverage.out -short $(GOTESTFLAGS) ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: go-test-long
go-test-long: go-all
	$(GO) test -fullpath -failfast -count=1 -coverprofile=coverage.out $(GOTESTFLAGS) ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

# Exchange-specific target groups live in their own includable Makefiles
# (e.g. docker-etrade-* below) to keep this file focused on core Go
# build/test -- see Makefile.etrade.
include Makefile.etrade
