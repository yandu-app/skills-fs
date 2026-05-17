package adapter_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/fuse"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	"github.com/skills-fs/skills-fs/core"
)

func TestFuseStub(t *testing.T) {
	fsys := core.NewFS(core.GlobalConfig{})
	server := fuse.New(fsys, "/mnt/skills", adapter.MountOptions{AllowOther: true})
	if server.FileSystem() != fsys || server.MountPoint() != "/mnt/skills" || !server.Options().AllowOther {
		t.Fatalf("unexpected fuse server state")
	}

	err := server.Mount(context.Background())
	if runtime.GOOS != "linux" {
		if !errors.Is(err, adapter.ErrNotImplemented) {
			t.Fatalf("expected ErrNotImplemented, got %v", err)
		}
		if err := server.Unmount(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
			t.Fatalf("expected ErrNotImplemented, got %v", err)
		}
	} else {
		// On Linux the adapter attempts a real FUSE mount. /mnt/skills does not
		// exist, so we expect a real error rather than ErrNotImplemented.
		if errors.Is(err, adapter.ErrNotImplemented) {
			t.Fatalf("expected real mount error on Linux, got ErrNotImplemented")
		}
	}
}

func TestWebDAVServer(t *testing.T) {
	fsys := core.NewFS(core.GlobalConfig{})
	server := webdav.New(fsys, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if server.FileSystem() != fsys || server.MountPoint() != "127.0.0.1:0" || !server.Options().ReadOnly {
		t.Fatalf("unexpected webdav server state")
	}
	if err := server.Mount(context.Background()); err != nil {
		t.Fatalf("unexpected mount error: %v", err)
	}
	if err := server.Unmount(context.Background()); err != nil {
		t.Fatalf("unexpected unmount error: %v", err)
	}
}

func TestShutdownContextUsesDefault(t *testing.T) {
	opts := adapter.MountOptions{}
	ctx, cancel := opts.ShutdownContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	want := time.Now().Add(adapter.DefaultShutdownTimeout)
	if deadline.After(want.Add(2*time.Second)) || deadline.Before(want.Add(-2*time.Second)) {
		t.Fatalf("deadline too far from expected: got %v, want around %v", deadline, want)
	}
}

func TestShutdownContextPreservesExistingDeadline(t *testing.T) {
	opts := adapter.MountOptions{ShutdownTimeout: 5 * time.Second}
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ctx, ctxCancel := opts.ShutdownContext(parent)
	defer ctxCancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	// Should keep the parent's shorter deadline.
	if time.Until(deadline) > 5*time.Second {
		t.Fatal("expected parent's shorter deadline to be preserved")
	}
}

func TestShutdownContextUsesCustomTimeout(t *testing.T) {
	opts := adapter.MountOptions{ShutdownTimeout: 10 * time.Second}
	ctx, cancel := opts.ShutdownContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	want := time.Now().Add(10 * time.Second)
	if deadline.After(want.Add(2*time.Second)) || deadline.Before(want.Add(-2*time.Second)) {
		t.Fatalf("deadline mismatch: got %v", deadline)
	}
}
