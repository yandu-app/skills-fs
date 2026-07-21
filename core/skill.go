package core

import (
	"bytes"
	"regexp"
	"sort"
	"sync"
	"text/template"
)

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// skillState holds the LOGICALLY generated (in-memory, never disk-written)
// content for a skill. Keeping this out of the filesystem preserves any
// static files underneath a FUSE overlay on the skill directory.
type skillState struct {
	cfg        SkillConfig
	skillBody  []byte
	agentsBody []byte
}

type SkillGenerator struct {
	root   string // retained for API compatibility; logical generate does not write here
	mu     sync.RWMutex
	skills map[string]*skillState
}

func NewSkillGenerator(root string) *SkillGenerator {
	return &SkillGenerator{root: root, skills: make(map[string]*skillState)}
}

// Generate renders a skill's SKILL.md (and AGENTS.md when AgentsTemplate is
// set) INTO MEMORY only. It never writes to disk, so a FUSE overlay on the
// skill directory can serve the generated content while the static files
// underneath remain intact (unmount restores them).
func (g *SkillGenerator) Generate(cfg SkillConfig) error {
	if err := validateSkillConfig(cfg); err != nil {
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
		body.WriteString("platforms: [" + joinPlatforms(cfg.Platforms) + "]\n")
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
	if cfg.BodyTemplate != "" {
		tpl, err := template.New("skill").Parse(cfg.BodyTemplate)
		if err != nil {
			return err
		}
		if err := tpl.Execute(&body, cfg); err != nil {
			return err
		}
	}
	st := &skillState{cfg: cfg, skillBody: append([]byte(nil), body.Bytes()...)}
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
		st.agentsBody = append([]byte(nil), agentsBody.Bytes()...)
	}
	g.mu.Lock()
	g.skills[cfg.Name] = st
	g.mu.Unlock()
	return nil
}

func joinPlatforms(p []string) string {
	if len(p) == 0 {
		return ""
	}
	out := p[0]
	for _, s := range p[1:] {
		out += ", " + s
	}
	return out
}

// Remove drops a skill from memory. It never touches the filesystem, so it
// cannot delete a static skill directory overlaid by FUSE.
func (g *SkillGenerator) Remove(name string) error {
	if !skillNameRE.MatchString(name) {
		return posix(EINVAL, OpWrite, name, nil)
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
	for _, st := range g.skills {
		out = append(out, st.cfg)
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

// ReadSkillFile returns the in-memory generated SKILL.md body for a skill.
func (g *SkillGenerator) ReadSkillFile(name string) ([]byte, error) {
	if !skillNameRE.MatchString(name) {
		return nil, posix(EINVAL, OpRead, name, nil)
	}
	g.mu.RLock()
	st, ok := g.skills[name]
	g.mu.RUnlock()
	if !ok {
		return nil, posix(ENOENT, OpRead, name, nil)
	}
	return st.skillBody, nil
}

// ReadAgentsFile returns the in-memory generated AGENTS.md body (empty if no
// agents template was set).
func (g *SkillGenerator) ReadAgentsFile(name string) ([]byte, error) {
	if !skillNameRE.MatchString(name) {
		return nil, posix(EINVAL, OpRead, name, nil)
	}
	g.mu.RLock()
	st, ok := g.skills[name]
	g.mu.RUnlock()
	if !ok {
		return nil, posix(ENOENT, OpRead, name, nil)
	}
	return st.agentsBody, nil
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
