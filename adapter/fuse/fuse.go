package fuse

import (
	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

// Server wraps a core.FileSystem in a FUSE mount. The Mount and Unmount
// methods are defined in platform-specific files (mount_linux.go for Linux,
// mount_stub.go for everything else).
type Server struct {
	fs         *core.FileSystem
	mountPoint string
	opts       adapter.MountOptions
	state      any
}

func New(fs *core.FileSystem, mountPoint string, opts adapter.MountOptions) *Server {
	return &Server{fs: fs, mountPoint: mountPoint, opts: opts}
}

func (s *Server) MountPoint() string {
	return s.mountPoint
}

func (s *Server) FileSystem() *core.FileSystem {
	return s.fs
}

func (s *Server) Options() adapter.MountOptions {
	return s.opts
}
