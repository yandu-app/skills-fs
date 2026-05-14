//go:build linux

package fuse

import (
	"testing"

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
