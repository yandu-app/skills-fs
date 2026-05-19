// Package registry maps opaque uintptr handles to *core.FileSystem
// instances. It is the storage backend that the C ABI bridge uses to
// hand JavaScript / Python / etc. callers a stable identifier they can
// pass back across the FFI boundary.
//
// The registry has no knowledge of FFI or cgo, so its behavior is
// exercised entirely from regular `go test`.
package registry

import (
	"sync"

	"github.com/skills-fs/skills-fs/core"
)

// Registry is a thread-safe mapping of uintptr handles to *core.FileSystem.
//
// Handles are dense, monotonically increasing, and start at 1 — zero is
// reserved so callers may use it as an "invalid handle" sentinel.
type Registry struct {
	mu      sync.Mutex
	counter uintptr
	fs      map[uintptr]*core.FileSystem
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{fs: make(map[uintptr]*core.FileSystem)}
}

// Register adds fs to the registry and returns its handle. The caller
// retains ownership of fs; the registry only stores the pointer.
func (r *Registry) Register(fs *core.FileSystem) uintptr {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	h := r.counter
	r.fs[h] = fs
	return h
}

// Get looks up the FileSystem associated with handle. The second
// return is false if the handle has been unregistered or never existed.
func (r *Registry) Get(handle uintptr) (*core.FileSystem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fs, ok := r.fs[handle]
	return fs, ok
}

// Unregister removes the handle from the registry and returns the
// FileSystem it was bound to. The second return is false if the handle
// was unknown. Unregister does NOT call Shutdown on the FileSystem;
// that is the caller's responsibility.
func (r *Registry) Unregister(handle uintptr) (*core.FileSystem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fs, ok := r.fs[handle]
	if !ok {
		return nil, false
	}
	delete(r.fs, handle)
	return fs, true
}

// Len returns the number of registered handles. Exposed primarily for
// tests and diagnostics.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fs)
}
