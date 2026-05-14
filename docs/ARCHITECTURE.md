# Architecture

`skills-fs` exposes host application capabilities as a filesystem-shaped namespace.

```
Agent tools -> FUSE/WebDAV/Bindings -> core.FileSystem -> Provider.Invoke
```

## Core Ownership

The Go core owns:

- Mount registration and route conflict checks.
- Path parameter extraction (zero-allocation inline array).
- Unix-style mode checks.
- Provider dispatch and POSIX error mapping.
- Generated Agent Skill directories.
- Handle lifecycle, advisory locking, write buffering, stream ring buffers.
- Event notification bus.
- Prometheus-compatible metrics at `/sys/metrics`.

Host bindings own only:

- Translating host callbacks into the `Provider` interface.
- Starting and stopping adapters.
- Passing caller identity from the OS or host token mapping.

## Package Layout

- `core`: public embedded filesystem API and in-memory semantics.
- `adapter`: adapter contracts (`MountedFS`, `MountOptions`, `Factory`) shared by FUSE and WebDAV.
- `adapter/fuse`: Linux FUSE implementation using `go-fuse/v2`. Non-Linux platforms compile a stub returning `ErrNotImplemented`.
- `adapter/webdav`: WebDAV fallback package boundary. Currently a no-listener stub.
- `bench`: required benchmark entry points.
- `docs`: handoff, architecture, testing, and milestone documents.
