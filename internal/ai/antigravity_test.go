package ai

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeAgy installs a fake `agy` on PATH whose body is the given shell
// script, and returns nothing — Run() locates it via exec.LookPath.
func writeFakeAgy(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake agy requires a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake agy: %v", err)
	}
	t.Setenv("PATH", dir)
}

func TestAntigravityRunStreamsOutputAndDeliversPrompt(t *testing.T) {
	// Echo the -p argument (the prompt) so we can assert it was delivered as an
	// argv value, not stdin.
	writeFakeAgy(t, `printf 'answer for: %s' "$2"`)

	var out bytes.Buffer
	if err := (antigravityProvider{}).Run(context.Background(), "why is my pod pending", &out, RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "answer for: why is my pod pending" {
		t.Fatalf("output = %q, prompt not delivered as -p arg", got)
	}
}

func TestAntigravityRunSurfacesStderr(t *testing.T) {
	writeFakeAgy(t, `echo "model unavailable" >&2; exit 3`)

	var out bytes.Buffer
	err := (antigravityProvider{}).Run(context.Background(), "hi", &out, RunOpts{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error %q should surface stderr", err)
	}
}

func TestAntigravityRunNilWriter(t *testing.T) {
	if err := (antigravityProvider{}).Run(context.Background(), "hi", nil, RunOpts{}); err == nil {
		t.Fatal("expected error for nil writer")
	}
}
