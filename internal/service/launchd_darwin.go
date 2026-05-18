//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// launchdManager installs services as per-user LaunchAgents. We deliberately
// stick to the gui/$UID domain (not system/) so install never needs sudo.
// Modern macOS (10.10+) exposes `launchctl bootstrap` / `bootout`, which we
// prefer; we fall back to the legacy `load -w` / `unload -w` pair when the
// modern verbs fail (older macOS or sandboxed contexts).
type launchdManager struct{}

func newManager() (Manager, error) { return launchdManager{}, nil }

func (launchdManager) UnitPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func (m launchdManager) Install(spec Spec, force bool) error {
	plistPath := m.UnitPath(spec.Label)
	if _, err := os.Stat(plistPath); err == nil {
		if !force {
			return ErrAlreadyInstalled
		}
		// Force: tear down the old service so the new one binds clean.
		_ = m.Uninstall(spec.Label)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	for _, p := range []string{spec.LogPath, spec.ErrLogPath} {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("create log dir %s: %w", filepath.Dir(p), err)
		}
	}
	body, err := RenderLaunchdPlist(spec)
	if err != nil {
		return fmt.Errorf("render plist: %w", err)
	}
	if err := os.WriteFile(plistPath, body, 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	if err := launchctlLoad(plistPath); err != nil {
		// Roll back the plist so a failed install doesn't leave dead
		// state on disk that the user has to manually clean up.
		_ = os.Remove(plistPath)
		return err
	}
	return nil
}

func (m launchdManager) Uninstall(label string) error {
	plistPath := m.UnitPath(label)
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return ErrNotInstalled
	}
	// Best-effort unload; even if it fails we still try to remove the
	// plist so a later install is not blocked.
	_ = launchctlUnload(plistPath, label)
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (m launchdManager) Status(label string) (State, error) {
	plistPath := m.UnitPath(label)
	st := State{UnitPath: plistPath}
	if _, err := os.Stat(plistPath); err == nil {
		st.Installed = true
	}
	pid, loaded := launchctlList(label)
	st.Loaded = loaded
	if pid > 0 {
		st.Running = true
		st.PID = pid
	}
	return st, nil
}

// launchctlLoad uses `launchctl bootstrap gui/$UID <plist>` (10.10+),
// falling back to `launchctl load -w <plist>` for older systems.
func launchctlLoad(plistPath string) error {
	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid
	out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput()
	if err == nil {
		return nil
	}
	outStr := string(out)
	if strings.Contains(strings.ToLower(outStr), "already") {
		return nil
	}
	out2, err2 := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput()
	if err2 == nil {
		return nil
	}
	return fmt.Errorf("launchctl bootstrap: %s; legacy load also failed: %s",
		strings.TrimSpace(outStr), strings.TrimSpace(string(out2)))
}

func launchctlUnload(plistPath, label string) error {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + label
	if err := exec.Command("launchctl", "bootout", target).Run(); err == nil {
		return nil
	}
	return exec.Command("launchctl", "unload", "-w", plistPath).Run()
}

// launchctlList parses `launchctl list <label>` to extract the running PID.
// The output is a plist-shaped dict with a "PID" key when the process is
// alive. Absent PID means installed-but-idle. A non-zero exit code is "not
// loaded".
var listPIDLine = regexp.MustCompile(`(?m)^\s*"PID"\s*=\s*([0-9]+);`)

func launchctlList(label string) (pid int, loaded bool) {
	out, err := exec.Command("launchctl", "list", label).Output()
	if err != nil {
		return 0, false
	}
	loaded = true
	if m := listPIDLine.FindSubmatch(out); len(m) == 2 {
		n, atoiErr := strconv.Atoi(string(m[1]))
		if atoiErr == nil {
			pid = n
		}
	}
	return pid, loaded
}
