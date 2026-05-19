package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

// Provider forwards Invoke calls to an HTTP endpoint as JSON POST requests.
type Provider struct {
	id             string
	baseURL        string
	client         *http.Client
	retryCount     int
	retryBaseDelay time.Duration
}

// NewProvider creates an HTTP provider that POSTs to baseURL.
func NewProvider(id, baseURL string) *Provider {
	return &Provider{
		id:      id,
		baseURL: baseURL,
		client:  http.DefaultClient,
	}
}

// WithRetry configures the provider to retry transient failures.
func (p *Provider) WithRetry(count int, baseDelay time.Duration) *Provider {
	p.retryCount = count
	p.retryBaseDelay = baseDelay
	return p
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

	var lastErr error
	for attempt := 0; attempt <= p.retryCount; attempt++ {
		if attempt > 0 {
			delay := p.retryBaseDelay * time.Duration(1<<uint(attempt-1))
			// #nosec G404 -- math/rand is sufficient for retry jitter.
			delay += time.Duration(rand.Int63n(int64(delay) / 2))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK {
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			return &core.ProviderResult{
				Data:        data,
				ContentType: resp.Header.Get("Content-Type"),
			}, nil
		}

		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("http provider %s returned %d: %s", p.id, resp.StatusCode, string(msg))
		if resp.StatusCode < 500 {
			break // client errors are not retried
		}
	}
	return nil, lastErr
}

type invokeRequest struct {
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params"`
}
