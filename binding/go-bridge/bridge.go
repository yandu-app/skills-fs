// Package main builds as a C shared library that exposes a minimal C API
// around skills-fs core.FileSystem. It is consumed by language bindings
// (Node N-API, Python ctypes, etc.).
//
// Build with:
//
//	go build -buildmode=c-shared -o libgobridge.so
package main

/*
#include <stdint.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"sync"
	"unsafe"

	"github.com/skills-fs/skills-fs/core"
)

var (
	fsRegistry = make(map[uintptr]*core.FileSystem)
	fsCounter  uintptr
	fsMu       sync.Mutex
)

//export skills_fs_create
func skills_fs_create() C.uintptr_t {
	fsMu.Lock()
	defer fsMu.Unlock()
	fsCounter++
	id := fsCounter
	fsRegistry[id] = core.NewFS(core.GlobalConfig{})
	return C.uintptr_t(id)
}

//export skills_fs_shutdown
func skills_fs_shutdown(handle C.uintptr_t) {
	fsMu.Lock()
	defer fsMu.Unlock()
	if fs, ok := fsRegistry[uintptr(handle)]; ok {
		_ = fs.Shutdown(context.Background())
		delete(fsRegistry, uintptr(handle))
	}
}

//export skills_fs_mount_blob
func skills_fs_mount_blob(handle C.uintptr_t, path *C.char, mode C.uint) C.int {
	fsMu.Lock()
	fs, ok := fsRegistry[uintptr(handle)]
	fsMu.Unlock()
	if !ok {
		return -1
	}
	if err := fs.Mount(C.GoString(path), core.MountEntry{Kind: core.KindBlob, Mode: uint32(mode)}); err != nil {
		return -1
	}
	return 0
}

//export skills_fs_mount_api
func skills_fs_mount_api(handle C.uintptr_t, path *C.char, providerID *C.char, action *C.char) C.int {
	fsMu.Lock()
	fs, ok := fsRegistry[uintptr(handle)]
	fsMu.Unlock()
	if !ok {
		return -1
	}
	if err := fs.Mount(C.GoString(path), core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o644,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: C.GoString(providerID), Action: C.GoString(action)},
		},
	}); err != nil {
		return -1
	}
	return 0
}

//export skills_fs_read
func skills_fs_read(handle C.uintptr_t, path *C.char, outLen *C.int) *C.char {
	fsMu.Lock()
	fs, ok := fsRegistry[uintptr(handle)]
	fsMu.Unlock()
	if !ok {
		return nil
	}
	data, err := fs.Read(context.Background(), C.GoString(path), core.CallerIdentity{})
	if err != nil {
		return nil
	}
	*outLen = C.int(len(data))
	return (*C.char)(C.CBytes(data))
}

//export skills_fs_write
func skills_fs_write(handle C.uintptr_t, path *C.char, data *C.char, len C.int) C.int {
	fsMu.Lock()
	fs, ok := fsRegistry[uintptr(handle)]
	fsMu.Unlock()
	if !ok {
		return -1
	}
	b := C.GoBytes(unsafe.Pointer(data), len)
	if err := fs.Write(context.Background(), C.GoString(path), b, core.CallerIdentity{}); err != nil {
		return -1
	}
	return 0
}

//export skills_fs_free
func skills_fs_free(p unsafe.Pointer) {
	C.free(p)
}

func main() {}
