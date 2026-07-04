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
	// Remove any stale FUSE mount from a previous crashed daemon. A stale mount
	// with a dead userspace connection would otherwise block new mounts and pin
	// any process accessing it in D-state.
	_ = syscall.Unmount(s.mountPoint, 0)

	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			Name:          "skillsfs",
			FsName:        "skillsfs",
			DisableXAttrs: true,
			AllowOther:    s.opts.AllowOther,
			Debug:         true,
		},
	}
	if s.opts.ReadOnly {
		opts.MountOptions.Options = append(opts.MountOptions.Options, "ro")
	}

	root := &rootNode{fsys: s.fs, readOnly: s.opts.ReadOnly, inodes: make(map[string]*fs.Inode)}
	server, err := fs.Mount(s.mountPoint, root, opts)
	if err != nil {
		return err
	}
	s.state = &linuxState{srv: server, root: root}

	// Wire core events to kernel inotify invalidations.
	s.fs.RegisterNotifier(func(e core.Event) {
		root.handleEvent(e)
	}, "")
	return nil
}

func (s *Server) Unmount(ctx context.Context) error {
	if s.state == nil {
		return nil
	}
	st, ok := s.state.(*linuxState)
	if !ok || st.srv == nil {
		return nil
	}
	// go-fuse Unmount is synchronous; use context for timeout only.
	ctx, cancel := s.opts.ShutdownContext(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- st.srv.Unmount() }()
	select {
	case err := <-done:
		// Even if the server unmount succeeded, ask the kernel to drop the
		// mount again so any remaining in-flight request gets an answer and
		// processes stuck in D-state can wake up.
		if unmountErr := syscall.Unmount(s.mountPoint, 0); unmountErr != nil && err == nil {
			return unmountErr
		}
		return err
	case <-ctx.Done():
		// If the server is wedged, force a kernel unmount to abort pending
		// requests and avoid leaving other processes in D-state.
		_ = syscall.Unmount(s.mountPoint, 0)
		return ctx.Err()
	}
}

// --- go-fuse filesystem implementation ---

type rootNode struct {
	fs.Inode
	fsys     *core.FileSystem
	readOnly bool
	mu       sync.RWMutex
	inodes   map[string]*fs.Inode
}

var _ fs.NodeGetattrer = (*rootNode)(nil)
var _ fs.NodeLookuper = (*rootNode)(nil)
var _ fs.NodeReaddirer = (*rootNode)(nil)
var _ fs.NodeOpener = (*rootNode)(nil)
var _ fs.NodeCreater = (*rootNode)(nil)
var _ fs.NodeMkdirer = (*rootNode)(nil)
var _ fs.NodeUnlinker = (*rootNode)(nil)
var _ fs.NodeRmdirer = (*rootNode)(nil)
var _ fs.NodeRenamer = (*rootNode)(nil)
var _ fs.NodeReadlinker = (*rootNode)(nil)
var _ fs.NodeSymlinker = (*rootNode)(nil)

func (r *rootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	return r.attr(out)
}

func (r *rootNode) attr(out *fuse.AttrOut) syscall.Errno {
	out.Mode = syscall.S_IFDIR | 0o555
	out.Ino = r.StableAttr().Ino
	return fs.OK
}

