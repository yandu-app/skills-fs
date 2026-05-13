# skills-fs

Embedded virtual filesystem engine for exposing host application capabilities and Agent Skills as ordinary files.

Current status: M0/M1 bootstrap. The repository contains a Go core framework, initial tests, benchmark entry points, and development documents. FUSE, WebDAV, and host bindings are intentionally staged behind the core semantics.

## Quick Start

```sh
go test ./...
go test ./bench -bench .
```

## Design Documents

- [Development handoff](docs/DEVELOPMENT_HANDOFF.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Testing](docs/TESTING.md)
- [Milestones](docs/MILESTONES.md)
