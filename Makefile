.PHONY: test coverage coverage-all race bench build

GOCACHE ?= /tmp/skills-fs-gocache

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.gitCommit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME)

build:
	go build -ldflags "$(LDFLAGS)" -o skills-fs ./cmd/skills-fs

CORE_PKGS := $(shell go list ./... | grep -v '/adapter')

test:
	GOCACHE=$(GOCACHE) go test ./...

coverage:
	GOCACHE=$(GOCACHE) go test $(CORE_PKGS) -coverprofile=coverage.out
	GOCACHE=$(GOCACHE) go tool cover -func=coverage.out
	GOCACHE=$(GOCACHE) ./scripts/check_coverage.sh 85.0 coverage.out

coverage-all:
	GOCACHE=$(GOCACHE) go test ./... -coverprofile=coverage.out
	GOCACHE=$(GOCACHE) go tool cover -func=coverage.out

race:
	GOCACHE=$(GOCACHE) go test -race ./core

bench:
	GOCACHE=$(GOCACHE) go test ./bench -bench . -benchmem
