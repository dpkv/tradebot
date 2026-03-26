# Copyright (c) 2025 BVK Chaitanya

export GO ?= go
export GOBIN ?= $(CURDIR)
export PATH := $(PATH):$(HOME)/go/bin
export GOTESTFLAGS ?=

DOCKER ?= docker
IMAGE_TRADEBOT ?= tradebot
IMAGE_IBKR_CP_GW ?= ibkr-cp-gw

# Branch name (lowercase, docker-tag-safe), commit date YYYYMMDD, short SHA for image tags.
GIT_BRANCH_SAFE := $(shell git rev-parse --abbrev-ref HEAD | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9._-]/-/g')
GIT_COMMIT_DATE_COMPACT := $(shell git log -1 --format=%cd --date=format:%Y%m%d)
GIT_COMMIT_SHORT := $(shell git rev-parse --short=7 HEAD)
TRADEBOT_IMAGE_TAG := $(GIT_BRANCH_SAFE)-$(GIT_COMMIT_DATE_COMPACT)-$(GIT_COMMIT_SHORT)

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

.PHONY: docker-tradebot
docker-tradebot:
	$(DOCKER) build -f docker/tradebot/Dockerfile -t $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG) .

.PHONY: docker-ibkr-cp-gw
docker-ibkr-cp-gw:
	$(DOCKER) build -f docker/ibkr-cp-gw/Dockerfile -t $(IMAGE_IBKR_CP_GW):latest .
