package main

import (
	"context"
	"os"
	"path/filepath"
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

// writeTestConfig writes cfgJSON into a temp dir and returns the config path.
func writeTestConfig(t *testing.T, cfgJSON string) (dir, path string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "skillfs-cfg-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestDataFileResolution(t *testing.T) {
	dir, path := writeTestConfig(t, `{
		"mounts": [{"path": "/x", "kind": "blob", "dataFile": "data/blob.md"}]
	}`)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "blob.md"), []byte("hello from file"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mounts[0].Data != "hello from file" {
		t.Fatalf("dataFile not resolved; Data=%q", cfg.Mounts[0].Data)
	}
	// And it flows through to the filesystem.
	fs, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.Read(context.TODO(), "/x", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello from file" {
		t.Fatalf("fs served %q", got)
	}
}

func TestDataFileOverridesInline(t *testing.T) {
	dir, path := writeTestConfig(t, `{
		"mounts": [{"path": "/x", "kind": "blob", "data": "inline", "dataFile": "f.md"}]
	}`)
	if err := os.WriteFile(filepath.Join(dir, "f.md"), []byte("from-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mounts[0].Data != "from-file" {
		t.Fatalf("dataFile should override inline; Data=%q", cfg.Mounts[0].Data)
	}
}

func TestDataFileMissingErrors(t *testing.T) {
	_, path := writeTestConfig(t, `{
		"mounts": [{"path": "/x", "kind": "blob", "dataFile": "nope.md"}]
	}`)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for missing dataFile, got nil")
	}
}

func TestBodyTemplateFileResolution(t *testing.T) {
	dir, path := writeTestConfig(t, `{
		"skills": [{"name": "demo", "description": "d", "bodyTemplateFile": "body.md", "agentsTemplateFile": "agents.md"}]
	}`)
	if err := os.WriteFile(filepath.Join(dir, "body.md"), []byte("# Body {{.Name}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents.md"), []byte("agents body"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Skills[0].BodyTemplate != "# Body {{.Name}}" {
		t.Fatalf("bodyTemplateFile not resolved; BodyTemplate=%q", cfg.Skills[0].BodyTemplate)
	}
	if cfg.Skills[0].AgentsTemplate != "agents body" {
		t.Fatalf("agentsTemplateFile not resolved; AgentsTemplate=%q", cfg.Skills[0].AgentsTemplate)
	}
}

func TestInlineDataStillWorks(t *testing.T) {
	_, path := writeTestConfig(t, `{
		"mounts": [{"path": "/x", "kind": "blob", "data": "inline-only"}]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mounts[0].Data != "inline-only" {
		t.Fatalf("inline data regressed; Data=%q", cfg.Mounts[0].Data)
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
	data, err := fs.Read(context.TODO(), "/hello", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("unexpected data %q", data)
	}
	linkData, err := fs.Read(context.TODO(), "/link", core.CallerIdentity{})
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
	_, err = fs.Read(context.TODO(), "/api", core.CallerIdentity{})
	if err == nil {
		t.Fatal("expected error because backend is not reachable")
	}
}

func TestBuildFSWriteParamsJSON(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderConfig{{ID: "remote", URL: "http://localhost:9000"}},
		Mounts: []MountConfig{
			{Path: "/clear", Kind: "api", Provider: "remote", Write: "clear_alert", WriteParams: "json"},
		},
	}
	fsys, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := []byte(`{"name": "my_alert"}`)
	err = fsys.Write(ctx, "/clear", payload, core.CallerIdentity{})
	if err == nil {
		t.Fatal("expected provider error from unreachable backend")
	}
	// A provider error means the request reached the HTTP provider after
	// ParamsFn parsed the JSON payload. EINVAL or missing-params would
	// indicate a ParamsFn or wiring bug.
	if core.IsCode(err, core.EINVAL) {
		t.Fatalf("ParamsFn should not return EINVAL for valid JSON: %v", err)
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

func TestReloadConfig(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Path: "/keep", Kind: "blob", Mode: "0644", Data: "keep"},
			{Path: "/remove", Kind: "blob", Mode: "0644", Data: "remove"},
		},
	}
	fs, err := cfg.BuildFS()
	if err != nil {
		t.Fatal(err)
	}

	// Reload with a modified config.
	cfg.Mounts = []MountConfig{
		{Path: "/keep", Kind: "blob", Mode: "0644", Data: "keep"},
		{Path: "/keep", Kind: "blob", Mode: "0644", Data: "changed"},
		{Path: "/add", Kind: "blob", Mode: "0644", Data: "add"},
	}
	if err := cfg.Reload(fs); err != nil {
		t.Fatal(err)
	}

	// /remove should be gone.
	_, err = fs.Read(context.TODO(), "/remove", core.CallerIdentity{})
	if err == nil {
		t.Fatal("expected /remove to be unmounted")
	}

	// /keep should have new data.
	data, err := fs.Read(context.TODO(), "/keep", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "changed" {
		t.Fatalf("expected 'changed', got %q", data)
	}

	// /add should exist.
	data, err = fs.Read(context.TODO(), "/add", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "add" {
		t.Fatalf("expected 'add', got %q", data)
	}
}
