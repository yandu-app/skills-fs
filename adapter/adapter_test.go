package adapter_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

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

func TestWebDAVStub(t *testing.T) {
	fsys := core.NewFS(core.GlobalConfig{})
	server := webdav.New(fsys, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if server.FileSystem() != fsys || server.MountPoint() != "127.0.0.1:0" || !server.Options().ReadOnly {
		t.Fatalf("unexpected webdav server state")
	}
	if err := server.Mount(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}
