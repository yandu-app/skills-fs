package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

type mockProvider struct {
	id      string
	calls   int
	result  []byte
	err     error
}

func (m *mockProvider) ID() string { return m.id }

func (m *mockProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &core.ProviderResult{Data: m.result}, nil
}

func TestCacheHit(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("hello")}
	p := New(inner, time.Minute)

	res1, err := p.Invoke(context.Background(), "a", map[string]interface{}{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := p.Invoke(context.Background(), "a", map[string]interface{}{"x": 1})
	if err != nil {
		t.Fatal(err)
	}

	if inner.calls != 1 {
		t.Fatalf("expected 1 call, got %d", inner.calls)
	}
	if string(res1.Data) != "hello" || string(res2.Data) != "hello" {
		t.Fatal("unexpected result data")
	}
}

func TestCacheMissDifferentAction(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, time.Minute)

	_, _ = p.Invoke(context.Background(), "a", nil)
	_, _ = p.Invoke(context.Background(), "b", nil)

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.calls)
	}
}

func TestCacheMissDifferentParams(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, time.Minute)

	_, _ = p.Invoke(context.Background(), "a", map[string]interface{}{"x": 1})
	_, _ = p.Invoke(context.Background(), "a", map[string]interface{}{"x": 2})

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.calls)
	}
}

func TestCacheExpires(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, 10*time.Millisecond)

	_, _ = p.Invoke(context.Background(), "a", nil)
	time.Sleep(20 * time.Millisecond)
	_, _ = p.Invoke(context.Background(), "a", nil)

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls after expiry, got %d", inner.calls)
	}
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	inner := &mockProvider{id: "mock", err: errors.New("boom")}
	p := New(inner, time.Minute)

	_, err1 := p.Invoke(context.Background(), "a", nil)
	_, err2 := p.Invoke(context.Background(), "a", nil)

	if err1 == nil || err2 == nil {
		t.Fatal("expected errors")
	}
	if inner.calls != 2 {
		t.Fatalf("expected 2 calls (errors not cached), got %d", inner.calls)
	}
}

func TestCacheInvalidate(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, time.Minute)

	_, _ = p.Invoke(context.Background(), "a", nil)
	p.Invalidate()
	_, _ = p.Invoke(context.Background(), "a", nil)

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls after invalidate, got %d", inner.calls)
	}
}

func TestCacheID(t *testing.T) {
	inner := &mockProvider{id: "mock"}
	p := New(inner, time.Second)
	if p.ID() != "mock" {
		t.Fatalf("expected id 'mock', got %q", p.ID())
	}
}
