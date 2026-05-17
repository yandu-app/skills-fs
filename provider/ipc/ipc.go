package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

// Provider invokes a subprocess for each action, sending the request as JSON
// to stdin and reading a JSON response from stdout.
type Provider struct {
	id      string
	cmd     string
	args    []string
	env     []string
	dir     string
	timeout time.Duration
}

// NewProvider creates an IPC provider that runs cmd with the given arguments.
// Each Invoke spawns a new subprocess; the command receives the action and
// params as a single JSON line on stdin and must print a JSON response on
// stdout. The response format is:
//
//	{"data":"...","contentType":"...","error":"..."}
//
// If the error field is non-empty, Invoke returns an error. The data field
// is forwarded as ProviderResult.Data.
func NewProvider(id, cmd string, args ...string) *Provider {
	return &Provider{
		id:      id,
		cmd:     cmd,
		args:    args,
		timeout: 30 * time.Second,
	}
}

// WithEnv sets additional environment variables for the subprocess.
func (p *Provider) WithEnv(env []string) *Provider {
	p.env = env
	return p
}

// WithDir sets the working directory for the subprocess.
func (p *Provider) WithDir(dir string) *Provider {
	p.dir = dir
	return p
}

// WithTimeout sets the maximum duration to wait for a subprocess response.
func (p *Provider) WithTimeout(d time.Duration) *Provider {
	p.timeout = d
	return p
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return p.id }

// Invoke spawns the subprocess, sends the request as JSON, and returns the
// parsed response.
func (p *Provider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	reqBody, err := json.Marshal(ipcRequest{Action: action, Params: params})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.cmd, p.args...)
	cmd.Dir = p.dir
	if len(p.env) > 0 {
		cmd.Env = append(cmd.Environ(), p.env...)
	}
	cmd.Stdin = bytes.NewReader(reqBody)

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ipc provider %s exited %d: %s", p.id, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("ipc provider %s failed: %w", p.id, err)
	}

	var resp ipcResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("ipc provider %s returned invalid JSON: %w", p.id, err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("ipc provider %s returned error: %s", p.id, resp.Error)
	}
	return &core.ProviderResult{
		Data:        resp.Data,
		ContentType: resp.ContentType,
	}, nil
}

type ipcRequest struct {
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params"`
}

type ipcResponse struct {
	Data        []byte `json:"data"`
	ContentType string `json:"contentType,omitempty"`
	Error       string `json:"error,omitempty"`
}
