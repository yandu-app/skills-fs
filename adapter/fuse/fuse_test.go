package fuse

import (
	"testing"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

func TestServerAccessors(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	opts := adapter.MountOptions{ReadOnly: true}
	srv := New(fs, "/mnt/test", opts)

	if srv.MountPoint() != "/mnt/test" {
		t.Fatalf("expected mount point /mnt/test, got %q", srv.MountPoint())
	}
	if srv.FileSystem() != fs {
		t.Fatal("FileSystem accessor returned wrong filesystem")
	}
	if srv.Options().ReadOnly != true {
		t.Fatal("Options accessor returned wrong options")
	}
}
