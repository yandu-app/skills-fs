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

// Callback type for host-provided providers.
// action, params_json are UTF-8 strings. payload may be NULL.
// On success, callback allocates *out_data with malloc and sets *out_len.
// Returns 0 on success, -1 on error.
typedef int (*skills_fs_provider_cb)(
	uintptr_t handle,
	const char* action,
	const char* params_json,
	const char* payload,
	int payload_len,
	char** out_data,
	int* out_len
);

// Defined in callback.c — calls the function pointer since CGo can't do it directly.
extern int skills_fs_provider_cb_call(
	skills_fs_provider_cb cb,
	uintptr_t handle,
	const char* action,
	const char* params_json,
	const char* payload,
	int payload_len,
	char** out_data,
	int* out_len
);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
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

// callbackProvider wraps a C function pointer as a core.Provider.
type callbackProvider struct {
	id       string
	handle   uintptr
	callback C.skills_fs_provider_cb
}

func (p *callbackProvider) ID() string { return p.id }

func (p *callbackProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	var outData *C.char
	var outLen C.int

	cAction := C.CString(action)
	defer C.free(unsafe.Pointer(cAction))
	cParams := C.CString(string(paramsJSON))
	defer C.free(unsafe.Pointer(cParams))

	rc := C.skills_fs_provider_cb_call(p.callback, C.uintptr_t(p.handle),
		cAction, cParams,
		nil, 0, &outData, &outLen)
	if rc != 0 {
		return nil, fmt.Errorf("provider %s: callback returned %d", p.id, rc)
	}
	defer C.free(unsafe.Pointer(outData))

	return &core.ProviderResult{
		Data: C.GoBytes(unsafe.Pointer(outData), outLen),
	}, nil
}

//export skills_fs_register_provider
func skills_fs_register_provider(handle C.uintptr_t, id *C.char, cb C.skills_fs_provider_cb) C.int {
	h := uintptr(handle)
	fs, ok := resolveFS(h)
	if !ok {
		return -1
	}
	provider := &callbackProvider{
		id:       C.GoString(id),
		handle:   h,
		callback: cb,
	}
	if err := fs.RegisterProvider(provider); err != nil {
		fail(h, err)
		return -1
	}
	clear(h)
	return 0
}

func main() {}
