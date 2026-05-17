package helm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNew_HonorsHELMBIN(t *testing.T) {
	t.Setenv("PATH", "") // hide a real helm if present
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-helm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("HELM_BIN", bin)

	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.Path != bin {
		t.Fatalf("want path %q, got %q", bin, r.Path)
	}
}

func TestNew_NotInstalled(t *testing.T) {
	// Empty PATH and no HELM_BIN → ErrNotInstalled.
	t.Setenv("PATH", "")
	t.Setenv("HELM_BIN", "")

	_, err := New()
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

func TestNew_HELMBINMissing(t *testing.T) {
	t.Setenv("HELM_BIN", "/nonexistent/path/to/helm")
	_, err := New()
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled wrapped, got %v", err)
	}
}

func TestExitCodeError_Error(t *testing.T) {
	e := &ExitCodeError{Code: 42}
	if got := e.Error(); got == "" {
		t.Fatalf("want non-empty error, got %q", got)
	}
}
