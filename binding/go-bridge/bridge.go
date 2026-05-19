// Package main builds as a C shared library that exposes a minimal C API
// around skills-fs core.FileSystem. It is consumed by language bindings
// (Node N-API, Python ctypes, etc.).
//
// All exported functions are thin glue: they translate C types to Go
// types and delegate to the binding/registry and core packages.
//
// Error propagation: success returns 0 (or a non-nil buffer for
// read-style ops). Failure returns -1 (or nil) AND stores the error
// message in the registry's per-handle slot. Hosts retrieve it via
// skills_fs_last_error.
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
	"encoding/json"
	"unsafe"

	"github.com/skills-fs/skills-fs/binding/registry"
	"github.com/skills-fs/skills-fs/core"
)

var reg = registry.New()

const errUnknownHandle = "unknown filesystem handle"

// statDTO and dirEntryDTO are the cross-FFI shapes for filesystem
// metadata. They are local to the binding so core types are free to
// evolve without breaking JS/Python clients.
type statDTO struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Mode uint32 `json:"mode"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	Size int64  `json:"size"`
}

type dirEntryDTO struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Mode uint32 `json:"mode"`
}

// resolveFS looks up the handle and records "unknown handle" if missing.
// The second return is false in that case so callers can bail out.
func resolveFS(h uintptr) (*core.FileSystem, bool) {
	fs, ok := reg.Get(h)
	if !ok {
		reg.SetError(h, errUnknownHandle)
		return nil, false
	}
	return fs, true
}

func fail(h uintptr, err error) {
	reg.SetError(h, err.Error())
}

func clear(h uintptr) {
	reg.SetError(h, "")
}

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

//export skills_fs_last_error
func skills_fs_last_error(handle C.uintptr_t) *C.char {
	msg := reg.LastError(uintptr(handle))
	if msg == "" {
		return nil
	}
	return C.CString(msg)
}

//export skills_fs_mount_blob
func skills_fs_mount_blob(handle C.uintptr_t, path *C.char, mode C.uint) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return -1
	}
	if err := fs.Mount(C.GoString(path), core.MountEntry{Kind: core.KindBlob, Mode: uint32(mode)}); err != nil {
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

//export skills_fs_mount_api
func skills_fs_mount_api(handle C.uintptr_t, path *C.char, providerID *C.char, action *C.char) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
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
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

//export skills_fs_unmount
func skills_fs_unmount(handle C.uintptr_t, path *C.char) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return -1
	}
	if err := fs.Unmount(C.GoString(path)); err != nil {
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

//export skills_fs_rename
func skills_fs_rename(handle C.uintptr_t, oldPath *C.char, newPath *C.char) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return -1
	}
	if err := fs.Rename(C.GoString(oldPath), C.GoString(newPath)); err != nil {
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

//export skills_fs_stat
func skills_fs_stat(handle C.uintptr_t, path *C.char, outLen *C.int) *C.char {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return nil
	}
	st, err := fs.Stat(C.GoString(path), core.CallerIdentity{})
	if err != nil {
		fail(h, err)
		return nil
	}
	payload, err := json.Marshal(statDTO{
		Path: st.Path,
		Kind: string(st.Kind),
		Mode: st.Mode,
		UID:  st.UID,
		GID:  st.GID,
		Size: st.Size,
	})
	if err != nil {
		fail(h, err)
		return nil
	}
	clear(h)
	*outLen = C.int(len(payload))
	return (*C.char)(C.CBytes(payload))
}

//export skills_fs_readdir
func skills_fs_readdir(handle C.uintptr_t, path *C.char, outLen *C.int) *C.char {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return nil
	}
	entries, err := fs.Readdir(C.GoString(path), core.CallerIdentity{})
	if err != nil {
		fail(h, err)
		return nil
	}
	dtos := make([]dirEntryDTO, len(entries))
	for i, e := range entries {
		dtos[i] = dirEntryDTO{Name: e.Name, Kind: string(e.Kind), Mode: e.Mode}
	}
	payload, err := json.Marshal(dtos)
	if err != nil {
		fail(h, err)
		return nil
	}
	clear(h)
	*outLen = C.int(len(payload))
	return (*C.char)(C.CBytes(payload))
}

//export skills_fs_read
func skills_fs_read(handle C.uintptr_t, path *C.char, outLen *C.int) *C.char {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return nil
	}
	data, err := fs.Read(context.Background(), C.GoString(path), core.CallerIdentity{})
	if err != nil {
		fail(h, err)
		return nil
	}
	clear(h)
	*outLen = C.int(len(data))
	return (*C.char)(C.CBytes(data))
}

//export skills_fs_write
func skills_fs_write(handle C.uintptr_t, path *C.char, data *C.char, length C.int) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return -1
	}
	payload := C.GoBytes(unsafe.Pointer(data), length)
	if err := fs.Write(context.Background(), C.GoString(path), payload, core.CallerIdentity{}); err != nil {
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

//export skills_fs_free
func skills_fs_free(p unsafe.Pointer) {
	C.free(p)
}

func main() {}
