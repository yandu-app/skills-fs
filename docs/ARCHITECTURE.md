# Architecture

`skills-fs` exposes host application capabilities as a filesystem-shaped namespace.

```
Agent tools -> FUSE/WebDAV/Bindings -> core.FileSystem -> Provider.Invoke
```

## Core Ownership

The Go core owns:

- Mount registration and route conflict checks.
- Path parameter extraction.
- Unix-style mode checks.
- Provider dispatch and POSIX error mapping.
- Generated Agent Skill directories.
- Future handle, lock, buffering, stream, stat cache, and observability state.

Host bindings own only:

- Translating host callbacks into the `Provider` interface.
- Starting and stopping adapters.
- Passing caller identity from the OS or host token mapping.

## Package Layout

- `core`: public embedded filesystem API and in-memory semantics.
- `bench`: required benchmark entry points.
- `docs`: handoff, architecture, testing, and milestone documents.

Adapter packages will be added under `adapter/fuse`, `adapter/webdav`, and `binding/node` once the core contracts are stable.
