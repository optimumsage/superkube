package service

import (
	"strings"
	"testing"
)

// TestRenderLaunchdPlist locks the plist shape so future edits can't silently
// drop a key launchd cares about (Label, KeepAlive, RunAtLoad,
// ProgramArguments). It runs on any GOOS because the renderer is platform-
// neutral; only the install/uninstall shell-outs are darwin-gated.
func TestRenderLaunchdPlist(t *testing.T) {
	spec := Spec{
		Label:      "com.optimumsage.superkube.web",
		BinaryPath: "/usr/local/bin/superkube",
		Args: []string{
			"web",
			"--bind", "127.0.0.1",
			"--port", "7070",
			"--no-open",
		},
		Env:        map[string]string{"HOME": "/Users/m", "PATH": "/usr/bin"},
		LogPath:    "/Users/m/.local/state/superkube/web.log",
		ErrLogPath: "/Users/m/.local/state/superkube/web.err",
		WorkingDir: "/",
	}
	out, err := RenderLaunchdPlist(spec)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)

	wantContains := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<!DOCTYPE plist`,
		`<plist version="1.0">`,
		`<key>Label</key>`,
		`<string>com.optimumsage.superkube.web</string>`,
		`<key>ProgramArguments</key>`,
		`<string>/usr/local/bin/superkube</string>`,
		`<string>web</string>`,
		`<string>--bind</string>`,
		`<string>127.0.0.1</string>`,
		`<string>--port</string>`,
		`<string>7070</string>`,
		`<string>--no-open</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		`<key>KeepAlive</key>`,
		`<key>WorkingDirectory</key>`,
		`<string>/</string>`,
		`<key>StandardOutPath</key>`,
		`<string>/Users/m/.local/state/superkube/web.log</string>`,
		`<key>StandardErrorPath</key>`,
		`<string>/Users/m/.local/state/superkube/web.err</string>`,
		`<key>EnvironmentVariables</key>`,
		`<key>HOME</key>`,
		`<string>/Users/m</string>`,
		`<key>PATH</key>`,
		`<string>/usr/bin</string>`,
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("plist missing %q\n--- got ---\n%s", w, s)
		}
	}

	// Env entries must be sorted so the output is stable across map
	// iterations.
	homeIdx := strings.Index(s, "<key>HOME</key>")
	pathIdx := strings.Index(s, "<key>PATH</key>")
	if homeIdx < 0 || pathIdx < 0 || homeIdx > pathIdx {
		t.Errorf("env keys should be sorted: HOME at %d, PATH at %d", homeIdx, pathIdx)
	}
}

// TestRenderLaunchdPlist_DefaultsWorkingDir confirms an empty WorkingDir
// resolves to "/" so a service launched from a user's home directory doesn't
// silently inherit a cwd that may not exist when the user is logged out.
func TestRenderLaunchdPlist_DefaultsWorkingDir(t *testing.T) {
	spec := Spec{Label: "x", BinaryPath: "/a/b"}
	out, _ := RenderLaunchdPlist(spec)
	if !strings.Contains(string(out), "<key>WorkingDirectory</key>\n<string>/</string>") {
		t.Errorf("expected WorkingDirectory=/, got:\n%s", out)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	spec := Spec{
		Label:      "superkube-web",
		BinaryPath: "/usr/local/bin/superkube",
		Args: []string{
			"web",
			"--bind", "127.0.0.1",
			"--port", "7070",
			"--no-open",
		},
		Env:        map[string]string{"HOME": "/home/m", "PATH": "/usr/bin"},
		LogPath:    "/home/m/.local/state/superkube/web.log",
		ErrLogPath: "/home/m/.local/state/superkube/web.err",
		WorkingDir: "/",
	}
	out, err := RenderSystemdUnit(spec)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)

	wantContains := []string{
		"[Unit]",
		"Description=superkube-web",
		"After=network-online.target",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=/",
		"ExecStart=/usr/local/bin/superkube web --bind 127.0.0.1 --port 7070 --no-open",
		"StandardOutput=append:/home/m/.local/state/superkube/web.log",
		"StandardError=append:/home/m/.local/state/superkube/web.err",
		"Restart=on-failure",
		"Environment=HOME=/home/m",
		"Environment=PATH=/usr/bin",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("unit missing %q\n--- got ---\n%s", w, s)
		}
	}

	// Env order should be deterministic (alphabetical).
	homeIdx := strings.Index(s, "Environment=HOME=")
	pathIdx := strings.Index(s, "Environment=PATH=")
	if homeIdx > pathIdx {
		t.Errorf("env order should be sorted: HOME at %d, PATH at %d", homeIdx, pathIdx)
	}
}

func TestRenderSystemdUnit_QuotesWhitespace(t *testing.T) {
	spec := Spec{
		Label:      "x",
		BinaryPath: "/a/b",
		Args:       []string{"--token", "needs space"},
	}
	out, _ := RenderSystemdUnit(spec)
	// The arg with a space must be quoted so systemd treats it as one
	// token rather than two.
	if !strings.Contains(string(out), `ExecStart=/a/b --token "needs space"`) {
		t.Errorf("expected quoted arg, got:\n%s", out)
	}
}

func TestQuoteSystemd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "simple"},
		{"with space", `"with space"`},
		{`with"quote`, `"with\"quote"`},
		{`with\back`, `"with\\back"`},
	}
	for _, tc := range cases {
		if got := quoteSystemd(tc.in); got != tc.want {
			t.Errorf("quoteSystemd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
