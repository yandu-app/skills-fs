# Testing and Benchmark Requirements

## Unit Tests

Core tests must cover:

- Route priority: exact match before parameter match.
- Path parameter extraction.
- Mount conflict detection.
- Read/write permission checks.
- Provider error string to POSIX error mapping.
- Skill generation and cleanup.
- Handle manager budget enforcement and sharded lookups.
- Advisory flock shared/exclusive transitions and close release.
- Buffered write flush on size, delay, and newline.
- Stream read/write with block, drop, and error backpressure.
- Event bus emit and multi-listener delivery.

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

Run:

```sh
make bench
```

Current baseline on Linux amd64:

- `BenchmarkPathResolveStatic`: about `56 ns/op`, `0 allocs/op`.
- `BenchmarkPathResolveParam`: about `169 ns/op`.
- `BenchmarkStatCacheHit`: about `23 ns/op`, `0 allocs/op`.
- `BenchmarkWriteImmediate`: about `34 ns/op`, `0 allocs/op` for blob overwrite core path.
- `BenchmarkSkillGenerate`: about `6.3 ms/op`.

## Integration Tests

FUSE tests must be opt-in and platform tagged because they require system drivers and mount privileges. They must verify real `ls`, `cat`, `grep`, `find`, and native watch events.

To run FUSE integration tests on Linux:

```sh
go test -tags fuse_integration ./adapter/fuse
```
