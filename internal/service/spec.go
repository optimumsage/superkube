// Package service installs and uninstalls superkube components as OS-managed
// background services. Backends today: launchd LaunchAgents (macOS) and
// systemd --user units (Linux). The package only knows how to render unit
// files and shell out to launchctl/systemctl — it does not know about
// superkube's HTTP server. Callers (e.g. internal/cli/web_install.go) build a
// Spec, hand it to NewManager(), and let the backend handle the OS dance.
package service

import "errors"

// Spec is the full description of a service to install. Everything the
// backend needs to render its unit file is here; nothing else is consulted.
type Spec struct {
	// Label is the unique identifier for the service. Must be reverse-DNS
	// for launchd (e.g. "com.optimumsage.superkube.web") and a valid systemd
	// unit basename (without the .service suffix).
	Label string
	// BinaryPath is the absolute path to the executable. Must be absolute
	// because launchd resolves PATH minimally and systemd ignores it.
	BinaryPath string
	// Args are the arguments passed to BinaryPath. Not shell-quoted —
	// each entry is one argv slot.
	Args []string
	// Env is environment passed through to the process. Keys are pruned
	// to those actually set in the parent env at install time.
	Env map[string]string
	// LogPath is where stdout is redirected. The directory is created if
	// it does not exist.
	LogPath string
	// ErrLogPath is where stderr is redirected. Same directory rule.
	ErrLogPath string
	// WorkingDir is the cwd for the service. Defaults to "/" if empty —
	// services should not depend on the install-time cwd.
	WorkingDir string
}

// State is a snapshot of one service's current OS-level status.
type State struct {
	// Installed reports whether the unit file is present on disk.
	Installed bool
	// Loaded reports whether the OS supervisor (launchd/systemd) knows
	// about the unit. Distinct from Running because a unit can be loaded
	// and idle (rare for our case) or installed but not yet loaded.
	Loaded bool
	// Running reports whether a process is currently executing for the
	// service. False if Installed is false.
	Running bool
	// PID is the main process id when Running. Zero otherwise.
	PID int
	// UnitPath is the absolute path to the on-disk unit file (plist on
	// macOS, .service on Linux). Empty when Installed is false.
	UnitPath string
}

// ErrUnsupportedPlatform is returned by NewManager on a runtime.GOOS we don't
// support (anything other than darwin or linux today). Callers should print a
// friendly message rather than a stack trace.
var ErrUnsupportedPlatform = errors.New("service management is not supported on this platform")

// ErrNotInstalled is returned by Uninstall/Status when the unit file is
// absent. Callers usually treat this as a clean "nothing to do" exit.
var ErrNotInstalled = errors.New("service is not installed")

// ErrAlreadyInstalled is returned by Install when a unit file already exists
// and Force was not set. The CLI surfaces it as a hint to pass --force.
var ErrAlreadyInstalled = errors.New("service is already installed")

// Manager is the platform-agnostic interface implemented by the launchd and
// systemd backends. Implementations are constructed by NewManager.
type Manager interface {
	// Install writes the unit file and starts the service. Returns
	// ErrAlreadyInstalled if a unit already exists and force is false.
	Install(spec Spec, force bool) error
	// Uninstall stops the service and removes the unit file. Returns
	// ErrNotInstalled when there's nothing to remove.
	Uninstall(label string) error
	// Status reports the current on-disk + runtime state.
	Status(label string) (State, error)
	// UnitPath returns the path the backend would write the unit to for
	// the given label. Used by `sk web status` so we can render the path
	// even when nothing is installed yet.
	UnitPath(label string) string
}
