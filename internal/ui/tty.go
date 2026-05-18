// Package ui owns superkube's terminal presentation. Everything that touches
// colors, tables, spinners, or interactive prompts lives here so the rest of
// the codebase doesn't import charm libraries directly. This also makes it
// trivial to add a `--plain` fallback in one place.
package ui

import (
	"os"
	"sync"
	"sync/atomic"

	"github.com/mattn/go-isatty"
)

var (
	// Plain, when true, disables all ANSI styling and interactive widgets.
	// Set via root flag --plain or the NO_COLOR env var. Read-mostly; written
	// once at startup.
	Plain bool

	envOnce sync.Once
)

// Init configures the package from the parsed --plain flag plus environment.
// Idempotent: subsequent calls update the override.
func Init(plainFlag bool) {
	envOnce.Do(func() {
		if os.Getenv("NO_COLOR") != "" {
			Plain = true
		}
	})
	if plainFlag {
		Plain = true
	}
}

// IsStdoutTTY reports whether stdout is a terminal. Cached; we don't poll fds
// repeatedly. Tests can short-circuit this via SetStdoutTTYForTest.
func IsStdoutTTY() bool {
	stdoutOnce.Do(func() { stdoutTTY = isatty.IsTerminal(os.Stdout.Fd()) })
	if p := stdoutTTYOverride.Load(); p != nil {
		return *p
	}
	return stdoutTTY
}

// SetStdoutTTYForTest forces the TTY-detection cache to a known value and
// returns a restore func. Tests use this to exercise the styled path even
// when running under `go test` where stdout isn't a real terminal. Backed by
// atomic.Pointer so parallel tests don't race the override.
func SetStdoutTTYForTest(v bool) func() {
	prev := stdoutTTYOverride.Load()
	stdoutTTYOverride.Store(&v)
	return func() { stdoutTTYOverride.Store(prev) }
}

// IsStdinTTY reports whether stdin is a terminal. Used by the guardrail layer
// to refuse destructive commands in non-interactive contexts unless --yes.
func IsStdinTTY() bool {
	stdinOnce.Do(func() { stdinTTY = isatty.IsTerminal(os.Stdin.Fd()) })
	return stdinTTY
}

// Interactive reports whether we should render TUI widgets (spinners,
// confirms, fuzzy pickers). Requires both stdin and stdout to be terminals
// and --plain to be off.
func Interactive() bool {
	return !Plain && IsStdoutTTY() && IsStdinTTY()
}

// Styled reports whether we should emit ANSI colors. Looser than Interactive:
// piping to less is fine for colors even when widgets aren't.
func Styled() bool {
	return !Plain && IsStdoutTTY()
}

var (
	stdoutOnce        sync.Once
	stdoutTTY         bool
	stdoutTTYOverride atomic.Pointer[bool] // test-only override; nil = use real detection
	stdinOnce         sync.Once
	stdinTTY          bool
)
