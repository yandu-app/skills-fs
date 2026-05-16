package webdav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/skills-fs/skills-fs/adapter"
	"github.com/skills-fs/skills-fs/core"
)

func TestWebDAVGetAndPut(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o666, BlobData: []byte("hello")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()

	// GET
	resp, err := http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "hello" {
		t.Fatalf("GET body = %q", string(data))
	}

	// PUT
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", bytes.NewReader([]byte("world")))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	// GET again
	resp, err = http.Get(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ = io.ReadAll(resp.Body)
	if string(data) != "world" {
		t.Fatalf("GET after PUT = %q", string(data))
	}
}

func TestWebDAVReadOnlyRejectsPut(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{ReadOnly: true})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodPut, baseURL+"/blob", bytes.NewReader([]byte("y")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestWebDAVHead(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	if err := fs.Mount("/blob", core.MountEntry{Kind: core.KindBlob, Mode: 0o444, BlobData: []byte("head-test")}); err != nil {
		t.Fatal(err)
	}

	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Head(baseURL + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Length") != "9" {
		t.Fatalf("Content-Length = %q", resp.Header.Get("Content-Length"))
	}
}

func TestWebDAVNotFound(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	resp, err := http.Get(baseURL + "/missing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWebDAVOptions(t *testing.T) {
	fs := core.NewFS(core.GlobalConfig{})
	server := New(fs, "127.0.0.1:0", adapter.MountOptions{})
	if err := server.Mount(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Unmount(context.Background())

	baseURL := "http://" + server.ln.Addr().String()
	req, _ := http.NewRequest(http.MethodOptions, baseURL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS status = %d", resp.StatusCode)
	}
	if resp.Header.Get("DAV") != "1" {
		t.Fatalf("DAV header = %q", resp.Header.Get("DAV"))
	}
}
