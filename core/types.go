package core

import (
	"context"
	"time"
)

type NodeKind string

const (
	KindBlob   NodeKind = "blob"
	KindAPI    NodeKind = "api"
	KindStream NodeKind = "stream"
	KindDir    NodeKind = "dir"
	KindLink   NodeKind = "link"
)

type OpCode string

const (
	OpRead    OpCode = "read"
	OpWrite   OpCode = "write"
	OpStat    OpCode = "stat"
	OpReaddir OpCode = "readdir"
)

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

type CapConfig struct {
	ProviderID string
	Action     string
	ParamsFn   func(pathParams map[string]string, payload []byte, ctx OpContext) (map[string]interface{}, error)
	Async      bool          // when true, provider runs in background and empty result is returned immediately
	Timeout    time.Duration // per-invocation timeout; zero = inherit from caller context
	CacheTTL   time.Duration // provider result cache TTL; zero = disabled
}

type OpContext struct {
	Path   string
	Op     OpCode
	Caller CallerIdentity
}

type CallerIdentity struct {
	UID       uint32
	GID       uint32
	Namespace string // empty = global namespace
}

type Provider interface {
	ID() string
	Invoke(ctx context.Context, action string, params map[string]interface{}) (*ProviderResult, error)
}

// HealthCheckable is an optional interface providers may implement
// to support explicit health check probes.
type HealthCheckable interface {
	HealthCheck(ctx context.Context) error
}

type ProviderResult struct {
	Data        []byte
	Meta        map[string]string
	ContentType string
}

type SkillConfig struct {
	Enabled       bool
	Name          string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string
	AllowedTools  []string
	BodyTemplate  string
	Scripts       []string
	References    []string
}

type Stat struct {
	Path string
	Kind NodeKind
	Mode uint32
	UID  uint32
	GID  uint32
	Size int64
}

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
