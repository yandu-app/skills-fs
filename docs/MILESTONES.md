# Milestones

## M0 Bootstrap

- Go module exists.
- Core package compiles.
- Documentation captures architecture and testing gates.
- Unit tests and benchmarks exist for the core surface.

Status: complete.

## M1 Core Semantics

- Mount, unmount, stat, read, write, readdir.
- Blob, API, dir, and link semantics in core.
- Provider dispatch with path params.
- Skill generation.

Status: mostly complete in pure Go core. Remaining adapter-facing work: full stream behavior, richer dir mutation, and generated `/skills` namespace projection.

## M2 Concurrency and Handles

- Handle manager.
- Advisory flock.
- Serial API write queue.
- Immediate and buffered write policies.

Status: initial core implementation complete with race tests. Remaining work: benchmark lock contention and refine timed deadlock detection.

## M3 Adapters

- Linux/macOS FUSE adapter.
- File event notification.
- WebDAV fallback.
- Windows WinFsp adapter.

## M4 Host Bindings

- Node N-API binding.
- IPC provider bridge.
- Lifecycle cleanup hooks.
