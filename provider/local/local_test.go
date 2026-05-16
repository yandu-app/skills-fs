package local

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocalProviderID(t *testing.T) {
	p := NewProvider()
	if p.ID() != "local" {
		t.Fatalf("expected id 'local', got %q", p.ID())
	}
}

func TestLocalProviderEcho(t *testing.T) {
	p := NewProvider()
	res, err := p.Invoke(context.Background(), "echo", map[string]interface{}{
		"args": []string{"hello", "world"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Data), "hello world") {
		t.Fatalf("unexpected output: %q", res.Data)
	}
}

func TestLocalProviderStdin(t *testing.T) {
	p := NewProvider()
	res, err := p.Invoke(context.Background(), "cat", map[string]interface{}{
		"stdin": "pipe-data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != "pipe-data" {
		t.Fatalf("unexpected output: %q", res.Data)
	}
}

func TestLocalProviderExitError(t *testing.T) {
	p := NewProvider()
	_, err := p.Invoke(context.Background(), "false", nil)
	if err == nil {
		t.Fatal("expected error for false command")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected exit error, got: %v", err)
	}
}

func TestLocalProviderNotFound(t *testing.T) {
	p := NewProvider()
	_, err := p.Invoke(context.Background(), "/nonexistent/command-12345", nil)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestLocalProviderContextCancellation(t *testing.T) {
	p := NewProvider()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Invoke(ctx, "sleep", map[string]interface{}{
		"args": []string{"10"},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
