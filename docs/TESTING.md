# Testing and Benchmark Requirements

## Unit Tests

Core tests must cover:

- Route priority: exact match before parameter match.
- Path parameter extraction.
- Mount conflict detection.
- Read/write permission checks.
- Provider error string to POSIX error mapping.
- Skill generation and cleanup, including `AGENTS.md` generation when `AgentsTemplate` is set.
- Dynamic directory routing, readdir, getattr, and open/read paths.
- Handle manager budget enforcement and sharded lookups.
- Advisory flock shared/exclusive transitions and close release.
- Buffered write flush on size, delay, and newline.
- Stream read/write with block, drop, and error backpressure.
- Event bus emit and multi-listener delivery.
- CLI config validation, including `includes` loading and `AGENTS.md` presence checks.

Run:

```sh
make test
make coverage
make race
```

`make coverage` measures `core` and `bench` packages (excluding adapter code that requires platform drivers) and fails if statement coverage drops below 85%.

`make coverage-all` measures the entire repository including adapters.

## Benchmarks

Benchmarks are tracked in `bench/`:

- `BenchmarkPathResolveStatic`: target `< 100 ns/op`, `0 allocs/op`.
- `BenchmarkPathResolveParam`: target `< 200 ns/op`.
- `BenchmarkStatCacheHit`: target `< 200 ns/op`.
- `BenchmarkWriteImmediate`: target `< 500 ns/op`, excluding provider work.
- `BenchmarkSerialQueue`: throughput under parallel API writes.
- `BenchmarkLockAcquire`: uncontended lock latency.
- `BenchmarkLockContention1000`: shared lock scaling with 1000 handles.
- `BenchmarkStreamWriteDrop`: write throughput to a drop-mode stream buffer.
- `BenchmarkStreamReadWrite`: paired reader/writer through a block-mode stream.
- `BenchmarkEventEmit`: event delivery to 1, 4, and 16 registered listeners.

Run:

```sh
make bench
```

Current baseline on Linux amd64:

- `BenchmarkPathResolveStatic`: about `80 ns/op`, `0 allocs/op`.
- `BenchmarkPathResolveParam`: about `92 ns/op`.
- `BenchmarkStatCacheHit`: about `140 ns/op`, `0 allocs/op`.
- `BenchmarkWriteImmediate`: about `210 ns/op`, `0 allocs/op` for blob overwrite core path.
- `BenchmarkSkillGenerate`: about `6.4 ms/op`.
- `BenchmarkStreamWriteDrop`: about `350 ns/op`, `0 allocs/op`.
- `BenchmarkStreamReadWrite`: about `220 ns/op`, `1 B/op`, `0 allocs/op`.
- `BenchmarkEventEmit/listeners=1`: about `730 ns/op`, `7 allocs/op`.

## Integration Tests

FUSE tests must be opt-in and platform tagged because they require system drivers and mount privileges. They must verify real `ls`, `cat`, `grep`, `find`, and native watch events.

Dynamic-directory integration tests should mount a test FUSE filesystem and verify that provider-backed entries are rendered as directories, that path parameters are passed to provider actions, and that `AGENTS.md` blobs appear in directory listings.

## Validation Tests

`skills-fs validate` should be tested against configs that:
- Load and merge `includes` fragments.
- Expand environment variables in paths and URLs.
- Fail when a `dir` or `dynamic_dir` mount lacks a child `AGENTS.md` mount.
- Pass when `"agents": false` is set to opt out.

## Binding Tests

Node N-API and Python ctypes bindings are tested against `libgobridge.so`:

```sh
make binding-test
```

Tests verify round-trip mount/read/write, error propagation, and lifecycle semantics.

## CI Pipeline

The full pipeline is simulated locally with:

```sh
make ci
```

This runs: fmt-check, lint, test, coverage gate (85%), race detector, vulnerability scan, and benchmark smoke.

To run FUSE integration tests on Linux:

```sh
go test -tags fuse_integration ./adapter/fuse
```
