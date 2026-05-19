package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func TestResolveFSKnownHandle(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	h := reg.Register(fs)
	defer reg.Unregister(h)

	got, ok := resolveFS(h)
	if !ok {
		t.Fatal("expected ok for registered handle")
	}
	if got != fs {
		t.Fatal("resolved wrong FileSystem")
	}
}

func TestResolveFSUnknownHandle(t *testing.T) {
	got, ok := resolveFS(99999)
	if ok {
		t.Fatal("expected not ok for unknown handle")
	}
	if got != nil {
		t.Fatal("expected nil FileSystem for unknown handle")
	}
	if msg := reg.LastError(99999); msg != errUnknownHandle {
		t.Fatalf("expected %q, got %q", errUnknownHandle, msg)
	}
}

func TestFailRecordsError(t *testing.T) {
	reg.Register(core.NewFS(core.GlobalConfig{}))
	h := uintptr(1)
	defer reg.Unregister(h)

	fail(h, errors.New("something broke"))
	if msg := reg.LastError(h); msg != "something broke" {
		t.Fatalf("expected error message, got %q", msg)
	}
}

func TestClearResetsError(t *testing.T) {
	reg.Register(core.NewFS(core.GlobalConfig{}))
	h := uintptr(1)
	defer reg.Unregister(h)

	fail(h, errors.New("err"))
	clear(h)
	if msg := reg.LastError(h); msg != "" {
		t.Fatalf("expected empty error, got %q", msg)
	}
}

func TestStatDTOMarshal(t *testing.T) {
	dto := statDTO{
		Path: "/hello",
		Kind: "blob",
		Mode: 0o644,
		UID:  1000,
		GID:  1000,
		Size: 42,
	}
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["path"] != "/hello" {
		t.Fatalf("unexpected path %v", out["path"])
	}
	if out["kind"] != "blob" {
		t.Fatalf("unexpected kind %v", out["kind"])
	}
}

func TestDirEntryDTOMarshal(t *testing.T) {
	dtos := []dirEntryDTO{
		{Name: "a", Kind: "blob", Mode: 0o644},
		{Name: "b", Kind: "dir", Mode: 0o755},
	}
	b, err := json.Marshal(dtos)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	if out[0]["name"] != "a" {
		t.Fatalf("unexpected name %v", out[0]["name"])
	}
}
