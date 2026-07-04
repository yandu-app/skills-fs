# Architecture

`skills-fs` exposes host application capabilities as a filesystem-shaped namespace.

```
Agent tools -> FUSE/WebDAV/WebSocket/Bindings -> core.FileSystem -> Provider.Invoke
```

## Core Ownership

The Go core owns:

- Mount registration and route conflict checks, including dynamic directories (`KindDynamicDir`) whose children are provided at runtime by an HTTP/WebSocket provider.
- Path parameter extraction (zero-allocation inline array).
- Unix-style mode checks.
- Provider dispatch and POSIX error mapping.
- Generated Agent Skill directories, `SKILL.md`, and optional `AGENTS.md` guides.
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
- `adapter/webdav`: Full WebDAV server with GET, PUT, PROPFIND, COPY, MOVE, LOCK, and streaming binary support.
- `adapter/websocket`: WebSocket streaming adapter with JSON protocol, binary messages, per-message deflate, and event subscriptions.
- `adapter/middleware`: Shared HTTP middleware for request IDs, CORS, rate limiting, connection limiting, gzip, and size limits.
- `provider/cache`, `provider/http`, `provider/ipc`, `provider/local`: built-in provider implementations.
- `binding/go-bridge`: cgo shared library that exposes core.FileSystem through a C ABI for language bindings.
- `binding/registry`: handle-to-FS registry and per-handle last-error storage used by the C bridge.
- `binding/nodejs`: Node.js N-API wrapper consuming `libgobridge.so`.
- `binding/python`: Python ctypes wrapper consuming `libgobridge.so`.
- `cmd/skills-fs`: CLI daemon with config reload, PID file, graceful shutdown, config `includes`, environment-variable expansion, and validation that every directory mount exposes an `AGENTS.md` guide.
- `bench`: required benchmark entry points.
- `docs`: handoff, architecture, testing, and milestone documents.
