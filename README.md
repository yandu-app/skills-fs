# skills-fs

Embedded virtual filesystem engine for exposing host application capabilities and Agent Skills as ordinary files.

## Current Status

M2/M3/M4/M5 complete. The repository contains:

- **Core**: radix-tree routing, POSIX permissions, provider dispatch, POSIX error mapping, sharded handle manager, advisory flock, write buffering, stream ring buffers with backpressure (block/drop/error), event bus, Prometheus metrics, skill generator, provider result cache, namespace isolation.
- **FUSE adapter**: Linux implementation using `go-fuse/v2` with inotify forwarding. Build-tagged stub for other platforms.
- **WebDAV adapter**: full HTTP server with GET, HEAD, PUT, PROPFIND, OPTIONS, COPY, MOVE, LOCK, Basic Auth, TLS, gzip, CORS, rate limiting, ETags, Range requests, conditional COPY/MOVE, property caching, and `/metrics` endpoint.
- **WebSocket adapter**: streaming filesystem operations over WebSocket with JSON and binary messages, per-message deflate compression, batch operations, subscription IDs, ping/pong keepalive, and `/metrics` endpoint.
- **HTTP provider bridge**: `provider/http` package forwards Invoke calls to remote HTTP endpoints with retry and circuit breaker.
- **CLI**: `cmd/skills-fs` with `webdav`, `websocket`, `fuse`, `validate`, and `version` commands, JSON config file support, SIGHUP config reload.
- **Host bindings**: Node.js N-API and Python ctypes wrappers around the same C shared library (`libgobridge.so`) generated from the Go core.
- **Tests**: `core` package at >91% statement coverage; total coverage >85%. Fuzz tests for router and path normalization.
- **Benchmarks**: path resolution, stat, write, lock contention, serial queue, event emit, stream read/write, handle open/close, HTTP provider roundtrip.
- **CI gates**: format, lint, unit tests, race detector, coverage (85%), vulnerability scan, cross-platform build (linux/darwin/windows), cross-platform test (macOS/Windows), benchmark smoke, benchmark regression gate (benchstat), Node binding, Python binding.

## Quick Start

```sh
go test ./...
go test ./bench -bench .
make coverage
make race
```

## CLI Usage

```sh
# Start WebDAV server
go run ./cmd/skills-fs webdav -addr :8080

# Start WebSocket server
go run ./cmd/skills-fs websocket -addr :8081

# Start with a config file
go run ./cmd/skills-fs webdav -config config.json

# Validate config without starting server
go run ./cmd/skills-fs validate -config config.json

# Example config.json
{
  "mounts": [
    {"path": "/hello", "kind": "blob", "mode": "0644", "data": "world"},
    {"path": "/api", "kind": "api", "read": "greet", "provider": "remote"}
  ],
  "providers": [
    {"id": "remote", "url": "http://localhost:9000"}
  ]
}
```

Signals:
- `SIGINT` / `SIGTERM`: graceful shutdown
- `SIGHUP`: reload configuration file (webdav / websocket commands)

Metrics:
- Prometheus text format at `/metrics` on both WebDAV and WebSocket servers

## Mount Kinds

- **blob** — static file with inline content.
- **api** — file whose content is produced by a provider `read` action; writes can be forwarded as provider `write` actions (with optional JSON payload forwarding via `writeParams: "json"`).
- **dir** — static directory containing nested mounts.
- **dynamic_dir** — provider-backed directory. On `readdir`, the configured `read` action is invoked and the returned JSON entries are rendered as directory contents. This lets agents `cd` into hierarchies whose entries are not known at config time (e.g. `groups/:group_id/:time_range/:message_id`). Each dynamic entry is matched against existing mounts to determine its kind.
- **stream** — bounded ring buffer for streaming data.
- **link** — symbolic link.

Dynamic directory provider response formats:

```json
["entry1", "entry2"]
```

or

```json
{"entries": [{"name": "entry1", "kind": "dynamic_dir", "mode": "0755"}, {"name": "entry2", "kind": "api"}]}
```

`kind` is optional; when omitted it is inferred from any registered mount at the child path.

## Agent Guidance (AGENTS.md)

A planned enhancement is per-directory `AGENTS.md` files. When present in a directory (static or dynamic), skills-fs will expose an `AGENTS.md` file that describes the directory's purpose, available subdirectories, and parameter options. This gives agents explicit hints so they can navigate provider-backed filesystems without guessing.

## Development

```sh
make all      # lint + test + vulncheck
make quick    # fmt + vet + core/registry/provider tests (fast)
make ci       # fmt + lint + test + coverage + race + vulncheck + bench (full)
make lint     # go vet + staticcheck
make test     # run all tests
make race     # run core tests with race detector
make coverage # check core coverage against 85% gate
make vulncheck# scan dependencies for vulnerabilities
make bench         # run benchmarks
make bench-gate    # compare benchmarks against baseline (benchstat)
make gen-docs      # regenerate API reference docs
make binding-node  # build Node.js N-API addon
make binding-python# build Python ctypes module
make clean         # remove build artifacts
```

## Design Documents

- [Development handoff](docs/DEVELOPMENT_HANDOFF.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Testing](docs/TESTING.md)
- [Milestones](docs/MILESTONES.md)
