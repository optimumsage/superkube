package service

// NewManager returns the Manager implementation appropriate for the host OS.
// The actual constructors live in launchd_darwin.go / systemd_linux.go /
// unsupported.go behind build tags so a single binary only carries the
// platform it ships on.
func NewManager() (Manager, error) {
	return newManager()
}
