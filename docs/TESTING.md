# Testing and Benchmark Requirements

## Unit Tests

Core tests must cover:

- Route priority: exact match before parameter match.
- Path parameter extraction.
- Mount conflict detection.
- Read/write permission checks.
- Provider error string to POSIX error mapping.
- Skill generation and cleanup.

Run:

```sh
make test
make coverage
make race
```

`make coverage` fails if total repository statement coverage drops below 85%.

## Benchmarks

Benchmarks are tracked in `bench/`:

- `BenchmarkPathResolveStatic`: target `< 100 ns/op`, `0 allocs/op`.
- `BenchmarkPathResolveParam`: target `< 200 ns/op`.
- `BenchmarkStatCacheHit`: target `< 200 ns/op`.
- `BenchmarkWriteImmediate`: target `< 500 ns/op`, excluding provider work.
- Additional placeholder benchmarks document future FUSE, lock, stream, and skill gates.

Run:

```sh
make bench
```

Current M0 baseline on Linux amd64:

- `BenchmarkPathResolveStatic`: about `56 ns/op`, `0 allocs/op`.
- `BenchmarkPathResolveParam`: about `169 ns/op`.
- `BenchmarkStatCacheHit`: about `23 ns/op`, `0 allocs/op`.
- `BenchmarkWriteImmediate`: about `34 ns/op`, `0 allocs/op` for blob overwrite core path.
- `BenchmarkSkillGenerate`: about `6.3 ms/op`.

## Integration Tests

FUSE tests must be opt-in and platform tagged because they require system drivers and mount privileges. They must verify real `ls`, `cat`, `grep`, `find`, and native watch events.
