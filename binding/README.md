# Host language bindings

The `binding/` tree exposes the in-process `core.FileSystem` to other
languages by way of a C shared library generated from Go. Implemented
bindings are Node.js (N-API) and Python (ctypes). The same
`libgobridge.so` is the intended ABI for Ruby (FFI), Rust, or any other
runtime that can speak C.

```
       Node.js  Python  Ruby ...
           \      |       /
            \     |      /
             +----+-----+
             |  libgobridge.so
             |  (cgo, c-shared)
             +----+-----+
                  |
              core.FileSystem
                  (Go)
```

## Layout

```
binding/
├── go-bridge/        cgo c-shared package; emits libgobridge.{so,h}
│   └── bridge.go
├── nodejs/           node-gyp project that wraps the C ABI as N-API
│   ├── binding.c     N-API → libgobridge translation
│   ├── binding.gyp   build config
│   ├── index.js      ergonomic JS wrapper
│   ├── test.js       smoke test
│   └── package.json
└── python/           ctypes module that loads the same C ABI
    ├── skills_fs.py  Python wrapper
    └── test_skills_fs.py
```

## C ABI

All exported symbols live in `libgobridge.so` and are declared in the
generated header `libgobridge.h`:

| Symbol | Purpose |
|--------|---------|
| `skills_fs_create` | allocate a `core.FileSystem` and return an opaque `uintptr_t` handle |
| `skills_fs_shutdown` | tear down a filesystem; idempotent |
| `skills_fs_mount_blob` | mount a writable blob at a path with a unix mode |
| `skills_fs_mount_api` | mount an API node backed by a provider+action pair |
| `skills_fs_read` | read a blob/api node; caller frees the returned buffer with `skills_fs_free` |
| `skills_fs_write` | write bytes to a blob/api node |
| `skills_fs_free` | free a buffer returned by `skills_fs_read` |

Handles are stored in a process-global registry guarded by a mutex. A
caller that loses its handle leaks the filesystem until process exit,
mirroring the contract of an opaque OS resource.

## Building the Node binding

From the repo root:

```sh
make binding-node
```

This is a two-stage build:

1. `go build -buildmode=c-shared -o binding/nodejs/lib/libgobridge.so ./binding/go-bridge`
   emits the shared library and its header into `binding/nodejs/lib/`.
2. `cd binding/nodejs && npm install && npm run build` runs `node-gyp`
   which compiles `binding.c` against the header and links against the
   shared library with an `$ORIGIN`-relative rpath so the addon finds
   `libgobridge.so` at runtime without `LD_LIBRARY_PATH`.

After a successful build, run the smoke test:

```sh
cd binding/nodejs && npm test
```

## Usage from Node

```js
const { FileSystem } = require('./index');

const fs = new FileSystem();
fs.mountBlob('/greeting.txt');
fs.write('/greeting.txt', Buffer.from('hello'));
console.log(fs.read('/greeting.txt').toString()); // hello
fs.shutdown();
```

## Usage from Python

From the repo root:

```sh
make binding-python
cd binding/python && python3 test_skills_fs.py
```

```python
from skills_fs import FileSystem

fs = FileSystem()
fs.mount_blob("/greeting.txt")
fs.write("/greeting.txt", b"hello")
print(fs.read("/greeting.txt"))  # b'hello'
fs.shutdown()
```

## Caveats

- Linux/x86_64 only for now; the rpath strategy needs a `@loader_path`
  variant on macOS and a different DLL search policy on Windows.
- All FFI calls block the calling thread; long-running provider work
  should be moved to a worker thread.
- Errors from Go are surfaced as non-zero `int` return codes; richer
  error metadata is on the TODO list.
