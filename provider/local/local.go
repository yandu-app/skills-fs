package local

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/skills-fs/skills-fs/core"
)

// Provider executes local shell commands with context cancellation.
type Provider struct{}

// NewProvider creates a local shell provider.
func NewProvider() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return "local" }

// Invoke runs a local command. The action is the command to execute.
// Supported params:
//   - "args": []string — command arguments
//   - "env":  []string — extra environment variables (KEY=VALUE)
//   - "dir":  string   — working directory
//   - "stdin": string  — data written to the command's standard input
func (p *Provider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	args := []string{}
	if v, ok := params["args"].([]string); ok {
		args = v
	}

	cmd := exec.CommandContext(ctx, action, args...)

	if v, ok := params["env"].([]string); ok {
		cmd.Env = append(cmd.Environ(), v...)
	}
	if v, ok := params["dir"].(string); ok && v != "" {
		cmd.Dir = v
	}
	if v, ok := params["stdin"].(string); ok {
		cmd.Stdin = bytes.NewBufferString(v)
	}

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("local provider: %s exited %d: %s", action, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("local provider: %s: %w", action, err)
	}
	return &core.ProviderResult{Data: out}, nil
}
