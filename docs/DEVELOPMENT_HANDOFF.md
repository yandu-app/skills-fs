# skills-fs Development Handoff

Version: 1.0.0-rc2

This repository is bootstrapped from the Chinese product and engineering specification in the project kickoff. The project is an embedded virtual filesystem engine written in Go. Host applications register minimal Providers, while Go owns path routing, permissions, handle state, buffering, locking, FUSE/WebDAV adapters, and Agent Skill generation.

## Milestone Policy

The implementation is split into strict milestones:

1. Core semantics: routing, mount registry, provider dispatch, permissions, error mapping, stat metadata, skill generation, **dynamic directories, and `AGENTS.md` validation**.
2. Concurrency semantics: handle manager, flock-style locks, serial API write queue, write buffering, stream ring buffers.
3. Adapter semantics: FUSE adapter, file event notification, WebDAV fallback.
4. Host bindings: N-API/FFI and IPC provider transport.
5. Cross-platform validation and benchmark gates.

No adapter may bypass the core package for permission checks, path matching, lock acquisition, buffering, stream backpressure, or Provider error mapping.

## Configuration Policy

- `skills-fs` supports a central JSON config plus per-Skill `includes` fragments so multiple skills can coexist without conflicting over a single global file.
- All string fields in the JSON config support `$VAR` and `${VAR}` environment-variable expansion, making paths and URLs portable across machines.
- Every `dir` and `dynamic_dir` mount must include a child `AGENTS.md` blob explaining the directory's purpose. Opt-out is allowed with `"agents": false` on the mount.

## P0 Acceptance Gates

- `go test ./...` must pass.
- `make coverage` (core packages) must exceed 85% statement coverage.
- `make race` must pass.
- Core route, permission, and error mapping packages must keep focused tests.
- **Dynamic directories and `AGENTS.md` validation must be covered by focused tests and documented in all user-facing docs.**
- Benchmarks in `bench/` must exist for every performance requirement.
- Public APIs must avoid global mutable state.
- New adapter code must be covered by platform-specific integration tests or build tags.

## Explicit Non-Goals

The project does not implement mmap, hard links, fcntl record locks, full POSIX ACLs, embedded language runtimes, implicit network listeners, or cross-mount atomic transactions.