func (r *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := filepath.Join("/", name)
	// Check cache first for stable inode numbers.
	r.mu.RLock()
	cached, ok := r.inodes[p]
	r.mu.RUnlock()
	if ok {
		stat, err := r.fsys.Stat(p, core.CallerIdentity{})
		if err != nil {
			return nil, toErrno(err)
		}
		fillEntryOut(out, stat)
		out.Ino = cached.StableAttr().Ino
		return cached, fs.OK
	}
	stat, err := r.fsys.Stat(p, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	mode := fileMode(stat)
	node := &pathNode{path: p, fsys: r.fsys, root: r, readOnly: r.readOnly}
	ino := r.NewInode(ctx, node, fs.StableAttr{Mode: uint32(mode)})
	fillEntryOut(out, stat)
	out.Ino = ino.StableAttr().Ino
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

func (r *rootNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if e := checkReadOnly(r); e != fs.OK {
		return nil, nil, 0, e
	}
	return createFile(ctx, r.fsys, "/", name, flags, mode, out, r)
}

func (r *rootNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if e := checkReadOnly(r); e != fs.OK {
		return nil, e
	}
	return mkdir(ctx, r.fsys, "/", name, mode, out, r)
}

func (r *rootNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if e := checkReadOnly(r); e != fs.OK {
		return e
	}
	return unlink(r.fsys, "/", name)
}

func (r *rootNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if e := checkReadOnly(r); e != fs.OK {
		return e
	}
	return rmdir(r.fsys, "/", name)
}

func (r *rootNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if e := checkReadOnly(r); e != fs.OK {
		return e
	}
	return rename(r.fsys, "/", name, newParent, newName)
}

func (r *rootNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return readlink(r.fsys, "/")
}

func (r *rootNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if e := checkReadOnly(r); e != fs.OK {
		return nil, e
	}
	return symlink(ctx, r.fsys, "/", target, name, out, r)
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
	path     string
	fsys     *core.FileSystem
	root     *rootNode
	readOnly bool
}

func checkReadOnly(r *rootNode) syscall.Errno {
	if r.readOnly {
		return syscall.EROFS
	}
	return fs.OK
}

var _ fs.NodeGetattrer = (*pathNode)(nil)
var _ fs.NodeSetattrer = (*pathNode)(nil)
var _ fs.NodeLookuper = (*pathNode)(nil)
var _ fs.NodeReaddirer = (*pathNode)(nil)
var _ fs.NodeOpener = (*pathNode)(nil)
var _ fs.NodeCreater = (*pathNode)(nil)
var _ fs.NodeMkdirer = (*pathNode)(nil)
var _ fs.NodeUnlinker = (*pathNode)(nil)
var _ fs.NodeRmdirer = (*pathNode)(nil)
var _ fs.NodeRenamer = (*pathNode)(nil)
var _ fs.NodeReadlinker = (*pathNode)(nil)
var _ fs.NodeSymlinker = (*pathNode)(nil)

func (n *pathNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	stat, err := n.fsys.Stat(n.path, core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(out, stat)
	out.Ino = n.StableAttr().Ino
	return fs.OK
}

func (n *pathNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	stat, err := n.fsys.Stat(n.path, core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	// API files have dynamic content generated by the provider; ignore size
	// changes (e.g. O_TRUNC from open("w")) so writes can proceed.
	if stat.Kind == core.KindAPI {
		fillAttrOut(out, stat)
		out.Ino = n.StableAttr().Ino
		return fs.OK
	}
	return syscall.ENOTSUP
}

func (n *pathNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(n.path, name)
	// Check cache first for stable inode numbers.
	n.root.mu.RLock()
	cached, ok := n.root.inodes[childPath]
	n.root.mu.RUnlock()
	if ok {
		stat, err := n.fsys.Stat(childPath, core.CallerIdentity{})
		if err != nil {
			return nil, toErrno(err)
		}
		fillEntryOut(out, stat)
		out.Ino = cached.StableAttr().Ino
		return cached, fs.OK
	}
	stat, err := n.fsys.Stat(childPath, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	mode := fileMode(stat)
	child := &pathNode{path: childPath, fsys: n.fsys, root: n.root, readOnly: n.readOnly}
	ino := n.NewInode(ctx, child, fs.StableAttr{Mode: uint32(mode)})
	fillEntryOut(out, stat)
	out.Ino = ino.StableAttr().Ino
	n.root.mu.Lock()
	n.root.inodes[childPath] = ino
	n.root.mu.Unlock()
	return ino, fs.OK
}

func (n *pathNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.fsys.Readdir(n.path, core.CallerIdentity{})
	if err != nil {
		return nil, toErrno(err)
	}
	return fs.NewListDirStream(dirEntries(entries)), fs.OK
}

func openFlagsFromFUSE(flags uint32) core.OpenFlags {
	var of core.OpenFlags
	// O_RDONLY is defined as 0, so we cannot test for it with a bitmask.
	// Use O_ACCMODE to distinguish the three access modes.
	switch flags & syscall.O_ACCMODE {
	case syscall.O_WRONLY:
		of |= core.OpenWrite
	case syscall.O_RDWR:
		of |= core.OpenRead | core.OpenWrite
	default:
		of |= core.OpenRead
	}
	if flags&syscall.O_APPEND != 0 {
		of |= core.OpenAppend
	}
	if flags&syscall.O_NONBLOCK != 0 {
		of |= core.OpenNonBlock
	}
	return of
}

func (n *pathNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	of := openFlagsFromFUSE(flags)
	h, err := n.fsys.Open(n.path, of, core.CallerIdentity{})
	if err != nil {
		return nil, 0, toErrno(err)
	}
	fh := &fileHandle{h: h, fsys: n.fsys, ino: n.StableAttr().Ino, readOnly: n.readOnly}
	return fh, fuse.FOPEN_KEEP_CACHE, fs.OK
}

func (n *pathNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.readOnly {
		return nil, nil, 0, syscall.EROFS
	}
	return createFile(ctx, n.fsys, n.path, name, flags, mode, out, n)
}

func (n *pathNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.readOnly {
		return nil, syscall.EROFS
	}
	return mkdir(ctx, n.fsys, n.path, name, mode, out, n)
}

func (n *pathNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.readOnly {
		return syscall.EROFS
	}
	return unlink(n.fsys, n.path, name)
}

func (n *pathNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.readOnly {
		return syscall.EROFS
	}
	return rmdir(n.fsys, n.path, name)
}

func (n *pathNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.readOnly {
		return syscall.EROFS
	}
	return rename(n.fsys, n.path, name, newParent, newName)
}

func (n *pathNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return readlink(n.fsys, n.path)
}

func (n *pathNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.readOnly {
		return nil, syscall.EROFS
	}
	return symlink(ctx, n.fsys, n.path, target, name, out, n)
}

// fileHandle bridges an open core.Handle to go-fuse file operations.
type fileHandle struct {
	h        *core.Handle
	fsys     *core.FileSystem
	readOnly bool
	ino      uint64
}

var _ fs.FileReader = (*fileHandle)(nil)
var _ fs.FileWriter = (*fileHandle)(nil)
var _ fs.FileFlusher = (*fileHandle)(nil)
var _ fs.FileReleaser = (*fileHandle)(nil)
var _ fs.FileGetattrer = (*fileHandle)(nil)
var _ fs.FileSetattrer = (*fileHandle)(nil)

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
	if fh.readOnly {
		return 0, syscall.EROFS
	}
	err := fh.h.Write(ctx, data)
	if err != nil {
		return 0, toErrno(err)
	}
	// #nosec G115 -- FUSE write size is bounded by the protocol (≤128 KiB).
	return uint32(len(data)), fs.OK
}

func (fh *fileHandle) Flush(ctx context.Context) syscall.Errno {
	return toErrno(fh.h.Flush(ctx))
}

func (fh *fileHandle) Release(ctx context.Context) syscall.Errno {
	return toErrno(fh.h.Close(ctx))
}
func (fh *fileHandle) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	stat, err := fh.fsys.Stat(fh.h.Path(), core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	// API files have dynamic content generated by the provider; ignore size
	// changes (e.g. O_TRUNC from open("w")) so writes can proceed.
	if stat.Kind == core.KindAPI {
		fillAttrOut(out, stat)
		out.Ino = fh.ino
		return fs.OK
	}
	return syscall.ENOTSUP
}

func (fh *fileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	stat, err := fh.fsys.Stat(fh.h.Path(), core.CallerIdentity{})
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(out, stat)
	out.Ino = fh.ino
	return fs.OK
}

// --- helpers ---

func fillAttrOut(out *fuse.AttrOut, st core.Stat) {
	out.Mode = fileMode(st)
	out.Uid = st.UID
	out.Gid = st.GID
	// API files have dynamic content; advertise a non-zero size so the
	// kernel will issue READ calls. The actual read returns real content.
	if st.Kind == core.KindAPI {
		out.Size = 1024 * 1024
		return
	}
	// #nosec G115 -- file sizes in core are always non-negative.
	out.Size = uint64(st.Size)
}

func fillEntryOut(out *fuse.EntryOut, st core.Stat) {
	out.Mode = fileMode(st)
	out.Uid = st.UID
	out.Gid = st.GID
	if st.Kind == core.KindAPI {
		out.Size = 1024 * 1024
		return
	}
	// #nosec G115 -- file sizes in core are always non-negative.
	out.Size = uint64(st.Size)
}

func fileMode(st core.Stat) uint32 {
	switch st.Kind {
	case core.KindDir:
		return syscall.S_IFDIR | st.Mode
	case core.KindLink:
		return syscall.S_IFLNK | st.Mode
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
	case core.ELOOP:
		return syscall.ELOOP
	default:
		return syscall.EIO
	}
}

// getRoot extracts the rootNode from an InodeEmbedder (rootNode or pathNode).
func getRoot(parent fs.InodeEmbedder) *rootNode {
	switch p := parent.(type) {
	case *rootNode:
		return p
	case *pathNode:
		return p.root
	default:
		return nil
	}
}

func createFile(ctx context.Context, fsys *core.FileSystem, parentPath, name string, flags uint32, mode uint32, out *fuse.EntryOut, parent fs.InodeEmbedder) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	childPath := filepath.Join(parentPath, name)
	if err := fsys.Mount(childPath, core.MountEntry{Kind: core.KindBlob, Mode: mode & 0o777}); err != nil {
		return nil, nil, 0, toErrno(err)
	}
	var of core.OpenFlags
	if flags&(syscall.O_RDONLY|syscall.O_RDWR) != 0 {
		of |= core.OpenRead
	}
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		of |= core.OpenWrite
	}
	h, err := fsys.Open(childPath, of, core.CallerIdentity{})
	if err != nil {
		_ = fsys.Unmount(childPath)
		return nil, nil, 0, toErrno(err)
	}
	stat, err := fsys.Stat(childPath, core.CallerIdentity{})
	if err != nil {
		_ = h.Close(ctx)
		_ = fsys.Unmount(childPath)
		return nil, nil, 0, toErrno(err)
	}
	fillEntryOut(out, stat)
	root := getRoot(parent)
	ro := root.readOnly
	node := &pathNode{path: childPath, fsys: fsys, root: root, readOnly: ro}
	ino := parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFREG | (mode & 0o777)})
	out.Ino = ino.StableAttr().Ino
	fh := &fileHandle{h: h, fsys: fsys, ino: ino.StableAttr().Ino, readOnly: ro}
	if root != nil {
		root.mu.Lock()
		root.inodes[childPath] = ino
		root.mu.Unlock()
	}
	return ino, fh, fuse.FOPEN_KEEP_CACHE, fs.OK
}

