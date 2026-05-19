// Package main builds as a C shared library that exposes a minimal C API
// around skills-fs core.FileSystem. It is consumed by language bindings
// (Node N-API, Python ctypes, etc.).
//
// All exported functions are thin glue: they translate C types to Go
// types and delegate to the binding/registry and core packages.
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
	"unsafe"

	"github.com/skills-fs/skills-fs/binding/registry"
	"github.com/skills-fs/skills-fs/core"
)

var reg = registry.New()

//export skills_fs_create
func skills_fs_create() C.uintptr_t {
	return C.uintptr_t(reg.Register(core.NewFS(core.GlobalConfig{})))
}

//export skills_fs_shutdown
func skills_fs_shutdown(handle C.uintptr_t) {
	if fs, ok := reg.Unregister(uintptr(handle)); ok {
		_ = fs.Shutdown(context.Background())
	}
}

//export skills_fs_mount_blob
func skills_fs_mount_blob(handle C.uintptr_t, path *C.char, mode C.uint) C.int {
	fs, ok := reg.Get(uintptr(handle))
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
	fs, ok := reg.Get(uintptr(handle))
	if !ok {
		return -1
	}
	entry := core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o644,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: C.GoString(providerID), Action: C.GoString(action)},
		},
	}
	if err := fs.Mount(C.GoString(path), entry); err != nil {
		return -1
	}
	return 0
}

//export skills_fs_read
func skills_fs_read(handle C.uintptr_t, path *C.char, outLen *C.int) *C.char {
	fs, ok := reg.Get(uintptr(handle))
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
func skills_fs_write(handle C.uintptr_t, path *C.char, data *C.char, length C.int) C.int {
	fs, ok := reg.Get(uintptr(handle))
	if !ok {
		return -1
	}
	payload := C.GoBytes(unsafe.Pointer(data), length)
	if err := fs.Write(context.Background(), C.GoString(path), payload, core.CallerIdentity{}); err != nil {
		return -1
	}
	return 0
}

//export skills_fs_free
func skills_fs_free(p unsafe.Pointer) {
	C.free(p)
}

func main() {}
