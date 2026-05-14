# skills-fs

Embedded virtual filesystem engine for exposing host application capabilities and Agent Skills as ordinary files.

## Current Status

M2/M3 complete. The repository contains:

- **Core**: routing, permissions, provider dispatch, POSIX error mapping, handle manager, advisory flock, write buffering, stream ring buffers, event bus, metrics.
- **FUSE adapter**: Linux implementation using `go-fuse/v2` with inotify forwarding. Build-tagged stub for other platforms.
- **WebDAV adapter**: stub (pending prioritization).
- **Tests**: `core` package at >88% statement coverage.
- **Benchmarks**: path resolution, stat, write, lock contention, serial queue.

## Quick Start

```sh
go test ./...
go test ./bench -bench .
make coverage
make race
```

## Design Documents

- [Development handoff](docs/DEVELOPMENT_HANDOFF.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Testing](docs/TESTING.md)
- [Milestones](docs/MILESTONES.md)
