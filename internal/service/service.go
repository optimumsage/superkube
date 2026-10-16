package service

import "os"

// NewManager returns the Manager implementation appropriate for the host OS.
// The actual constructors live in launchd_darwin.go / systemd_linux.go /
// unsupported.go behind build tags so a single binary only carries the
// platform it ships on.
func NewManager() (Manager, error) {
	return newManager()
}

// writeUnitFile writes a service definition owner-only (0600). The unit/plist
// carries the web auth token in its arguments, so it must not be world-
// readable; the Chmod also tightens a pre-existing 0644 file, which WriteFile
// alone would leave as is.
func writeUnitFile(path string, body []byte) error {
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
