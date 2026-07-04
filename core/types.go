package core

import (
	"context"
	"fmt"
	"time"
)

// NodeKind classifies the type of a mounted node.
type NodeKind string

const (
	KindBlob       NodeKind = "blob"
	KindAPI        NodeKind = "api"
	KindStream     NodeKind = "stream"
	KindDir        NodeKind = "dir"
	KindDynamicDir NodeKind = "dynamic_dir"
	KindLink       NodeKind = "link"
)

// OpCode identifies a filesystem operation for capability checks and error reporting.
type OpCode string

const (
	OpRead    OpCode = "read"
	OpWrite   OpCode = "write"
	OpStat    OpCode = "stat"
	OpReaddir OpCode = "readdir"
)

// GlobalConfig controls filesystem-wide limits and behaviour.
// Apply defaults by calling [NewFS], which invokes withDefaults internally.
type GlobalConfig struct {
	Label             string
	MaxOpenHandles    int
	MaxMounts         int
	MaxBlobSize       int64
	ZeroCopyThreshold int
	StatCacheTTL      time.Duration
	DefaultUID        uint32
	DefaultGID        uint32
	SkillsRoot        string
	LockTimeout       time.Duration // advisory lock acquisition timeout
	Breaker           CircuitBreakerConfig
	AuditFunc         AuditFunc // optional; nil disables audit logging
}

// AuditFunc receives an entry for every audited filesystem operation.
type AuditFunc func(AuditEntry)

// AuditEntry describes a single audited operation.
type AuditEntry struct {
	Timestamp time.Time
	Op        string
	Path      string
	Caller    CallerIdentity
	Err       error
	Duration  time.Duration
}

// CircuitBreakerConfig configures per-provider circuit breaking.
type CircuitBreakerConfig struct {
	Enabled          bool
	FailureThreshold int           // consecutive failures before opening
	ResetTimeout     time.Duration // time before trying half-open
	HalfOpenMaxCalls int           // successful calls needed to close
}

func (c GlobalConfig) withDefaults() GlobalConfig {
	if c.MaxOpenHandles == 0 {
		c.MaxOpenHandles = 65536
	}
	if c.MaxMounts == 0 {
		c.MaxMounts = 10000
	}
	if c.MaxBlobSize == 0 {
		c.MaxBlobSize = 64 * 1024 * 1024
	}
	if c.ZeroCopyThreshold == 0 {
		c.ZeroCopyThreshold = 4096
	}
	if c.LockTimeout == 0 {
		c.LockTimeout = 30 * time.Second
	}
	if c.Breaker.FailureThreshold == 0 {
		c.Breaker.FailureThreshold = 5
	}
	if c.Breaker.ResetTimeout == 0 {
		c.Breaker.ResetTimeout = 30 * time.Second
	}
	if c.Breaker.HalfOpenMaxCalls == 0 {
		c.Breaker.HalfOpenMaxCalls = 1
	}
	return c
}

func (c GlobalConfig) Validate() error {
	if c.MaxOpenHandles < 0 {
		return fmt.Errorf("MaxOpenHandles must be non-negative")
	}
	if c.MaxMounts < 0 {
		return fmt.Errorf("MaxMounts must be non-negative")
	}
	if c.MaxBlobSize < 0 {
		return fmt.Errorf("MaxBlobSize must be non-negative")
	}
	if c.ZeroCopyThreshold < 0 {
		return fmt.Errorf("ZeroCopyThreshold must be non-negative")
	}
	if c.StatCacheTTL < 0 {
		return fmt.Errorf("StatCacheTTL must be non-negative")
	}
	if c.LockTimeout < 0 {
		return fmt.Errorf("LockTimeout must be non-negative")
	}
	if c.Breaker.FailureThreshold < 0 {
		return fmt.Errorf("Breaker.FailureThreshold must be non-negative")
	}
	if c.Breaker.ResetTimeout < 0 {
		return fmt.Errorf("Breaker.ResetTimeout must be non-negative")
	}
	if c.Breaker.HalfOpenMaxCalls < 0 {
		return fmt.Errorf("Breaker.HalfOpenMaxCalls must be non-negative")
	}
	return nil
}

type BackpressureMode int

const (
	BackpressureBlock BackpressureMode = iota
	BackpressureDrop
	BackpressureError
)

type StreamConfig struct {
	Capacity     int
	Mode         BackpressureMode
	MaxChunkSize int // max bytes per read/write; zero = default (64 KiB)
}

