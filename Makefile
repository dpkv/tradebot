# Copyright (c) 2025 BVK Chaitanya

export GO ?= go
export GOBIN ?= $(CURDIR)
export PATH := $(PATH):$(HOME)/go/bin
export GOTESTFLAGS ?=

DOCKER ?= docker
IMAGE_TRADEBOT ?= tradebot
IMAGE_IBKR_CP_GW ?= ibkr-cp-gw

# Auto-detect host timezone: Linux uses /etc/timezone, macOS uses the /etc/localtime symlink.
# If detection is empty, fall back to TZ from the environment (export TZ=... before make).
HOST_TZ ?= $(shell cat /etc/timezone 2>/dev/null || readlink /etc/localtime 2>/dev/null | sed 's|.*/zoneinfo/||')
CONTAINER_TZ := $(if $(strip $(HOST_TZ)),$(strip $(HOST_TZ)),$(strip $(TZ)))
DOCKER_TZ_FLAGS := $(if $(CONTAINER_TZ),-e TZ=$(CONTAINER_TZ),)

# Branch name (lowercase, docker-tag-safe), commit date YYYYMMDD, short SHA for image tags.
GIT_BRANCH_SAFE := $(shell git rev-parse --abbrev-ref HEAD | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9._-]/-/g')
GIT_COMMIT_DATE_COMPACT := $(shell git log -1 --format=%cd --date=format:%Y%m%d)
GIT_COMMIT_SHORT := $(shell git rev-parse --short=7 HEAD)
GIT_DIRTY := $(shell git diff --quiet && git diff --cached --quiet || date +-%H%M-dirty)
TRADEBOT_IMAGE_TAG := $(GIT_BRANCH_SAFE)-$(GIT_COMMIT_DATE_COMPACT)-$(GIT_COMMIT_SHORT)$(GIT_DIRTY)

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

.PHONY: docker-build-tradebot
docker-build-tradebot:
	$(DOCKER) build -f docker/tradebot/Dockerfile -t $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG) .

.PHONY: docker-build-tradebot-live
docker-build-tradebot-live: docker-build-tradebot
	$(DOCKER) tag $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG) $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG)-live

.PHONY: docker-build-tradebot-paper
docker-build-tradebot-paper: docker-build-tradebot
	$(DOCKER) tag $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG) $(IMAGE_TRADEBOT):$(TRADEBOT_IMAGE_TAG)-paper

.PHONY: docker-build-ibkr-cp-gw
docker-build-ibkr-cp-gw:
	$(DOCKER) build -f docker/ibkr-cp-gw/Dockerfile -t $(IMAGE_IBKR_CP_GW):latest .

# Run IBKR Client Portal Gateway: map host PORT to container 5000.
# Usage: make docker-run-ibkr-cp-gw PORT=3000 CNAME=ibkr-cp-gw
.PHONY: docker-run-ibkr-cp-gw
docker-run-ibkr-cp-gw:
	@test -n "$(PORT)" || (echo "usage: make docker-run-ibkr-cp-gw PORT=<host-port> CNAME=<docker-hostname-and-container-name>" >&2; exit 1)
	@test -n "$(CNAME)" || (echo "usage: make docker-run-ibkr-cp-gw PORT=<host-port> CNAME=<docker-hostname-and-container-name>" >&2; exit 1)
	$(DOCKER) run -d --restart unless-stopped --hostname "$(CNAME)" --name "$(CNAME)" $(DOCKER_TZ_FLAGS) -p $(PORT):5000 $(IMAGE_IBKR_CP_GW):latest

# Run tradebot image with data directory on the host mounted at /root/.tradebot.
# PORT maps host port to container 10000 (tradebot server default).
# Usage: make docker-run-tradebot TAG=branch-YYYYMMDD-abc1234 DATA_DIR=/path/to/data CNAME=tradebot PORT=10000
.PHONY: docker-run-tradebot
docker-run-tradebot:
	@test -n "$(TAG)" || (echo "usage: make docker-run-tradebot TAG=<image-tag> DATA_DIR=<host-data-dir> CNAME=<docker-hostname-and-container-name> PORT=<host-port>" >&2; exit 1)
	@test -n "$(DATA_DIR)" || (echo "usage: make docker-run-tradebot TAG=<image-tag> DATA_DIR=<host-data-dir> CNAME=<docker-hostname-and-container-name> PORT=<host-port>" >&2; exit 1)
	@test -n "$(CNAME)" || (echo "usage: make docker-run-tradebot TAG=<image-tag> DATA_DIR=<host-data-dir> CNAME=<docker-hostname-and-container-name> PORT=<host-port>" >&2; exit 1)
	@test -n "$(PORT)" || (echo "usage: make docker-run-tradebot TAG=<image-tag> DATA_DIR=<host-data-dir> CNAME=<docker-hostname-and-container-name> PORT=<host-port>" >&2; exit 1)
	$(DOCKER) run -d --restart unless-stopped --hostname "$(CNAME)" --name "$(CNAME)" $(DOCKER_TZ_FLAGS) -p $(PORT):10000 -v "$(abspath $(DATA_DIR))":/root/.tradebot $(IMAGE_TRADEBOT):$(TAG)

# Stop a container; -t is seconds to wait after SIGTERM before SIGKILL (5 minutes).
# Usage: make docker-stop CNAME=<container-name-or-id>
.PHONY: docker-stop
docker-stop:
	@test -n "$(CNAME)" || (echo "usage: make docker-stop CNAME=<container-name-or-id>" >&2; exit 1)
	$(DOCKER) stop -t 300 "$(CNAME)"
