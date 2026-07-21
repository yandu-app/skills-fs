# skills-fs

Virtual filesystem engine for exposing application capabilities and Agent
Skills as files.

[中文](README.zh.md)

---

skills-fs is a Go library and CLI that turns application capabilities into a
virtual filesystem. Read a file to query data. Write to a file to perform an
action. List a directory to discover what's available. Every filesystem
operation is dispatched to a provider — local code, a remote HTTP endpoint,
or a stream — through a radix-tree router with POSIX semantics.

---

## What It Does

AI agents interact best with flat, discoverable interfaces. A filesystem is
that interface: `cat` to read, `>` to write, `ls` to explore. skills-fs makes
any application capability — a bot API, a database query, a live event stream
— available through these primitives, so agents need only filesystem knowledge
to operate.

Example: NapCat QQ Bot → filesystem

```bash
cat napcat/events           # read recent messages
echo '{"group_id":123,"message":"hello"}' > napcat/send_group  # send message
cat napcat/status           # check bot status
```

---

## Overview

### Mount Kinds

Each node in the virtual filesystem is a mount entry with a kind:

| Kind | Description | Example |
|------|-------------|---------|
| Blob | Static file with inline content. Read-only. | `/SKILL.md`, `/AGENTS.md` |
| API | Content produced by provider `read` action; writes forwarded as provider `write` action. Optional JSON payload forwarding (`writeParams: "json"`). | `napcat/send_group`, `napcat/events` |
| Dir | Static directory containing nested mounts. | `/napcat/`, `/napcat/groups/` |
| DynamicDir | Provider-backed directory. On `readdir`, invokes a provider action that returns JSON entries. Entries matched against registered mounts to determine kind. | `groups/{group_id}/`, `friends/{user_id}/` |
| Stream | Bounded ring buffer. Supports `block`, `drop`, or `error` backpressure. Multiple handles share one buffer (FIFO semantics). | `napcat/events`, `napcat/alerts` |
| Link | Symbolic link to another mount path. | `/my-skill -> /skills/real-skill` |

### Key Features

- Radix-tree router: fast prefix matching for path resolution.
- Sharded handles: 16-map sharding for concurrent open/read/write.
- Advisory flock: per-path shared/exclusive locks, auto-released on close.
- Serial queue: per-mount serialization prevents race conditions while allowing cross-path concurrency.
- Write buffering: payloads coalesced until size threshold, delay, or newline triggers flush.
- Event bus: create/write/remove broadcast with path prefix filtering.
- Provider cache: TTL-based `(action, params)` cache per mount.
- Prometheus metrics: `/metrics` endpoint on WebDAV and WebSocket servers.

---

## Adapters

Three transport layers let skills-fs reach any client:

- FUSE (Linux) — native mount via `go-fuse/v2`. Includes inotify forwarding.
- WebDAV — full HTTP server with Basic Auth, TLS, gzip, CORS, rate limiting, ETags, Range requests, property caching.
- WebSocket — streaming operations, JSON/binary messages, per-message deflate (RFC 7692), subscription IDs for event watching.

---

## Providers

- HTTP provider — forwards `Invoke` calls to a remote HTTP endpoint as
  JSON POST requests (`{ "action": "...", "params": {...} }`). Configurable
  retry with exponential backoff and jitter; circuit breaker on consecutive
  failures.

- Local provider — executes in-process Go functions.

---

## Host Bindings

skills-fs compiles to a C shared library (`libgobridge.so`) via Go's cgo
export mechanism. Callers use dense, monotonic `uintptr` handles — no pointer
management required.

| Binding | Technology | Interface |
|---------|-----------|-----------|
| Python | ctypes | `skills_fs.py` — object-oriented wrapper |
| Node.js | N-API (node-addon-api) | `index.js` — async methods, `path()` calls |
| Go | Direct import | `github.com/skills-fs/skills-fs/core` |

---

## Skills System

A Skill is a declarative bundle that generates filesystem mounts from a
template. Skills define their capabilities, documentation, and agent
guidance — then skills-fs generates the actual mount structure at runtime.

```json
{
  "name": "napcat-cli",
  "description": "NapCat QQ bot messaging",
  "bodyTemplate": "# NapCat CLI Skill\n\nAccess QQ bot via filesystem...",
  "agentsTemplate": "# Agent Guide\n\n## Daemon requirements...",
  "exposeAtRoot": true,
  "allowedTools": ["read_file", "write_file", "list_directory"]
}
```

The Skill generator writes a `SKILL.md` to disk (YAML frontmatter + template
body) and optionally an `AGENTS.md` with agent-specific guidance. When
`exposeAtRoot` is true, these files are also mounted at `/SKILL.md` and
`/AGENTS.md` in the virtual filesystem.

### Example: NapCat CLI Integration