// MountEntry describes a single node in the virtual filesystem.
// Populate Kind, Mode, and at least one data source (BlobData, LinkPath,
// Ops, or Stream) before passing to [FileSystem.Mount].
type MountEntry struct {
	ID           uint64
	Path         string
	Kind         NodeKind
	Mode         uint32
	UID          uint32
	GID          uint32
	Ops          map[OpCode]*CapConfig
	Serial       bool
	BufferPolicy *WriteBufferPolicy
	Skill        *SkillConfig
	Stream       *StreamConfig
	Visibility   string
	Namespace    string // empty = global namespace

	BlobData []byte
	LinkPath string
	serial   *serialQueue
}

func (e MountEntry) Validate() error {
	switch e.Kind {
	case KindBlob, KindAPI, KindStream, KindDir, KindDynamicDir, KindLink:
		// valid
	default:
		return fmt.Errorf("invalid kind %q", e.Kind)
	}
	if len(e.BlobData) > 0 && e.Kind != KindBlob {
		return fmt.Errorf("BlobData set for non-blob kind %q", e.Kind)
	}
	if e.LinkPath != "" && e.Kind != KindLink {
		return fmt.Errorf("LinkPath set for non-link kind %q", e.Kind)
	}
	if e.Stream != nil {
		if e.Kind != KindStream {
			return fmt.Errorf("stream config set for non-stream kind %q", e.Kind)
		}
		if e.Stream.Capacity <= 0 {
			return fmt.Errorf("stream capacity must be positive")
		}
		if e.Stream.MaxChunkSize < 0 {
			return fmt.Errorf("stream MaxChunkSize must be non-negative")
		}
	}
	if e.Skill != nil && e.Skill.Enabled && e.Skill.Name == "" {
		return fmt.Errorf("enabled skill requires Name")
	}
	if e.Visibility != "" && e.Visibility != "public" && e.Visibility != "private" {
		return fmt.Errorf("invalid visibility %q", e.Visibility)
	}
	for op, cap := range e.Ops {
		if cap == nil {
			continue
		}
		if cap.ProviderID == "" {
			return fmt.Errorf("operation %q requires ProviderID", op)
		}
	}
	return nil
}

// CapConfig binds an [OpCode] to a provider action.
type CapConfig struct {
	ProviderID string
	Action     string
	ParamsFn   func(pathParams map[string]string, payload []byte, ctx OpContext) (map[string]interface{}, error)
	Async      bool          // when true, provider runs in background and empty result is returned immediately
	Timeout    time.Duration // per-invocation timeout; zero = inherit from caller context
	CacheTTL   time.Duration // provider result cache TTL; zero = disabled
}

// OpContext carries operation metadata into ParamsFn callbacks.
type OpContext struct {
	Path   string
	Op     OpCode
	Caller CallerIdentity
}

// CallerIdentity identifies the caller for permission checks.
type CallerIdentity struct {
	UID       uint32
	GID       uint32
	Namespace string // empty = global namespace
}

// Provider is the interface that external data sources implement.
// Register implementations with [FileSystem.RegisterProvider].
type Provider interface {
	ID() string
	Invoke(ctx context.Context, action string, params map[string]interface{}) (*ProviderResult, error)
}

// HealthCheckable is an optional interface providers may implement
// to support explicit health check probes.
type HealthCheckable interface {
	HealthCheck(ctx context.Context) error
}

// ProviderResult is returned by Provider.Invoke. Data is forwarded through
// FileSystem.Read; Meta and ContentType are currently provider-side metadata
// not yet wired to adapters (e.g. WebDAV could use ContentType for the
// Content-Type header instead of contentTypeFromKind).
type ProviderResult struct {
	Data        []byte
	Meta        map[string]string
	ContentType string
}

// SkillConfig describes a skill to be generated into the /skills namespace.
type SkillConfig struct {
	Enabled       bool              `json:"enabled"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Version       string            `json:"version,omitempty"`
	Author        string            `json:"author,omitempty"`
	License       string            `json:"license,omitempty"`
	Platforms     []string          `json:"platforms,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  []string          `json:"allowedTools,omitempty"`
	BodyTemplate  string            `json:"bodyTemplate"`
	AgentsTemplate string           `json:"agentsTemplate,omitempty"`
	Scripts       []string          `json:"scripts,omitempty"`
	References    []string          `json:"references,omitempty"`
	ExposeAtRoot  bool              `json:"exposeAtRoot,omitempty"`
}

// Stat describes a mounted node, returned by [FileSystem.Stat].
type Stat struct {
	Path string
	Kind NodeKind
	Mode uint32
	UID  uint32
	GID  uint32
	Size int64
}

// DirEntry is a single child returned by [FileSystem.Readdir].
type DirEntry struct {
	Name string
	Kind NodeKind
	Mode uint32
}

type SnapshotDiff struct {
	Added    []MountEntry
	Removed  []MountEntry
	Modified []MountEntryChange
}

type MountEntryChange struct {
	Path string
	Old  MountEntry
	New  MountEntry
}
