// Package config resolves filesystem paths for superkube's state, cache, and
// configuration. Honors XDG Base Directory Specification on Linux/macOS; we
// don't ship Windows in v1 so no special-casing is needed yet.
package config

import (
	"os"
	"path/filepath"
)

// StateDir returns the directory for mutable runtime state (audit log,
// previous-context pointer). Honors $XDG_STATE_HOME, falling back to
// ~/.local/state/superkube.
func StateDir() string {
	return resolveXDG("XDG_STATE_HOME", ".local/state")
}

// CacheDir returns the directory for ephemeral caches (e.g. kubectl version
// probe results when we start persisting them). Honors $XDG_CACHE_HOME.
func CacheDir() string {
	return resolveXDG("XDG_CACHE_HOME", ".cache")
}

// ConfigDir returns the directory for user configuration. Honors
// $XDG_CONFIG_HOME, falling back to ~/.config/superkube.
func ConfigDir() string {
	return resolveXDG("XDG_CONFIG_HOME", ".config")
}

// ConfigFile returns the conventional path to the YAML config file. Callers
// may pass --config to override; this is only the default.
func ConfigFile() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

func resolveXDG(envVar, defaultSubdir string) string {
	if dir := os.Getenv(envVar); dir != "" {
		return filepath.Join(dir, "superkube")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to /tmp rather than panic; superkube remains usable.
		return filepath.Join(os.TempDir(), "superkube-"+envVar)
	}
	return filepath.Join(home, defaultSubdir, "superkube")
}
