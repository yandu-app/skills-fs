package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

type mockProvider struct {
	id     string
	calls  int
	result []byte
	meta   map[string]string
	err    error
}

func (m *mockProvider) ID() string { return m.id }

func (m *mockProvider) Invoke(ctx context.Context, action string, params map[string]interface{}) (*core.ProviderResult, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &core.ProviderResult{Data: m.result, Meta: m.meta}, nil
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

	_, err := p.Invoke(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Invoke(context.Background(), "b", nil)
	if err != nil {
		t.Fatal(err)
	}

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.calls)
	}
}

func TestCacheMissDifferentParams(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, time.Minute)

	if _, err := p.Invoke(context.Background(), "a", map[string]interface{}{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Invoke(context.Background(), "a", map[string]interface{}{"x": 2}); err != nil {
		t.Fatal(err)
	}

	if inner.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.calls)
	}
}

func TestCacheExpires(t *testing.T) {
	inner := &mockProvider{id: "mock", result: []byte("ok")}
	p := New(inner, 10*time.Millisecond)

	if _, err := p.Invoke(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := p.Invoke(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}

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

	if _, err := p.Invoke(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}
	p.Invalidate()
	if _, err := p.Invoke(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}

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

func TestCacheMetaCopy(t *testing.T) {
	inner := &mockProvider{
		id:     "mock",
		result: []byte("hello"),
		meta:   map[string]string{"k": "v"},
	}
	p := New(inner, time.Minute)

	res1, err := p.Invoke(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := p.Invoke(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate the returned meta to ensure cache returns independent copies.
	res1.Meta["k"] = "mutated"
	if res2.Meta["k"] != "v" {
		t.Fatalf("expected cached meta unchanged, got %q", res2.Meta["k"])
	}
}

func TestCopyMap(t *testing.T) {
	if got := copyMap(nil); got != nil {
		t.Fatalf("copyMap(nil) = %v, want nil", got)
	}
	m := map[string]string{"a": "1", "b": "2"}
	got := copyMap(m)
	if len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("copyMap(%v) = %v", m, got)
	}
	// Ensure independent copy.
	got["a"] = "x"
	if m["a"] != "1" {
		t.Fatal("copyMap mutated source")
	}
}
