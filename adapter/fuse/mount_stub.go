//go:build !linux

package fuse

import (
	"context"

	"github.com/skills-fs/skills-fs/adapter"
)

func (s *Server) Mount(_ context.Context) error {
	return adapter.ErrNotImplemented
}

func (s *Server) Unmount(_ context.Context) error {
	return adapter.ErrNotImplemented
}
