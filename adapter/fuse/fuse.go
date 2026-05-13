package fuse

import (
	"context"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

type Server struct {
	fs         *core.FileSystem
	mountPoint string
	opts       adapter.MountOptions
}

func New(fs *core.FileSystem, mountPoint string, opts adapter.MountOptions) *Server {
	return &Server{fs: fs, mountPoint: mountPoint, opts: opts}
}

func (s *Server) Mount(_ context.Context) error {
	return adapter.ErrNotImplemented
}

func (s *Server) Unmount(_ context.Context) error {
	return adapter.ErrNotImplemented
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
