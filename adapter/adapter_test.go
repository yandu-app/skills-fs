package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/adapter/fuse"
	"github.com/skills-fs/skills-fs/adapter/webdav"
	"github.com/skills-fs/skills-fs/core"
)

func TestFuseStub(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := fuse.New(fs, "/mnt/skills", adapter.MountOptions{AllowOther: true})
	if server.FileSystem() != fs || server.MountPoint() != "/mnt/skills" || !server.Options().AllowOther {
		t.Fatalf("unexpected fuse server state")
	}
	if err := server.Mount(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if err := server.Unmount(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}

func TestWebDAVStub(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := webdav.New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if server.FileSystem() != fs || server.MountPoint() != "127.0.0.1:0" || !server.Options().ReadOnly {
		t.Fatalf("unexpected webdav server state")
	}
	if err := server.Mount(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}
