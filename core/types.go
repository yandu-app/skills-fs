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
	ZeroCopyThreshold int
	StatCacheTTL      time.Duration
	DefaultUID        uint32
	DefaultGID        uint32
	SkillsRoot        string
}

func (c GlobalConfig) withDefaults() GlobalConfig {
	if c.MaxOpenHandles == 0 {
		c.MaxOpenHandles = 65536
	}
	if c.MaxMounts == 0 {
		c.MaxMounts = 10000
	}
	if c.ZeroCopyThreshold == 0 {
		c.ZeroCopyThreshold = 4096
	}
	return c
}

type MountEntry struct {
	ID         uint64
	Path       string
	Kind       NodeKind
	Mode       uint32
	UID        uint32
	GID        uint32
	Ops        map[OpCode]*CapConfig
	Serial     bool
	Skill      *SkillConfig
	Visibility string

	BlobData []byte
	LinkPath string
}

type CapConfig struct {
	ProviderID string
	Action     string
	ParamsFn   func(pathParams map[string]string, payload []byte, ctx OpContext) (map[string]interface{}, error)
}

type OpContext struct {
	Path   string
	Op     OpCode
	Caller CallerIdentity
}

type CallerIdentity struct {
	UID uint32
	GID uint32
}

type Provider interface {
	ID() string
	Invoke(ctx context.Context, action string, params map[string]interface{}) (*ProviderResult, error)
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
