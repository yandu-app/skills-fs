package core

import (
	"context"
	"testing"
	"time"
)

func TestHandleAccessorsAndManager(t *testing.T) {
	fs := NewFS(GlobalConfig{MaxOpenHandles: 2})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	caller := CallerIdentity{UID: 7, GID: 8}
	h, err := fs.Open("/blob", OpenRead|OpenWrite, caller)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID() == 0 || h.Path() != "/blob" || h.Caller() != caller || h.LockKind() != LockNone {
		t.Fatalf("unexpected handle accessors: id=%d path=%s caller=%#v lock=%v", h.ID(), h.Path(), h.Caller(), h.LockKind())
	}
	if got, ok := fs.handles.get(h.ID()); !ok || got != h {
		t.Fatalf("handle lookup failed: %#v %v", got, ok)
	}
	if fs.handles.Active() != 1 {
		t.Fatalf("active handles = %d", fs.handles.Active())
	}
	if len(fs.handles.snapshot()) != 1 {
		t.Fatalf("snapshot size mismatch")
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.handles.Active() != 0 {
		t.Fatalf("active handles after close = %d", fs.handles.Active())
	}
}

func TestOpenRejectsInvalidFlagsAndPermission(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o400, UID: 1, GID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Open("/blob", 0, CallerIdentity{UID: 1, GID: 1}); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
	if _, err := fs.Open("/blob", OpenWrite, CallerIdentity{UID: 2, GID: 2}); !IsCode(err, EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
}

func TestHandleWritePermissionAndDoubleClose(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o444}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenRead, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Write(context.Background(), []byte("x")); !IsCode(err, EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
	if err := h.Flush(context.Background()); !IsCode(err, EBUSY) {
		t.Fatalf("expected closed flush EBUSY, got %v", err)
	}
}

func TestBufferedHandleFlushOnDelay(t *testing.T) {
	provider := &fakeProvider{id: "p"}
	fs := NewFS(GlobalConfig{})
	if err := fs.RegisterProvider(provider); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/commands/run", MountEntry{
		Kind:         KindAPI,
		Mode:         0o222,
		BufferPolicy: &WriteBufferPolicy{Mode: WriteBuffered, MaxDelay: 5 * time.Millisecond},
		Ops: map[OpCode]*CapConfig{OpWrite: {
			ProviderID: "p",
			Action:     "run",
			ParamsFn: func(_ map[string]string, payload []byte, _ OpContext) (map[string]interface{}, error) {
				return map[string]interface{}{"payload": string(payload)}, nil
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/commands/run", OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Write(context.Background(), []byte("delayed")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(200 * time.Millisecond)
	for {
		provider.mu.Lock()
		n := len(provider.calls)
		provider.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timer flush did not run")
		case <-time.After(time.Millisecond):
		}
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFlockSharedExclusiveAndCloseRelease(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	h1, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h1.Flock(context.Background(), LockShared, false); err != nil {
		t.Fatal(err)
	}
	if err := h2.Flock(context.Background(), LockShared, false); err != nil {
		t.Fatal(err)
	}
	if shared, excl := fs.locks.inspect("/blob"); shared != 2 || excl {
		t.Fatalf("shared/excl = %d/%v", shared, excl)
	}
	if err := h1.Flock(context.Background(), LockExclusive, true); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
	if err := h2.Funlock(); err != nil {
		t.Fatal(err)
	}
	if err := h1.Flock(context.Background(), LockExclusive, false); err != nil {
		t.Fatal(err)
	}
	if _, excl := fs.locks.inspect("/blob"); !excl {
		t.Fatalf("exclusive lock missing")
	}
	if err := h1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, excl := fs.locks.inspect("/blob"); excl {
		t.Fatalf("exclusive lock should release on close")
	}
	if err := h2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFlockInvalidAndClosed(t *testing.T) {
	fs := NewFS(GlobalConfig{})
	if err := fs.Mount("/blob", MountEntry{Kind: KindBlob, Mode: 0o666}); err != nil {
		t.Fatal(err)
	}
	h, err := fs.Open("/blob", OpenRead|OpenWrite, CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Flock(context.Background(), LockKind(99), false); !IsCode(err, EINVAL) {
		t.Fatalf("expected EINVAL, got %v", err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Flock(context.Background(), LockShared, false); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
	if err := h.Funlock(); !IsCode(err, EBUSY) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
}
