.PHONY: test coverage bench

GOCACHE ?= /tmp/skills-fs-gocache

test:
	GOCACHE=$(GOCACHE) go test ./...

coverage:
	GOCACHE=$(GOCACHE) go test ./... -coverprofile=coverage.out
	GOCACHE=$(GOCACHE) go tool cover -func=coverage.out
	GOCACHE=$(GOCACHE) ./scripts/check_coverage.sh 85.0 coverage.out

bench:
	GOCACHE=$(GOCACHE) go test ./bench -bench . -benchmem
