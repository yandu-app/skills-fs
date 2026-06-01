//go:build windows

package fuse

import (
	"context"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/skills-fs/skills-fs/core"
)

type windowsState struct {
	host *fuse.FileSystemHost
}

// winfspFs implements fuse.FileSystemInterface using core.FileSystem.
type winfspFs struct {
	fuse.FileSystemBase
	fsys     *core.FileSystem
	readOnly bool
	handles  map[uint64]*winfspHandle
	nextFH   uint64
}

type winfspHandle struct {
	path string
	h    *core.Handle
}

func (s *Server) Mount(_ context.Context) error {
	wfs := &winfspFs{
		fsys:     s.fs,
		readOnly: s.opts.ReadOnly,
		handles:  make(map[uint64]*winfspHandle),
		nextFH:   1,
	}
	host := fuse.NewFileSystemHost(wfs)
	s.state = &windowsState{host: host}
	if !host.Mount(s.mountPoint, []string{"-o", "umask=000"}) {
		return fmt.Errorf("winfsp mount failed")
	}
	return nil
}

func (s *Server) Unmount(_ context.Context) error {
	if s.state == nil {
		return nil
	}
	st, ok := s.state.(*windowsState)
	if !ok || st.host == nil {
		return nil
	}
	st.host.Unmount()
	return nil
}

// --- fuse.FileSystemInterface ---

func (fs *winfspFs) Init() {}

func (fs *winfspFs) Destroy() {}

func (fs *winfspFs) Statfs(path string, stat *fuse.Statfs_t) int {
	stat.Bsize = 4096
	stat.Frsize = 4096
	stat.Blocks = 1 << 40
	stat.Bfree = 1 << 39
	stat.Bavail = 1 << 39
	stat.Namemax = 255
	return 0
}

func (fs *winfspFs) Open(path string, flags int) (int, uint64) {
	of := core.OpenRead
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		of |= core.OpenWrite
	}
	if flags&syscall.O_APPEND != 0 {
		of |= core.OpenAppend
	}
	h, err := fs.fsys.Open(path, of, core.CallerIdentity{})
	if err != nil {
		return toErrno(err), ^uint64(0)
	}
	fh := fs.nextFH
	fs.nextFH++
	fs.handles[fh] = &winfspHandle{path: path, h: h}
	return 0, fh
}

func (fs *winfspFs) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	if fh != ^uint64(0) {
		if h, ok := fs.handles[fh]; ok {
			path = h.path
		}
	}
	st, err := fs.fsys.Stat(path, core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fillStat(stat, st)
	return 0
}

func (fs *winfspFs) Read(path string, buff []byte, ofst int64, fh uint64) int {
	h, ok := fs.handles[fh]
	if !ok {
		return -fuse.ENOENT
	}
	data, err := h.h.ReadAll(nil)
	if err != nil {
		return toErrno(err)
	}
	if ofst >= int64(len(data)) {
		return 0
	}
	end := ofst + int64(len(buff))
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return copy(buff, data[ofst:end])
}

func (fs *winfspFs) Write(path string, buff []byte, ofst int64, fh uint64) int {
	h, ok := fs.handles[fh]
	if !ok {
		return -fuse.ENOENT
	}
	err := h.h.Write(nil, buff)
	if err != nil {
		return toErrno(err)
	}
	return len(buff)
}

func (fs *winfspFs) Release(path string, fh uint64) int {
	if h, ok := fs.handles[fh]; ok {
		if h.h != nil {
			h.h.Close(nil)
		}
		delete(fs.handles, fh)
	}
	return 0
}

func (fs *winfspFs) Opendir(path string) (int, uint64) {
	return 0, 0
}

