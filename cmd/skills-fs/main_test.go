package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
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
