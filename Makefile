.PHONY: all test coverage coverage-all race bench build gen-docs lint vulncheck clean fmt fmt-check binding-go binding-node binding-python binding-test quick ci

GOCACHE ?= /tmp/skills-fs-gocache

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.gitCommit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME)

all: lint test vulncheck

build:
	go build -ldflags "$(LDFLAGS)" -o skills-fs ./cmd/skills-fs

CORE_PKGS := $(shell go list ./... | grep -v '/adapter' | grep -v '/examples/' | grep -v '/cmd/')

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

DOCS_PKGS := core adapter adapter/fuse adapter/webdav adapter/websocket provider/cache provider/http provider/local

gen-docs:
	@mkdir -p docs/api
	@for pkg in $(DOCS_PKGS); do \
		out=$$(echo "$$pkg" | tr '/' '_'); \
		echo "generating docs/api/$$out.md ..."; \
		go run github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest -o "docs/api/$$out.md" "./$$pkg"; \
	done

lint:
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt would reformat:" && gofmt -l . && exit 1)

clean:
	rm -f skills-fs webdav-server websocket-events websocket-reconnect basic coverage.out
	rm -rf binding/nodejs/lib binding/nodejs/build
	rm -rf binding/python/lib
	rm -rf binding/python/__pycache__

binding-go:
	@mkdir -p binding/nodejs/lib
	go build -buildmode=c-shared \
		-o binding/nodejs/lib/libgobridge.so \
		./binding/go-bridge

binding-node: binding-go
	cd binding/nodejs && npm install --no-audit --no-fund && npm run build

binding-python:
	@mkdir -p binding/python/lib
	go build -buildmode=c-shared \
		-o binding/python/lib/libgobridge.so \
		./binding/go-bridge
	@echo "Python module ready.  cd binding/python && python3 test_skills_fs.py"

binding-test: binding-node binding-python
	cd binding/nodejs && npm test
	cd binding/python && python3 test_skills_fs.py

quick: fmt-check
	go vet ./...
	go test ./core ./binding/registry ./provider/... -count=1

ci: fmt-check lint test coverage race vulncheck bench
	@echo "All CI checks passed."
