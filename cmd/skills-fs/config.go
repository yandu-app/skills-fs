package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/skills-fs/skills-fs/core"
	"github.com/skills-fs/skills-fs/provider/http"
)

// Config describes the filesystem layout and providers for the CLI.
type Config struct {
	Mounts    []MountConfig    `json:"mounts"`
	Providers []ProviderConfig `json:"providers"`
}

// MountConfig describes a single mount point.
type MountConfig struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Mode     string `json:"mode,omitempty"`
	Data     string `json:"data,omitempty"`
	Link     string `json:"link,omitempty"`
	Provider string `json:"provider,omitempty"` // provider ID for API mounts
	Read     string `json:"read,omitempty"`     // action for API read
	Write    string `json:"write,omitempty"`    // action for API write
	Serial   bool   `json:"serial,omitempty"`
}

// ProviderConfig describes an HTTP provider.
type ProviderConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// LoadConfig reads and parses a JSON config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// BuildFS creates a FileSystem from the config, registering providers and
// mounting entries. On error the returned filesystem may be partially built.
func (c *Config) BuildFS() (*core.FileSystem, error) {
	fs := core.NewFS(core.GlobalConfig{})

	for _, pc := range c.Providers {
		p := http.NewProvider(pc.ID, pc.URL)
		if err := fs.RegisterProvider(p); err != nil {
			return nil, fmt.Errorf("register provider %s: %w", pc.ID, err)
		}
	}

	for _, mc := range c.Mounts {
		entry, err := mc.toMountEntry()
		if err != nil {
			return nil, fmt.Errorf("mount %s: %w", mc.Path, err)
		}
		if err := fs.Mount(mc.Path, entry); err != nil {
			return nil, fmt.Errorf("mount %s: %w", mc.Path, err)
		}
	}

	return fs, nil
}

func (mc *MountConfig) toMountEntry() (core.MountEntry, error) {
	mode := uint32(0o644)
	if mc.Mode != "" {
		m, err := strconv.ParseUint(mc.Mode, 8, 32)
		if err != nil {
			return core.MountEntry{}, fmt.Errorf("invalid mode %q: %w", mc.Mode, err)
		}
		mode = uint32(m)
	}

	var kind core.NodeKind
	switch mc.Kind {
	case "blob":
		kind = core.KindBlob
	case "api":
		kind = core.KindAPI
	case "dir":
		kind = core.KindDir
	case "link":
		kind = core.KindLink
	case "stream":
		kind = core.KindStream
	default:
		return core.MountEntry{}, fmt.Errorf("unknown kind %q", mc.Kind)
	}

	entry := core.MountEntry{
		Kind: kind,
		Mode: mode,
	}

	switch kind {
	case core.KindBlob:
		entry.BlobData = []byte(mc.Data)
	case core.KindLink:
		entry.LinkPath = mc.Link
	case core.KindAPI:
		entry.Ops = make(map[core.OpCode]*core.CapConfig)
		pid := mc.Provider
		if pid == "" {
			pid = "remote"
		}
		if mc.Read != "" {
			entry.Ops[core.OpRead] = &core.CapConfig{ProviderID: pid, Action: mc.Read}
		}
		if mc.Write != "" {
			entry.Ops[core.OpWrite] = &core.CapConfig{ProviderID: pid, Action: mc.Write}
		}
		entry.Serial = mc.Serial
	}

	return entry, nil
}
