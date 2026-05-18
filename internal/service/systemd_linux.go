//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// systemdManager installs services as `--user` units (no sudo). On a session
// without a running user manager (some CI containers, minimal distros) we
// surface a clear error instead of silently writing a unit that will never
// be loaded.
type systemdManager struct{}

func newManager() (Manager, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, errors.New("systemctl not found in PATH; systemd --user service installation requires systemd")
	}
	return systemdManager{}, nil
}

func (systemdManager) UnitPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", label+".service")
}

func (m systemdManager) Install(spec Spec, force bool) error {
	unitPath := m.UnitPath(spec.Label)
	if _, err := os.Stat(unitPath); err == nil {
		if !force {
			return ErrAlreadyInstalled
		}
		_ = m.Uninstall(spec.Label)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create user systemd dir: %w", err)
	}
	for _, p := range []string{spec.LogPath, spec.ErrLogPath} {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("create log dir %s: %w", filepath.Dir(p), err)
		}
	}
	body, err := RenderSystemdUnit(spec)
	if err != nil {
		return fmt.Errorf("render unit: %w", err)
	}
	if err := os.WriteFile(unitPath, body, 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	// daemon-reload so systemd picks up the new file; enable --now starts
	// it AND wires it to start on session login.
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		_ = os.Remove(unitPath)
		return fmt.Errorf("systemctl daemon-reload: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", spec.Label+".service").CombinedOutput(); err != nil {
		_ = os.Remove(unitPath)
		return fmt.Errorf("systemctl enable --now: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m systemdManager) Uninstall(label string) error {
	unitPath := m.UnitPath(label)
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return ErrNotInstalled
	}
	// Best-effort: even if disable fails, remove the file and reload so
	// `sk web install` works again. The user can re-run uninstall to
	// surface any residual error.
	_, _ = exec.Command("systemctl", "--user", "disable", "--now", label+".service").CombinedOutput()
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	_, _ = exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	return nil
}

func (m systemdManager) Status(label string) (State, error) {
	unitPath := m.UnitPath(label)
	st := State{UnitPath: unitPath}
	if _, err := os.Stat(unitPath); err == nil {
		st.Installed = true
	}
	// is-active exits non-zero when not active; we still read stdout to
	// distinguish active / activating / inactive / failed.
	out, _ := exec.Command("systemctl", "--user", "is-active", label+".service").Output()
	switch strings.TrimSpace(string(out)) {
	case "active":
		st.Loaded = true
		st.Running = true
	case "activating", "inactive", "failed":
		st.Loaded = st.Installed
	}
	if st.Running {
		out, _ := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", label+".service").Output()
		if pid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && pid > 0 {
			st.PID = pid
		}
	}
	return st, nil
}