func mkdir(ctx context.Context, fsys *core.FileSystem, parentPath, name string, mode uint32, out *fuse.EntryOut, parent fs.InodeEmbedder) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(parentPath, name)
	if err := fsys.Mount(childPath, core.MountEntry{Kind: core.KindDir, Mode: mode & 0o777}); err != nil {
		return nil, toErrno(err)
	}
	stat, err := fsys.Stat(childPath, core.CallerIdentity{})
	if err != nil {
		_ = fsys.Unmount(childPath)
		return nil, toErrno(err)
	}
	fillEntryOut(out, stat)
	root := getRoot(parent)
	ro := root.readOnly
	node := &pathNode{path: childPath, fsys: fsys, root: root, readOnly: ro}
	ino := parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFDIR | (mode & 0o777)})
	out.Ino = ino.StableAttr().Ino
	if root != nil {
		root.mu.Lock()
		root.inodes[childPath] = ino
		root.mu.Unlock()
	}
	return ino, fs.OK
}

func unlink(fsys *core.FileSystem, parentPath, name string) syscall.Errno {
	childPath := filepath.Join(parentPath, name)
	return toErrno(fsys.Unmount(childPath))
}

func rmdir(fsys *core.FileSystem, parentPath, name string) syscall.Errno {
	childPath := filepath.Join(parentPath, name)
	return toErrno(fsys.Unmount(childPath))
}

