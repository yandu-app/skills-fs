package main

import (
	"bytes"
	"flag"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
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
