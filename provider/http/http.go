package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/skills-fs/skills-fs/core"
)

// Provider forwards Invoke calls to an HTTP endpoint as JSON POST requests.
type Provider struct {
	id      string
	baseURL string
	client  *http.Client
}

// NewProvider creates an HTTP provider that POSTs to baseURL.
func NewProvider(id, baseURL string) *Provider {
	return &Provider{
		id:      id,
		baseURL: baseURL,
		client:  http.DefaultClient,
	}
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return p.id }

// Invoke sends action and params as a JSON POST to the configured baseURL.
// The response body is returned as ProviderResult.Data and the Content-Type
// header is forwarded as ProviderResult.ContentType.
func (p *Provider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	body, err := json.Marshal(invokeRequest{Action: action, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http provider %s returned %d: %s", p.id, resp.StatusCode, string(msg))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &core.ProviderResult{
		Data:        data,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

type invokeRequest struct {
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params"`
}
