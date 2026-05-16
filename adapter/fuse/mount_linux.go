//go:build linux

package fuse

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/skills-fs/skills-fs/core"
)

type linuxState struct {
	srv  *fuse.Server
	root *rootNode
}

func (s *Server) Mount(ctx context.Context) error {
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			Name:          "skillsfs",
			FsName:        "skillsfs",
			DisableXAttrs: true,
			AllowOther:    s.opts.AllowOther,
			Debug:         false,
		},
	}
	if s.opts.ReadOnly {
		opts.MountOptions.Options = append(opts.MountOptions.Options, "ro")
	}

	root := &rootNode{fsys: s.fs, inodes: make(map[string]*fs.Inode)}
	server, err := fs.Mount(s.mountPoint, root, opts)
	if err != nil {
		return err
	}
	s.state = &linuxState{srv: server, root: root}
	go s.state.(*linuxState).srv.Serve()
	if err := s.state.(*linuxState).srv.WaitMount(); err != nil {
		_ = s.state.(*linuxState).srv.Unmount()
		return err
	}

	// Wire core events to kernel inotify invalidations.
	s.fs.RegisterNotifier(func(e core.Event) {
		root.handleEvent(e)
	}, "")
	return nil
}

func (s *Server) Unmount(_ context.Context) error {
	if s.state == nil {
		return nil
	}
	st, ok := s.state.(*linuxState)
	if !ok || st.srv == nil {
		return nil
	}
	return st.srv.Unmount()
}

// --- go-fuse filesystem implementation ---

type rootNode struct {
	fs.Inode
	fsys   *core.FileSystem
	mu     sync.RWMutex
	inodes map[string]*fs.Inode
}

var _ fs.NodeGetattrer = (*rootNode)(nil)
var _ fs.NodeLookuper = (*rootNode)(nil)
var _ fs.NodeReaddirer = (*rootNode)(nil)
var _ fs.NodeOpener = (*rootNode)(nil)

func (r *rootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	return r.attr(out)
}

func (r *rootNode) attr(out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	return fs.OK
}