func rename(fsys *core.FileSystem, parentPath, name string, newParent fs.InodeEmbedder, newName string) syscall.Errno {
	oldPath := filepath.Join(parentPath, name)
	var newPath string
	switch np := newParent.(type) {
	case *rootNode:
		newPath = filepath.Join("/", newName)
	case *pathNode:
		newPath = filepath.Join(np.path, newName)
	default:
		return syscall.EINVAL
	}
	return toErrno(fsys.Rename(oldPath, newPath))
}

func readlink(fsys *core.FileSystem, p string) ([]byte, syscall.Errno) {
	target, err := fsys.ReadLink(p)
	if err != nil {
		return nil, toErrno(err)
	}
	return []byte(target), fs.OK
}

func symlink(ctx context.Context, fsys *core.FileSystem, parentPath, target, name string, out *fuse.EntryOut, parent fs.InodeEmbedder) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(parentPath, name)
	if err := fsys.Mount(childPath, core.MountEntry{Kind: core.KindLink, Mode: 0o777, LinkPath: target}); err != nil {
		return nil, toErrno(err)
	}
	stat, err := fsys.Stat(childPath, core.CallerIdentity{})
	if err != nil {
		_ = fsys.Unmount(childPath)
		return nil, toErrno(err)
	}
	fillEntryOut(out, stat)
	root := getRoot(parent)
	ro := root.readOnly
	node := &pathNode{path: childPath, fsys: fsys, root: root, readOnly: ro}
	ino := parent.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: syscall.S_IFLNK | 0o777})
	out.Ino = ino.StableAttr().Ino
	if root != nil {
		root.mu.Lock()
		root.inodes[childPath] = ino
		root.mu.Unlock()
	}
	return ino, fs.OK
}
