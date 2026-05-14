# Milestones

## M0 Bootstrap

- Go module exists.
- Core package compiles.
- Documentation captures architecture and testing gates.
- Unit tests and benchmarks exist for the core surface.

Status: complete.

## M1 Core Semantics

- Mount, unmount, stat, read, write, readdir.
- Blob, API, dir, link, and stream semantics in core.
- Provider dispatch with path params.
- Skill generation with virtual `/skills` namespace projection.

Status: complete.

## M2 Concurrency and Handles

- Handle manager with sharded maps and MaxOpenHandles budget.
- Advisory flock (shared/exclusive) with timeout and ctx cancellation.
- Serial API write queue.
- Immediate and buffered write policies (size, delay, newline triggers).
- Stream pull/push with ring buffer and configurable backpressure (block, drop, error).

Status: complete.

## M3 Adapters

- Linux FUSE adapter using `github.com/hanwen/go-fuse/v2` with dynamic Lookup, Getattr, Readdir, Open, Read, Write, Flush, Release.
- FUSE inotify forwarding via kernel cache invalidation (NotifyContent / NotifyEntry).
- Build-tagged stub for non-Linux platforms.
- fs.Notify event API (Create, Write, Remove) with multi-listener support.
- WebDAV fallback (stub).

Status: FUSE and event API complete. WebDAV stub pending M4+ prioritization.

## M4 Host Bindings

- Node N-API binding.
- IPC provider bridge.
- Lifecycle cleanup hooks.

Status: not started.
