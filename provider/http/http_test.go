package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func TestHTTPProviderInvoke(t *testing.T) {
	var gotAction string
	var gotParams map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req invokeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		gotAction = req.Action
		gotParams = req.Params
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	p := NewProvider("remote", server.URL)
	result, err := p.Invoke(context.Background(), "test.action", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAction != "test.action" {
		t.Fatalf("expected action test.action, got %s", gotAction)
	}
	if gotParams["key"] != "value" {
		t.Fatalf("expected params key=value, got %+v", gotParams)
	}
	if string(result.Data) != `{"status":"ok"}` {
		t.Fatalf("unexpected data %q", result.Data)
	}
	if result.ContentType != "application/json" {
		t.Fatalf("unexpected content type %q", result.ContentType)
	}
}

func TestHTTPProviderNon2xxReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer server.Close()

	p := NewProvider("remote", server.URL)
	_, err := p.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestHTTPProviderID(t *testing.T) {
	p := NewProvider("my-id", "http://localhost")
	if p.ID() != "my-id" {
		t.Fatalf("expected ID my-id, got %s", p.ID())
	}
}

func TestHTTPProviderIntegrationWithFS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello from remote"))
	}))
	defer server.Close()

	fs := core.NewFS(core.GlobalConfig{})
	p := NewProvider("remote", server.URL)
	if err := fs.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mount("/api", core.MountEntry{
		Kind: core.KindAPI,
		Mode: 0o444,
		Ops: map[core.OpCode]*core.CapConfig{
			core.OpRead: {ProviderID: "remote", Action: "greet"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := fs.Read(context.Background(), "/api", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from remote" {
		t.Fatalf("unexpected data %q", data)
	}
}
