package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

// Provider wraps a core.Provider with an in-memory TTL cache.
type Provider struct {
	inner core.Provider
	ttl   time.Duration
	mu    sync.RWMutex
	store map[string]entry
}

type entry struct {
	result  *core.ProviderResult
	expires time.Time
}

// New wraps the given provider with a cache of the specified TTL.
func New(inner core.Provider, ttl time.Duration) *Provider {
	return &Provider{
		inner: inner,
		ttl:   ttl,
		store: make(map[string]entry),
	}
}

// ID returns the wrapped provider's identifier.
func (p *Provider) ID() string { return p.inner.ID() }

// Invoke delegates to the inner provider only if the result is not cached.
func (p *Provider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	key := cacheKey(action, params)

	p.mu.RLock()
	e, ok := p.store[key]
	p.mu.RUnlock()
	if ok && time.Now().Before(e.expires) {
		return &core.ProviderResult{
			Data:        append([]byte(nil), e.result.Data...),
			Meta:        copyMap(e.result.Meta),
			ContentType: e.result.ContentType,
		}, nil
	}

	res, err := p.inner.Invoke(ctx, action, params)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.store[key] = entry{result: res, expires: time.Now().Add(p.ttl)}
	p.mu.Unlock()
	return res, nil
}

// Invalidate removes all cached entries.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	p.store = make(map[string]entry)
	p.mu.Unlock()
}

func cacheKey(action string, params map[string]interface{}) string {
	b, _ := json.Marshal(params)
	h := sha256.New()
	h.Write([]byte(action))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
