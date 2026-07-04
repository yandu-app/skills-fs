package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/skills-fs/skills-fs/core"
	"github.com/skills-fs/skills-fs/provider/http"
)

// Config describes the filesystem layout and providers for the CLI.
type Config struct {
	Mounts     []MountConfig    `json:"mounts"`
	Providers  []ProviderConfig `json:"providers"`
	SkillsRoot string            `json:"skillsRoot,omitempty"`
	Skills     []core.SkillConfig `json:"skills,omitempty"`
	Includes   []string          `json:"includes,omitempty"`
}

// MountConfig describes a single mount point.
type MountConfig struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Mode     string `json:"mode,omitempty"`
	Data     string `json:"data,omitempty"`
	Link     string `json:"link,omitempty"`
	Provider     string `json:"provider,omitempty"` // provider ID for API mounts
	Read         string `json:"read,omitempty"`     // action for API read
	Write        string `json:"write,omitempty"`    // action for API write
	WriteParams  string `json:"writeParams,omitempty"` // "json" to forward JSON payload as params
	Serial       bool   `json:"serial,omitempty"`
	Agents       *bool  `json:"agents,omitempty"`   // nil=required AGENTS.md for dirs; false=opt-out
}

// ProviderConfig describes an HTTP provider.
type ProviderConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// LoadConfig reads and parses a JSON config file, then recursively loads
// and merges any config files listed in the top-level "includes" field.
// Include paths are resolved relative to the parent config file's directory.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}

	baseDir := filepath.Dir(path)
	seen := map[string]bool{path: true}
	for _, inc := range cfg.Includes {
		incPath := inc
		if !filepath.IsAbs(incPath) {
			incPath = filepath.Join(baseDir, incPath)
		}
		if seen[incPath] {
			continue
		}
		seen[incPath] = true
		incCfg, err := LoadConfig(incPath)
		if err != nil {
			return nil, fmt.Errorf("include %s: %w", incPath, err)
		}
		cfg.Providers = append(cfg.Providers, incCfg.Providers...)
		cfg.Skills = append(cfg.Skills, incCfg.Skills...)
		cfg.Mounts = append(cfg.Mounts, incCfg.Mounts...)
	}
	cfg.Includes = nil // merged; no need to keep them

	return &cfg, nil
}

// BuildFS creates a FileSystem from the config, registering providers and
// mounting entries. On error the returned filesystem may be partially built.
func (c *Config) BuildFS() (*core.FileSystem, error) {
	fs := core.NewFS(core.GlobalConfig{SkillsRoot: c.SkillsRoot})

	// Generate skill files
	for _, sc := range c.Skills {
		if err := fs.Skills().Generate(sc); err != nil {
			return nil, fmt.Errorf("generate skill %s: %w", sc.Name, err)
		}
		if err := fs.MountSkillAtRoot(sc); err != nil {
			return nil, fmt.Errorf("mount skill %s at root: %w", sc.Name, err)
		}
	}

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

// Reload applies configuration changes to an existing filesystem.
// It unmounts removed entries, remounts modified entries, and mounts new ones.
func (c *Config) Reload(fs *core.FileSystem) error {
	current := fs.Snapshot()

	desired := make([]core.MountEntry, 0, len(c.Mounts))
	for _, mc := range c.Mounts {
		entry, err := mc.toMountEntry()
		if err != nil {
			return fmt.Errorf("mount %s: %w", mc.Path, err)
		}
		entry.Path = mc.Path
		desired = append(desired, entry)
	}

	diff := core.DiffSnapshots(current, desired)
	for _, e := range diff.Removed {
		if err := fs.Unmount(e.Path); err != nil {
			return fmt.Errorf("unmount %s: %w", e.Path, err)
		}
	}
	for _, ch := range diff.Modified {
		if err := fs.Unmount(ch.Path); err != nil {
			return fmt.Errorf("unmount %s: %w", ch.Path, err)
		}
		if err := fs.Mount(ch.New.Path, ch.New); err != nil {
			return fmt.Errorf("mount %s: %w", ch.New.Path, err)
		}
	}
	for _, e := range diff.Added {
		if err := fs.Mount(e.Path, e); err != nil {
			return fmt.Errorf("mount %s: %w", e.Path, err)
		}
	}
	return nil
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
		if mc.Mode == "" {
			mode = uint32(0o755)
		}
	case "dynamic_dir":
		kind = core.KindDynamicDir
		if mc.Mode == "" {
			mode = uint32(0o755)
		}
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
			writeCap := &core.CapConfig{ProviderID: pid, Action: mc.Write}
			if mc.WriteParams == "json" {
				writeCap.ParamsFn = func(pathParams map[string]string, payload []byte, ctx core.OpContext) (map[string]interface{}, error) {
					params := make(map[string]interface{}, len(pathParams))
					for k, v := range pathParams {
						params[k] = v
					}
					if len(payload) > 0 {
						var body map[string]interface{}
						if err := json.Unmarshal(payload, &body); err != nil {
							return nil, fmt.Errorf("write to %s expects a valid JSON object; got %q: %w", mc.Path, string(payload), err)
						}
						for k, v := range body {
							params[k] = v
						}
					}
					return params, nil
				}
			}
			entry.Ops[core.OpWrite] = writeCap
		}
		entry.Serial = mc.Serial
	case core.KindDynamicDir:
		entry.Ops = make(map[core.OpCode]*core.CapConfig)
		pid := mc.Provider
		if pid == "" {
			pid = "remote"
		}
		if mc.Read == "" {
			return core.MountEntry{}, fmt.Errorf("dynamic_dir mount %q requires a read action", mc.Path)
		}
		entry.Ops[core.OpRead] = &core.CapConfig{ProviderID: pid, Action: mc.Read}
		entry.Serial = mc.Serial
	}

	return entry, nil
}
