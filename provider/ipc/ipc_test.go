package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestIPCProviderWithEnvAndDir(t *testing.T) {
	p := NewProvider("ipc-cfg", "sh", "-c", "echo ok").
		WithEnv([]string{"EXTRA=val"}).
		WithDir("/")

	if p.env[0] != "EXTRA=val" {
		t.Fatalf("unexpected env %v", p.env)
	}
	if p.dir != "/" {
		t.Fatalf("unexpected dir %q", p.dir)
	}
}

func TestIPCProviderInvokeSuccess(t *testing.T) {
	// Subprocess that echoes a valid JSON response with base64-encoded data.
	resp := ipcResponse{Data: []byte("hello-ipc"), ContentType: "text/plain"}
	respJSON, _ := json.Marshal(resp)
	p := NewProvider("ipc-echo", "sh", "-c", fmt.Sprintf("echo '%s'", respJSON))

	result, err := p.Invoke(context.Background(), "greet", map[string]interface{}{"name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Data) != "hello-ipc" {
		t.Fatalf("unexpected data %q", result.Data)
	}
	if result.ContentType != "text/plain" {
		t.Fatalf("unexpected content type %q", result.ContentType)
	}
}

func TestIPCProviderInvokeErrorResponse(t *testing.T) {
	resp := ipcResponse{Error: "bad-action"}
	respJSON, _ := json.Marshal(resp)
	p := NewProvider("ipc-err", "sh", "-c", fmt.Sprintf("echo '%s'", respJSON))

	_, err := p.Invoke(context.Background(), "bad", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "ipc provider ipc-err returned error: bad-action" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIPCProviderInvokeExitError(t *testing.T) {
	p := NewProvider("ipc-fail", "sh", "-c", "echo fail-msg >&2; exit 1")

	_, err := p.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIPCProviderInvokeInvalidJSON(t *testing.T) {
	p := NewProvider("ipc-badjson", "sh", "-c", "echo 'not-json'")

	_, err := p.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIPCProviderInvokeTimeout(t *testing.T) {
	p := NewProvider("ipc-slow", "sh", "-c", "sleep 10").
		WithTimeout(50 * time.Millisecond)

	_, err := p.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestIPCProviderID(t *testing.T) {
	p := NewProvider("my-id", "sh")
	if p.ID() != "my-id" {
		t.Fatalf("expected ID my-id, got %s", p.ID())
	}
}

func TestIPCProviderInvokeContextCancellation(t *testing.T) {
	p := NewProvider("ipc-cancel", "sh", "-c", "sleep 10")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Invoke(ctx, "x", nil)
		done <- err
	}()

	// Give the subprocess time to start before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Invoke did not return after cancellation")
	}
}

func TestIPCProviderInvokeCommandNotFound(t *testing.T) {
	p := NewProvider("ipc-missing", "/nonexistent-binary-that-does-not-exist")

	_, err := p.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err == context.DeadlineExceeded {
		t.Fatal("expected non-deadline error")
	}
}

func TestIPCProviderReadsRequestFromStdin(t *testing.T) {
	// Python script reads stdin JSON and echoes action back as data.
	script := `
import sys, json, base64
req = json.load(sys.stdin)
resp = {"data": base64.b64encode(req["action"].encode("utf-8")).decode("ascii"), "contentType": "text/plain"}
print(json.dumps(resp))
`
	p := NewProvider("ipc-cat", "python3", "-c", script)

	result, err := p.Invoke(context.Background(), "echo-action", map[string]interface{}{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Data) != "echo-action" {
		t.Fatalf("expected echoed action, got %q", result.Data)
	}
}
