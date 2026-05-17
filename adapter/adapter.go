package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

var ErrNotImplemented = errors.New("adapter not implemented")

// DefaultShutdownTimeout is applied when MountOptions.ShutdownTimeout is zero.
const DefaultShutdownTimeout = 30 * time.Second

type MountOptions struct {
	ReadOnly        bool
	AllowOther      bool
	TLSCertFile     string
	TLSKeyFile      string
	AllowedOrigins  []string      // empty = allow all (for WebSocket origin validation)
	EnableGzip      bool          // compress GET/PROPFIND responses for WebDAV
	ShutdownTimeout time.Duration // zero = use DefaultShutdownTimeout
	RateLimitRPS    float64       // zero = no rate limiting
	RateLimitBurst  int           // max burst size for rate limiter
	CORSOrigins     []string      // empty = allow all origins
	MaxConnections  int           // zero = unlimited concurrent connections
	Debug           bool          // enable /debug/pprof endpoints
	MaxRequestSize   int64         // max request body bytes (PUT); zero = unlimited
	MaxResponseSize  int64         // max response body bytes (GET); zero = unlimited
	MaxPropfindDepth  int           // max depth for PROPFIND; zero = default (3), negative = unlimited
	MaxBatchSize      int           // max ops in a WebSocket batch; zero = default (32)
	PropfindCacheTTL  time.Duration // TTL for PROPFIND property cache; zero = disabled
}

// ShutdownContext returns a context with timeout if the supplied context has
// no deadline and a positive timeout is configured.
func (o MountOptions) ShutdownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := o.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

type MountedFS interface {
	Mount(ctx context.Context) error
	Unmount(ctx context.Context) error
	MountPoint() string
}

type Factory interface {
	New(fs *core.FileSystem, mountPoint string, opts MountOptions) MountedFS
}