func (fs *winfspFs) Readdir(path string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	entries, err := fs.fsys.Readdir(path, core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fill(".", nil, 0)
	fill("..", nil, 0)
	for _, e := range entries {
		childPath := filepath.Join(path, e.Name)
		st := &fuse.Stat_t{}
		childStat, serr := fs.fsys.Stat(childPath, core.CallerIdentity{})
		if serr == nil {
			fillStat(st, childStat)
		}
		fill(e.Name, st, 0)
	}
	return 0
}

func (fs *winfspFs) Releasedir(path string, fh uint64) int {
	return 0
}

func (fs *winfspFs) Create(path string, flags int, mode uint32) (int, uint64) {
	if fs.readOnly {
		return -fuse.EROFS, ^uint64(0)
	}
	if err := fs.fsys.Mount(path, core.MountEntry{Kind: core.KindBlob, Mode: mode & 0o777}); err != nil {
		return toErrno(err), ^uint64(0)
	}
	of := core.OpenRead | core.OpenWrite
	h, err := fs.fsys.Open(path, of, core.CallerIdentity{})
	if err != nil {
		_ = fs.fsys.Unmount(path)
		return toErrno(err), ^uint64(0)
	}
	fh := fs.nextFH
	fs.nextFH++
	fs.handles[fh] = &winfspHandle{path: path, h: h}
	return 0, fh
}

func (fs *winfspFs) Mkdir(path string, mode uint32) int {
	if fs.readOnly {
		return -fuse.EROFS
	}
	return toErrno(fs.fsys.Mount(path, core.MountEntry{Kind: core.KindDir, Mode: mode & 0o777}))
}

func (fs *winfspFs) Unlink(path string) int {
	if fs.readOnly {
		return -fuse.EROFS
	}
	return toErrno(fs.fsys.Unmount(path))
}

func (fs *winfspFs) Rmdir(path string) int {
	if fs.readOnly {
		return -fuse.EROFS
	}
	return toErrno(fs.fsys.Unmount(path))
}

func (fs *winfspFs) Rename(oldpath string, newpath string) int {
	if fs.readOnly {
		return -fuse.EROFS
	}
	return toErrno(fs.fsys.Rename(oldpath, newpath))
}

func (fs *winfspFs) Access(path string, mask uint32) int {
	// Allow all access — permissions are enforced by core.FileSystem
	return 0
}

func (fs *winfspFs) Chmod(path string, mode uint32) int  { return 0 }
func (fs *winfspFs) Chown(path string, uid, gid uint32) int { return 0 }
func (fs *winfspFs) Utimens(path string, tmsp []fuse.Timespec) int { return 0 }

// --- helpers ---

// currentUID/GID — WinFsp uses these for ACL mapping.
// Must match the actual Windows user, not 0 (SYSTEM).
var currentUID, currentGID uint32

func init() {
	// Resolve current user's UID via WinFsp's fsptool-compatible logic.
	// On Windows, UID = hash of SID or just use a fixed non-zero value.
	// WinFsp maps UID 0 → SYSTEM, which causes permission issues.
	// Using 1000 (standard first user) works correctly.
	currentUID = 1000
	currentGID = 1000
}

func fillStat(out *fuse.Stat_t, st core.Stat) {
	switch st.Kind {
	case core.KindDir:
		out.Mode = syscall.S_IFDIR | st.Mode
	case core.KindLink:
		out.Mode = syscall.S_IFLNK | st.Mode
	default:
		out.Mode = syscall.S_IFREG | st.Mode
	}
	out.Size = st.Size
	// Use current user UID/GID instead of core's 0 (SYSTEM)
	out.Uid = currentUID
	out.Gid = currentGID
	if !st.ModTime.IsZero() {
		out.Mtim = fuse.NewTimespec(st.ModTime)
		out.Ctim = fuse.NewTimespec(st.ModTime)
		out.Atim = fuse.NewTimespec(st.ModTime)
	}
}

func toErrno(err error) int {
	if err == nil {
		return 0
	}
	switch core.ExtractErrno(err) {
	case core.ENOENT:
		return -fuse.ENOENT
	case core.EACCES:
		return -fuse.EACCES
	case core.EEXIST:
		return -fuse.EEXIST
	case core.EINVAL:
		return -fuse.EINVAL
	case core.ENOTDIR:
		return -fuse.ENOTDIR
	case core.EISDIR:
		return -fuse.EISDIR
	case core.ENOSYS:
		return -fuse.ENOSYS
	default:
		return -fuse.EIO
	}
}
