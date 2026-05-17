package adapter

import (
	"context"
	"errors"

	"github.com/skills-fs/skills-fs/core"
)

var ErrNotImplemented = errors.New("adapter not implemented")

type MountOptions struct {
	ReadOnly       bool
	AllowOther     bool
	TLSCertFile    string
	TLSKeyFile     string
	AllowedOrigins []string // empty = allow all (for WebSocket origin validation)
}

type MountedFS interface {
	Mount(ctx context.Context) error
	Unmount(ctx context.Context) error
	MountPoint() string
}

type Factory interface {
	New(fs *core.FileSystem, mountPoint string, opts MountOptions) MountedFS
}
