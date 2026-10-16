package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// Platform-neutral rendering of unit files. We keep these here (rather than
// in launchd_darwin.go / systemd_linux.go) so the golden-file tests can run
// on any GOOS. The platform-specific files only own the exec.Command paths
// that actually shell out to launchctl/systemctl.

// RenderLaunchdPlist serializes a Spec into a launchd plist. We hand-render
// rather than using encoding/plist because launchd is picky about element
// order and the DOCTYPE; pinning the output makes the golden tests stable
// across Go versions.
func RenderLaunchdPlist(spec Spec) ([]byte, error) {
	wd := spec.WorkingDir
	if wd == "" {
		wd = "/"
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	plistKey(&b, "Label")
	plistString(&b, spec.Label)
	plistKey(&b, "ProgramArguments")
	b.WriteString("    <array>\n")
	for _, a := range append([]string{spec.BinaryPath}, spec.Args...) {
		b.WriteString("        ")
		plistString(&b, a)
	}
	b.WriteString("    </array>\n")
	plistKey(&b, "RunAtLoad")
	b.WriteString("    <true/>\n")
	plistKey(&b, "KeepAlive")
	b.WriteString("    <true/>\n")
	plistKey(&b, "WorkingDirectory")
	plistString(&b, wd)
	if spec.LogPath != "" {
		plistKey(&b, "StandardOutPath")
		plistString(&b, spec.LogPath)
	}
	if spec.ErrLogPath != "" {
		plistKey(&b, "StandardErrorPath")
		plistString(&b, spec.ErrLogPath)
	}
	if len(spec.Env) > 0 {
		plistKey(&b, "EnvironmentVariables")
		b.WriteString("    <dict>\n")
		for _, k := range sortedKeys(spec.Env) {
			b.WriteString("        ")
			plistKey(&b, k)
			b.WriteString("        ")
			plistString(&b, spec.Env[k])
		}
		b.WriteString("    </dict>\n")
	}
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.Bytes(), nil
}

// RenderSystemdUnit serializes a Spec into a systemd .service file.
func RenderSystemdUnit(spec Spec) ([]byte, error) {
	wd := spec.WorkingDir
	if wd == "" {
		wd = "/"
	}
	var b bytes.Buffer
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", escapeSystemdValue(spec.Label))
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", escapeSystemdValue(wd))
	// systemd quotes argv entries with embedded whitespace by wrapping
	// them in double quotes; backslash and double quote inside the value
	// must be escaped. Most of our args (paths, ports, hex tokens) have
	// no special chars, but we handle the general case so a future bind
	// like "::1" or a path with a space won't silently break.
	fmt.Fprintf(&b, "ExecStart=%s", quoteSystemd(spec.BinaryPath))
	for _, a := range spec.Args {
		b.WriteString(" ")
		b.WriteString(quoteSystemd(a))
	}
	b.WriteString("\n")
	if spec.LogPath != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\n", escapeSystemdValue(spec.LogPath))
	}
	if spec.ErrLogPath != "" {
		fmt.Fprintf(&b, "StandardError=append:%s\n", escapeSystemdValue(spec.ErrLogPath))
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=2\n")
	for _, k := range sortedKeys(spec.Env) {
		fmt.Fprintf(&b, "Environment=%s\n", quoteSystemdEnv(k, spec.Env[k]))
	}
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.Bytes(), nil
}

func plistKey(b *bytes.Buffer, k string) {
	b.WriteString("    <key>")
	_ = xml.EscapeText(b, []byte(k))
	b.WriteString("</key>\n")
}

func plistString(b *bytes.Buffer, s string) {
	b.WriteString("<string>")
	_ = xml.EscapeText(b, []byte(s))
	b.WriteString("</string>\n")
}

// escapeSystemdValue neutralizes the two things that can change a unit
// file's meaning inside any value: a newline (starts a new directive) and %
// (a specifier expansion).
func escapeSystemdValue(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.ReplaceAll(s, "%", "%%")
}

func quoteSystemd(s string) string {
	s = escapeSystemdValue(s)
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// quoteSystemdEnv renders one Environment= assignment per systemd.exec(5):
// an assignment containing whitespace or quote/backslash characters must be
// wrapped in double quotes (with \ and " escaped), and a literal % must be
// doubled because unit files expand %-specifiers. Plain values stay bare so
// the common case reads naturally.
func quoteSystemdEnv(k, v string) string {
	v = strings.ReplaceAll(v, "\n", " ")
	assign := k + "=" + strings.ReplaceAll(v, "%", "%%")
	if !strings.ContainsAny(assign, " \t\"'\\") {
		return assign
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(assign) + `"`
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
