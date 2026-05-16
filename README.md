# skills-fs

Embedded virtual filesystem engine for exposing host application capabilities and Agent Skills as ordinary files.

## Current Status

M2/M3/M4 complete. The repository contains:

- **Core**: radix-tree routing, POSIX permissions, provider dispatch, POSIX error mapping, sharded handle manager, advisory flock, write buffering, stream ring buffers with backpressure (block/drop/error), event bus, Prometheus metrics, skill generator.
- **FUSE adapter**: Linux implementation using `go-fuse/v2` with inotify forwarding. Build-tagged stub for other platforms.
- **WebDAV adapter**: full HTTP server with GET, HEAD, PUT, PROPFIND, OPTIONS, Basic Auth, read-only mode, and XML multistatus responses.
- **HTTP provider bridge**: `provider/http` package forwards Invoke calls to remote HTTP endpoints.
- **CLI**: `cmd/skills-fs` with `webdav`, `fuse`, and `version` commands, JSON config file support.
- **Tests**: `core` package at >91% statement coverage; total coverage >85%.
- **Benchmarks**: path resolution, stat, write, lock contention, serial queue, event emit, stream read/write, handle open/close, HTTP provider roundtrip.

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

# Start with a config file
go run ./cmd/skills-fs webdav -config config.json

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

## Design Documents

- [Development handoff](docs/DEVELOPMENT_HANDOFF.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Testing](docs/TESTING.md)
- [Milestones](docs/MILESTONES.md)
