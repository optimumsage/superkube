package ai

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFakeBin drops an executable stub named bin into dir so exec.LookPath can
// find it when dir is on PATH.
func writeFakeBin(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func TestDetect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executables require a POSIX shell")
	}

	cases := []struct {
		name     string
		explicit string
		env      string   // $SUPERKUBE_AI
		present  []string // fake binaries on PATH
		want     string   // provider Name(), or "" when an error is expected
		wantErr  bool
	}{
		{name: "auto prefers claude", present: []string{"claude", "agy"}, want: "claude"},
		{name: "auto falls back to antigravity", present: []string{"agy"}, want: "antigravity"},
		{name: "explicit agy", explicit: "agy", present: []string{"agy"}, want: "antigravity"},
		{name: "explicit antigravity", explicit: "antigravity", present: []string{"agy"}, want: "antigravity"},
		{name: "explicit antigravity missing", explicit: "antigravity", present: nil, wantErr: true},
		{name: "explicit claude missing", explicit: "claude", present: []string{"agy"}, wantErr: true},
		{name: "gemini no longer supported", explicit: "gemini", present: []string{"agy"}, wantErr: true},
		{name: "unknown provider", explicit: "bogus", present: []string{"claude"}, wantErr: true},
		{name: "none present", present: nil, wantErr: true},
		{name: "env selects antigravity", env: "antigravity", present: []string{"claude", "agy"}, want: "antigravity"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, b := range tc.present {
				writeFakeBin(t, dir, b)
			}
			t.Setenv("PATH", dir)
			t.Setenv("SUPERKUBE_AI", tc.env)

			p, err := Detect(tc.explicit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got provider %q", p.Name())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != tc.want {
				t.Fatalf("provider = %q, want %q", p.Name(), tc.want)
			}
		})
	}
}

func TestDetectNoProviderIsErrNoProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executables require a POSIX shell")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SUPERKUBE_AI", "")
	if _, err := Detect(""); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v", err)
	}
}

func TestDetectName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executables require a POSIX shell")
	}
	dir := t.TempDir()
	writeFakeBin(t, dir, "agy")
	t.Setenv("PATH", dir)
	t.Setenv("SUPERKUBE_AI", "")

	if got := DetectName(""); got != "antigravity" {
		t.Fatalf("DetectName = %q, want antigravity", got)
	}
	if got := DetectName("claude"); got != "(none)" {
		t.Fatalf("DetectName(claude, absent) = %q, want (none)", got)
	}
}