The [napcat-cli](https://github.com/cyjin-yl/napcat-cli) project demonstrates
a complete skills-fs integration:

1. Watch daemon (`daemon/watch.py`) connects to NapCat's WebSocket,
   writes events to disk, and runs an HTTP server implementing the
   skills-fs provider contract.

2. skills-fs config (`skills-fs-config.json`) declares the provider
   URL (`http://127.0.0.1:18821/invoke`) and includes a fragment file
   (`skills-fs-fragment.json`) with the full mount tree for napcat-cli.

3. Fragment file defines dozens of mounts: `napcat/send_group`,
   `napcat/events`, `napcat/groups/{group_id}/{time_range}/{message_id}`, etc.
   Dynamic directories let agents browse message history by group and time range.

4. FUSE mount makes all paths available as real filesystem paths to
   the AI agent, which interacts using only `cat`, `ls`, and `echo >`.

---

## Quick Start

### Go Library

```go
import "github.com/skills-fs/skills-fs/core"

fs, _ := core.NewFileSystem(core.Config{
    MaxOpenHandles: 1024,
    DefaultUID:     1000,
    DefaultGID:     1000,
})
fs.Mount(core.MountEntry{
    Path: "/hello",
    Kind: core.KindBlob,
    Mode: 0o644,
    BlobData: []byte("world"),
})
data, _ := fs.OpenRead("/hello")
```

### CLI

```bash
go run ./cmd/skills-fs webdav -addr :8080
go run ./cmd/skills-fs websocket -addr :8081
go run ./cmd/skills-fs fuse -mountpoint /tmp/skills-fs
go run ./cmd/skills-fs webdav -config config.json
go run ./cmd/skills-fs validate -config config.json
```

### Python

```python
from skills_fs import SkillsFs
import json

cfg = json.loads(open("config.json").read())
fs = SkillsFs(cfg)
print(fs.read("/hello"))  # b"world"
```

### Node.js

```javascript
const SkillsFs = require("skills-fs");

const fs = new SkillsFs(require("./config.json"));
console.log(fs.read("/hello").toString()); // "world"
```

---

## Configuration

The config file is JSON with these top-level keys:

| Key | Description |
|-----|-------------|
| `providers` | Array of provider definitions (id, url). |
| `mounts` | Array of mount entries (path, kind, mode, read/write actions, provider). |
| `skills` | Array of skill definitions. |
| `skillsRoot` | Directory where skill `SKILL.md` files are generated. |
| `includes` | Additional config files to merge (resolved relative to parent). |
| `defaultUID` / `defaultGID` | Default ownership for generated mounts. |
| `maxOpenHandles` | Handle budget (default: 1024). |
| `lockTimeout` | Advisory lock timeout (default: 30s). |
| `serialQueue` | Per-mount serial queue size (default: 1). |

### Config Includes

Multiple skills can share one skills-fs instance without a single global
config. The `includes` array loads and merges additional config files:

```json
{
  "skillsRoot": "$HOME/.skills",
  "providers": [{"id": "napcat", "url": "http://127.0.0.1:18821/invoke"}],
  "includes": ["skills-fs.d/napcat-cli.json"]
}
```

### Signals

- `SIGINT` / `SIGTERM`: graceful shutdown
- `SIGHUP`: reload configuration file (webdav / websocket commands)

### Metrics

Prometheus text format at `/metrics` on WebDAV and WebSocket servers.

---

## Development

```bash
make all            # lint + test + vulncheck
make quick          # fmt + vet + core/registry/provider tests (fast)
make ci             # fmt + lint + test + coverage + race + vulncheck + bench (full)
make lint           # go vet + staticcheck
make test           # run all tests
make race           # core tests with race detector
make coverage       # check core coverage against 85% gate
make vulncheck      # scan dependencies for vulnerabilities
make bench          # run benchmarks
make bench-gate     # compare benchmarks against baseline (benchstat)
make gen-docs       # regenerate API reference docs
make binding-node   # build Node.js N-API addon
make binding-python # build Python ctypes module
make clean          # remove build artifacts
```

- `core` package: >91% statement coverage; total >85%.
- Fuzz tests for router and path normalization.
- Benchmarks: path resolution, stat, write, lock contention, serial queue,
  event emit, stream read/write, handle open/close, HTTP provider roundtrip.

---

## Documentation

### API Reference

Generated from source. Located in [`docs/api/`](docs/api/):

- [core](docs/api/core.md) — FileSystem, MountEntry, Handle, Config, events, locks, streams, metrics, skills
- [adapter](docs/api/adapter.md) — MountOptions, adapter interface
- [adapter/fuse](docs/api/adapter_fuse.md)
- [adapter/webdav](docs/api/adapter_webdav.md)
- [adapter/websocket](docs/api/adapter_websocket.md)
- [provider/http](docs/api/provider_http.md)
- [provider/local](docs/api/provider_local.md)
- [provider/cache](docs/api/provider_cache.md)

### Design Documents

- [Development handoff](docs/DEVELOPMENT_HANDOFF.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Testing](docs/TESTING.md)
- [Milestones](docs/MILESTONES.md)
