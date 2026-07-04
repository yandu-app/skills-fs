//go:build linux

package fuse

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/skills-fs/skills-fs/core"
)

func TestToErrnoMapping(t *testing.T) {
	cases := []struct {
		code core.Errno
		want int
	}{
		{core.ENOENT, 2},      // syscall.ENOENT
		{core.EACCES, 13},     // syscall.EACCES
		{core.EINVAL, 22},     // syscall.EINVAL
		{core.EBUSY, 16},      // syscall.EBUSY
		{core.EAGAIN, 11},     // syscall.EAGAIN
		{core.ENOSPC, 28},     // syscall.ENOSPC
		{core.ENOTDIR, 20},    // syscall.ENOTDIR
		{core.EISDIR, 21},     // syscall.EISDIR
		{core.ENOSYS, 38},     // syscall.ENOSYS
		{core.ETIMEDOUT, 110}, // syscall.ETIMEDOUT
	}
	for _, tc := range cases {
		err := core.PosixError{Code: tc.code}
		got := int(toErrno(&err))
		if got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestFileModeConversion(t *testing.T) {
	cases := []struct {
		st   core.Stat
		want uint32
	}{
		{core.Stat{Kind: core.KindDir, Mode: 0o555}, 0o040555},
		{core.Stat{Kind: core.KindBlob, Mode: 0o644}, 0o100644},
		{core.Stat{Kind: core.KindStream, Mode: 0o666}, 0o100666},
		{core.Stat{Kind: core.KindAPI, Mode: 0o222}, 0o100222},
	}
	for _, tc := range cases {
		got := fileMode(tc.st)
		if got != tc.want {
			t.Fatalf("kind=%s mode=%o: got %o, want %o", tc.st.Kind, tc.st.Mode, got, tc.want)
		}
	}
}

func TestDirEntriesConversion(t *testing.T) {
	entries := []core.DirEntry{
		{Name: "sys", Kind: core.KindDir, Mode: 0o555},
		{Name: "blob", Kind: core.KindBlob, Mode: 0o644},
	}
	out := dirEntries(entries)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	if out[0].Name != "sys" || out[0].Mode != 0o040555 {
		t.Fatalf("unexpected dir entry: %+v", out[0])
	}
	if out[1].Name != "blob" || out[1].Mode != 0o100644 {
		t.Fatalf("unexpected file entry: %+v", out[1])
	}
}

func TestFillAttrOut(t *testing.T) {
	var out fuse.AttrOut
	fillAttrOut(&out, core.Stat{Mode: 0o644, UID: 7, GID: 8, Size: 42})
	if out.Mode != 0o100644 || out.Uid != 7 || out.Gid != 8 || out.Size != 42 {
		t.Fatalf("unexpected attr out: mode=%o uid=%d gid=%d size=%d", out.Mode, out.Uid, out.Gid, out.Size)
	}
}

func TestFillAttrOutAPISize(t *testing.T) {
	var out fuse.AttrOut
	fillAttrOut(&out, core.Stat{Kind: core.KindAPI, Mode: 0o444, Size: 0})
	if out.Size != 1024*1024 {
		t.Fatalf("API attr size: got %d, want %d", out.Size, 1024*1024)
	}
}

func TestFillEntryOutAPISize(t *testing.T) {
	var out fuse.EntryOut
	fillEntryOut(&out, core.Stat{Kind: core.KindAPI, Mode: 0o444, Size: 0})
	if out.Size != 1024*1024 {
		t.Fatalf("API entry size: got %d, want %d", out.Size, 1024*1024)
	}
}

func TestOpenFlagsFromFUSE(t *testing.T) {
	cases := []struct {
		flags uint32
		want  core.OpenFlags
	}{
		{0, core.OpenRead},
		{syscall.O_LARGEFILE, core.OpenRead},
		{syscall.O_RDONLY, core.OpenRead},
		{syscall.O_WRONLY, core.OpenWrite},
		{syscall.O_RDWR, core.OpenRead | core.OpenWrite},
		{syscall.O_WRONLY | syscall.O_APPEND, core.OpenWrite | core.OpenAppend},
		{syscall.O_RDWR | syscall.O_NONBLOCK, core.OpenRead | core.OpenWrite | core.OpenNonBlock},
	}
	for _, tc := range cases {
		got := openFlagsFromFUSE(tc.flags)
		if got != tc.want {
			t.Fatalf("flags=%#x: got %v, want %v", tc.flags, got, tc.want)
		}
	}
}

func TestFileHandleSetattrAPI(t *testing.T) {
	fsys := core.NewFS(core.GlobalConfig{})
	if err := fsys.RegisterProvider(&staticProvider{data: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mount("/api", core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o444,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: "static", Action: "get"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := fsys.Open("/api", core.OpenRead, core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	fh := &fileHandle{h: h, fsys: fsys, ino: 123, readOnly: true}

	var in fuse.SetAttrIn
	in.Valid = fuse.FATTR_SIZE
	in.Size = 0
	var out fuse.AttrOut
	errno := fh.Setattr(context.Background(), &in, &out)
	if errno != fs.OK {
		t.Fatalf("expected OK for API size setattr, got errno=%d", errno)
	}
	if out.Size != 1024*1024 {
		t.Fatalf("expected API placeholder size, got %d", out.Size)
	}
}

type staticProvider struct {
	data []byte
}

func (p *staticProvider) ID() string { return "static" }
func (p *staticProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	return &core.ProviderResult{Data: p.data}, nil
}
