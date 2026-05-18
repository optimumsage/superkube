//go:build !darwin && !linux

package service

// On platforms we don't support, all entry points return
// ErrUnsupportedPlatform so the CLI layer can print a helpful message rather
// than a stack trace.

type unsupportedManager struct{}

func newManager() (Manager, error) { return nil, ErrUnsupportedPlatform }

func (unsupportedManager) Install(Spec, bool) error     { return ErrUnsupportedPlatform }
func (unsupportedManager) Uninstall(string) error       { return ErrUnsupportedPlatform }
func (unsupportedManager) Status(string) (State, error) { return State{}, ErrUnsupportedPlatform }
func (unsupportedManager) UnitPath(string) string       { return "" }
