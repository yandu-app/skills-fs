package core

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
)

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type SkillGenerator struct {
	root   string
	mu     sync.RWMutex
	skills map[string]SkillConfig
}

func NewSkillGenerator(root string) *SkillGenerator {
	return &SkillGenerator{root: root, skills: make(map[string]SkillConfig)}
}

func (g *SkillGenerator) Generate(cfg SkillConfig) error {
	if g.root == "" {
		return posix(EINVAL, OpWrite, "skillsRoot", nil)
	}
	if err := validateSkillConfig(cfg); err != nil {
		return err
	}
	dir := filepath.Join(g.root, cfg.Name)
	// #nosec G301 -- skill directories are intentionally browsable.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var body bytes.Buffer
	body.WriteString("---\n")
	body.WriteString("name: " + cfg.Name + "\n")
	body.WriteString("description: " + cfg.Description + "\n")
	if cfg.Version != "" {
		body.WriteString("version: " + cfg.Version + "\n")
	}
	if cfg.Author != "" {
		body.WriteString("author: " + cfg.Author + "\n")
	}
	if cfg.License != "" {
		body.WriteString("license: " + cfg.License + "\n")
	}
	if len(cfg.Platforms) > 0 {
		body.WriteString("platforms: [" + strings.Join(cfg.Platforms, ", ") + "]\n")
	}
	if cfg.Compatibility != "" {
		body.WriteString("compatibility: " + cfg.Compatibility + "\n")
	}
	if len(cfg.AllowedTools) > 0 {
		body.WriteString("allowed-tools:\n")
		for _, tool := range cfg.AllowedTools {
			body.WriteString("  - " + tool + "\n")
		}
	}
	if len(cfg.Metadata) > 0 {
		keys := make([]string, 0, len(cfg.Metadata))
		for k := range cfg.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		body.WriteString("metadata:\n")
		for _, k := range keys {
			body.WriteString("  " + k + ": " + cfg.Metadata[k] + "\n")
		}
	}
	body.WriteString("---\n\n")
	tpl, err := template.New("skill").Parse(cfg.BodyTemplate)
	if err != nil {
		return err
	}
	if err := tpl.Execute(&body, cfg); err != nil {
		return err
	}
	if body.Len() == 0 {
		return posix(EINVAL, OpWrite, cfg.Name, nil)
	}
	// #nosec G306 -- skill metadata is intentionally world-readable.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body.Bytes(), 0o644); err != nil {
		return err
	}
	if cfg.AgentsTemplate != "" {
		var agentsBody bytes.Buffer
		agentsBody.WriteString("---\n")
		agentsBody.WriteString("name: " + cfg.Name + "\n")
		agentsBody.WriteString("description: " + cfg.Description + "\n")
		agentsBody.WriteString("---\n\n")
		agentsTpl, err := template.New("agents").Parse(cfg.AgentsTemplate)
		if err != nil {
			return err
		}
		if err := agentsTpl.Execute(&agentsBody, cfg); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), agentsBody.Bytes(), 0o644); err != nil {
			return err
		}
	}
	g.mu.Lock()
	g.skills[cfg.Name] = cfg
	g.mu.Unlock()
	return nil
}

func (g *SkillGenerator) Remove(name string) error {
	if g.root == "" {
		return nil
	}
	if !skillNameRE.MatchString(name) {
		return posix(EINVAL, OpWrite, name, nil)
	}
	if err := os.RemoveAll(filepath.Join(g.root, name)); err != nil {
		return err
	}
	g.mu.Lock()
	delete(g.skills, name)
	g.mu.Unlock()
	return nil
}

func (g *SkillGenerator) List() []SkillConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]SkillConfig, 0, len(g.skills))
	for _, cfg := range g.skills {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *SkillGenerator) Exists(name string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.skills[name]
	return ok
}

func (g *SkillGenerator) ReadSkillFile(name string) ([]byte, error) {
	if !skillNameRE.MatchString(name) {
		return nil, posix(EINVAL, OpRead, name, nil)
	}
	g.mu.RLock()
	_, ok := g.skills[name]
	g.mu.RUnlock()
	if !ok {
		return nil, posix(ENOENT, OpRead, name, nil)
	}
	data, err := os.ReadFile(filepath.Join(g.root, name, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func validateSkillConfig(cfg SkillConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if !skillNameRE.MatchString(cfg.Name) {
		return posix(EINVAL, OpWrite, cfg.Name, nil)
	}
	if len(cfg.Description) < 1 || len(cfg.Description) > 1024 {
		return posix(EINVAL, OpWrite, cfg.Name, nil)
	}
	if len(cfg.Compatibility) > 500 {
		return posix(EINVAL, OpWrite, cfg.Name, nil)
	}
	return nil
}
