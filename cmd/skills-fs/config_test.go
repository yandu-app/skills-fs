package main

import (
	"os"
	"testing"

	"github.com/skills-fs/skills-fs/core"
)

func TestLoadConfig(t *testing.T) {
	data := `{
		"mounts": [
			{"path": "/hello", "kind": "blob", "mode": "0644", "data": "world"},
			{"path": "/api", "kind": "api", "read": "greet", "provider": "remote"}
		],
		"providers": [
			{"id": "remote", "url": "http://localhost:9000"}
		]
	}`
	f, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := LoadConfig(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(cfg.Mounts))
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Mounts[0].Path != "/hello" {
		t.Fatalf("unexpected mount path %s", cfg.Mounts[0].Path)
	}
}

func TestBuildFS(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Path: "/hello", Kind: "blob", Mode: "0644", Data: "world"},
			{Path: "/link", Kind: "link", Link: "/target"},
		},
	}
	fs, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read(nil, "/hello", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("unexpected data %q", data)
	}
	linkData, err := fs.Read(nil, "/link", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(linkData) != "/target" {
		t.Fatalf("unexpected link target %q", linkData)
	}
}

func TestBuildFSUnknownKind(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{{Path: "/x", Kind: "unknown"}},
	}
	_, err := cfg.BuildFS()
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestBuildFSInvalidMode(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{{Path: "/x", Kind: "blob", Mode: "xyz"}},
	}
	_, err := cfg.BuildFS()
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestBuildFSWithProvider(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{{ID: "remote", URL: "http://localhost:9000"}},
		Mounts: []MountConfig{
			{Path: "/api", Kind: "api", Provider: "remote", Read: "greet", Write: "create", Serial: true},
		},
	}
	fs, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}
	_, err = fs.Read(nil, "/api", core.CallerIdentity{})
	if err == nil {
		t.Fatal("expected error because backend is not reachable")
	}
}

func TestBuildFSWithDirAndStream(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Path: "/dir", Kind: "dir", Mode: "0755"},
			{Path: "/events", Kind: "stream", Mode: "0666"},
		},
	}
	fs, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.Readdir("/dir", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %+v", entries)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	f, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("not json")
	f.Close()
	_, err = LoadConfig(f.Name())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBuildFSDefaultProviderMissing(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Path: "/api", Kind: "api", Read: "greet"},
		},
	}
	_, err := cfg.BuildFS()
	if err == nil {
		t.Fatal("expected error because provider 'remote' is not registered")
	}
}
