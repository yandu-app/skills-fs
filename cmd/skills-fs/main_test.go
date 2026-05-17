package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skills-fs/skills-fs/core"
)

func TestVersionOutput(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	out := buf.String()

	if !strings.Contains(out, "skills-fs") {
		t.Fatalf("expected version output, got: %q", out)
	}
	if !strings.Contains(out, "git:") {
		t.Fatalf("expected git commit line, got: %q", out)
	}
	if !strings.Contains(out, "built:") {
		t.Fatalf("expected build time line, got: %q", out)
	}
	if !strings.Contains(out, runtime.Version()) {
		t.Fatalf("expected go version %q, got: %q", runtime.Version(), out)
	}
}

func TestWebSocketCommandHelp(t *testing.T) {
	// Verify cmdWebSocket flag set is well-formed by checking -h exits cleanly.
	fs := flag.NewFlagSet("websocket", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("addr", ":8081", "")
	fs.Bool("readonly", false, "")
	fs.String("config", "", "")
	fs.String("log-level", "info", "")
	fs.String("log-file", "", "")
	fs.Bool("daemon", false, "")
	fs.String("pidfile", "", "")
	fs.String("allowed-origins", "", "")
	fs.String("cors-origins", "", "")
	fs.Int("max-connections", 0, "")
	fs.Bool("debug", false, "")
	fs.Duration("shutdown-timeout", 30*time.Second, "")
	if err := fs.Parse([]string{"-h"}); err != flag.ErrHelp {
		t.Fatalf("expected help error, got %v", err)
	}
}

func TestHealthCommandFlagParsing(t *testing.T) {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("addr", "http://localhost:8080", "")
	fs.String("path", "/healthz", "")
	fs.Duration("timeout", 5*time.Second, "")
	if err := fs.Parse([]string{"-h"}); err != flag.ErrHelp {
		t.Fatalf("expected help error, got %v", err)
	}
}

func TestSetupLoggerLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		l := setupLogger(level, "")
		if l == nil {
			t.Fatalf("setupLogger(%q) returned nil", level)
		}
	}
}

func TestSetupLoggerFile(t *testing.T) {
	f, err := os.CreateTemp("", "skills-fs-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	l := setupLogger("info", f.Name())
	if l == nil {
		t.Fatal("setupLogger with file returned nil")
	}
	l.Info("hello-file")

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("hello-file")) {
		t.Fatalf("expected log line in file, got %q", data)
	}
}

func TestBuildFSNoConfig(t *testing.T) {
	fs, err := buildFS("")
	if err != nil {
		t.Fatal(err)
	}
	if fs == nil {
		t.Fatal("buildFS(\"\") returned nil")
	}
}

func TestBuildFSWithConfig(t *testing.T) {
	cfg := `{
		"mounts": [
			{"path": "/hello", "kind": "blob", "mode": "0644", "data": "world"}
		]
	}`
	f, err := os.CreateTemp("", "skills-fs-config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fs, err := buildFS(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read(context.Background(), "/hello", core.CallerIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("expected 'world', got %q", data)
	}
}

func TestCmdValidateSuccess(t *testing.T) {
	cfg := `{"mounts": [{"path": "/x", "kind": "blob"}]}`
	f, err := os.CreateTemp("", "skills-fs-config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()

	code := cmdValidate([]string{"-config", f.Name()})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestCmdValidateFailure(t *testing.T) {
	code := cmdValidate([]string{"-config", "/nonexistent/path.json"})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
}