func (r *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := filepath.Join("/", name)
	stat, err := r.fsys.Stat(p, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	node := &pathNode{path: p, fsys: r.fsys}
	fillEntryOut(out, stat)
	mode := fileMode(stat)
	ino := r.NewInode(ctx, node, fs.StableAttr{Mode: uint32(mode)})
	r.mu.Lock()
	r.inodes[p] = ino
	r.mu.Unlock()
	return ino, fs.OK
}

func (r *rootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := r.fsys.Readdir("/", core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	return fs.NewListDirStream(dirEntries(entries)), fs.OK
}

func (r *rootNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return nil, 0, syscall.EISDIR
}

// handleEvent forwards core events to the FUSE kernel cache.
func (r *rootNode) handleEvent(e core.Event) {
	switch e.Kind {
	case core.EventWrite:
		r.mu.RLock()
		ino, ok := r.inodes[e.Path]
		r.mu.RUnlock()
		if ok {
			ino.NotifyContent(0, -1)
		}
		// Also invalidate the directory entry so inotify watchers refresh.
		r.notifyParentEntry(e.Path)

	case core.EventCreate:
		r.notifyParentEntry(e.Path)

	case core.EventRemove:
		r.mu.Lock()
		delete(r.inodes, e.Path)
		r.mu.Unlock()
		r.notifyParentEntry(e.Path)
	}
}

func (r *rootNode) notifyParentEntry(p string) {
	dir, name := path.Split(p)
	dir = path.Clean(dir)
	if dir == "." {
		dir = "/"
	}
	if name == "" {
		return
	}
	r.mu.RLock()
	parent, ok := r.inodes[dir]
	r.mu.RUnlock()
	if ok {
		parent.NotifyEntry(name)
	}
}

// pathNode represents any non-root path in the skills-fs namespace.
type pathNode struct {
	fs.Inode
	path string
	fsys *core.FileSystem
}

var _ fs.NodeGetattrer = (*pathNode)(nil)
var _ fs.NodeLookuper = (*pathNode)(nil)
var _ fs.NodeReaddirer = (*pathNode)(nil)
var _ fs.NodeOpener = (*pathNode)(nil)

func (n *pathNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	stat, err := n.fsys.Stat(n.path, core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(out, stat)
	return fs.OK
}

func (n *pathNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(n.path, name)
	stat, err := n.fsys.Stat(childPath, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	child := &pathNode{path: childPath, fsys: n.fsys}
	fillEntryOut(out, stat)
	mode := fileMode(stat)
	return n.NewInode(ctx, child, fs.StableAttr{Mode: uint32(mode)}), fs.OK
}

func (n *pathNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.fsys.Readdir(n.path, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	return fs.NewListDirStream(dirEntries(entries)), fs.OK
}

func (n *pathNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	var of core.OpenFlags
	if flags&(syscall.O_RDONLY|syscall.O_RDWR) != 0 {
		of |= core.OpenRead
	}
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		of |= core.OpenWrite
	}
	if flags&syscall.O_APPEND != 0 {
		of |= core.OpenAppend
	}
	if flags&syscall.O_NONBLOCK != 0 {
		of |= core.OpenNonBlock
	}
	h, err := n.fsys.Open(n.path, of, core.CallerIdentity{})
	if err != nil {
		return nil, 0, toErrno(err)
	}
	fh := &fileHandle{h: h, fsys: n.fsys}
	return fh, fuse.FOPEN_KEEP_CACHE, fs.OK
}

// fileHandle bridges an open core.Handle to go-fuse file operations.
type fileHandle struct {
	h    *core.Handle
	fsys *core.FileSystem
}

var _ fs.FileReader = (*fileHandle)(nil)
var _ fs.FileWriter = (*fileHandle)(nil)
var _ fs.FileFlusher = (*fileHandle)(nil)
var _ fs.FileReleaser = (*fileHandle)(nil)
var _ fs.FileGetattrer = (*fileHandle)(nil)

func (fh *fileHandle) Read(ctx context.Context, buf []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := fh.h.ReadAll(ctx)
	if err != nil {
		return nil, toErrno(err)
	}
	if off >= int64(len(data)) {
		return fuse.ReadResultData(nil), fs.OK
	}
	data = data[off:]
	if len(data) > len(buf) {
		data = data[:len(buf)]
	}
	return fuse.ReadResultData(data), fs.OK
}

func (fh *fileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	err := fh.h.Write(ctx, data)
	if err != nil {
		return 0, toErrno(err)
	}
	return uint32(len(data)), fs.OK
}

func (fh *fileHandle) Flush(ctx context.Context) syscall.Errno {
	return toErrno(fh.h.Flush(ctx))
}

func (fh *fileHandle) Release(ctx context.Context) syscall.Errno {
	return toErrno(fh.h.Close(ctx))
}

func (fh *fileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	stat, err := fh.fsys.Stat(fh.h.Path(), core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(out, stat)
	return fs.OK
}

// --- helpers ---

func fillAttrOut(out *fuse.AttrOut, st core.Stat) {
	out.Mode = fileMode(st)
	out.Uid = st.UID
	out.Gid = st.GID
	out.Size = uint64(st.Size)
}

func fillEntryOut(out *fuse.EntryOut, st core.Stat) {
	out.Mode = fileMode(st)
	out.Uid = st.UID
	out.Gid = st.GID
	out.Size = uint64(st.Size)
}

func fileMode(st core.Stat) uint32 {
	switch st.Kind {
	case core.KindDir:
		return syscall.S_IFDIR | st.Mode
	default:
		return syscall.S_IFREG | st.Mode
	}
}

func dirEntries(entries []core.DirEntry) []fuse.DirEntry {
	out := make([]fuse.DirEntry, len(entries))
	for i, e := range entries {
		mode := uint32(0)
		switch e.Kind {
		case core.KindDir:
			mode = syscall.S_IFDIR | e.Mode
		default:
			mode = syscall.S_IFREG | e.Mode
		}
		out[i] = fuse.DirEntry{
			Name: e.Name,
			Mode: mode,
		}
	}
	return out
}

func toErrno(err error) syscall.Errno {
	if err == nil {
		return fs.OK
	}
	var pe *core.PosixError
	if !errors.As(err, &pe) {
		return syscall.EIO
	}
	switch pe.Code {
	case core.ENOENT:
		return syscall.ENOENT
	case core.EACCES:
		return syscall.EACCES
	case core.EEXIST:
		return syscall.EEXIST
	case core.EINVAL:
		return syscall.EINVAL
	case core.ETIMEDOUT:
		return syscall.ETIMEDOUT
	case core.EBUSY:
		return syscall.EBUSY
	case core.EAGAIN:
		return syscall.EAGAIN
	case core.ENOSPC:
		return syscall.ENOSPC
	case core.EPIPE:
		return syscall.EPIPE
	case core.ENOTDIR:
		return syscall.ENOTDIR
	case core.EISDIR:
		return syscall.EISDIR
	case core.ENOSYS:
		return syscall.ENOSYS
	default:
		return syscall.EIO
	}
}
