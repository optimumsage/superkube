// Package kubectl shells out to the user's kubectl binary. Keeping this isolated
// makes it easy to test arg rewriting without spawning processes and to swap
// the locator (PATH lookup vs $KUBECTL env var) without touching call sites.
package kubectl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Runner is the configured kubectl shell-out client. Construct one per process
// via New() and reuse it.
type Runner struct {
	// Path is the resolved path to the kubectl binary.
	Path string
}

// RunOpts configures a single kubectl invocation. All fields are optional; zero
// values mean "inherit from the parent process".
type RunOpts struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// ExtraEnv is appended to os.Environ() for this invocation.
	ExtraEnv []string
}

var (
	defaultRunnerOnce sync.Once
	defaultRunner     *Runner
	defaultRunnerErr  error
)

// Default returns a process-wide Runner singleton, locating kubectl on first
// call. Safe for concurrent use. Use New() directly if you need a fresh probe
// (e.g. in tests).
func Default() (*Runner, error) {
	defaultRunnerOnce.Do(func() {
		defaultRunner, defaultRunnerErr = New()
	})
	return defaultRunner, defaultRunnerErr
}

// New locates kubectl. $KUBECTL overrides PATH lookup. The returned Runner has
// validated that the binary exists; it does not probe the version (see
// Version, which is lazy and cached).
func New() (*Runner, error) {
	path := os.Getenv("KUBECTL")
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("$KUBECTL points to %q which is not accessible: %w", path, err)
		}
		return &Runner{Path: path}, nil
	}
	p, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("kubectl not found on PATH (set $KUBECTL to override)")
	}
	return &Runner{Path: p}, nil
}

// Run invokes kubectl with the given args, streaming stdio. The child inherits
// our process group, so terminal signals (Ctrl-C) reach it naturally — no
// manual forwarding required. Returns *ExitCodeError when kubectl exits
// non-zero so the caller can propagate the exit code unchanged.
func (r *Runner) Run(ctx context.Context, args []string, opts RunOpts) error {
	cmd := exec.CommandContext(ctx, r.Path, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if len(opts.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), opts.ExtraEnv...)
	}
	// We deliberately do NOT set Setpgid: true. Inheriting the parent's
	// process group means SIGINT from the terminal is delivered to both us
	// and kubectl simultaneously, which is the correct UX for `logs -f`,
	// `port-forward`, and `exec`.
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &ExitCodeError{Code: ee.ExitCode()}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &ExitCodeError{Code: 130, Err: err} // 128 + SIGINT
		}
		return err
	}
	return nil
}

// Captured holds buffered stdout/stderr from a non-streaming kubectl call.
// Used by features that need to parse kubectl's output, e.g. dry-run-then-diff
// or the `--ai` log analyzer. Wired in a later task.
type Captured struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitCodeError carries a child-process exit code through Go's error path so
// the caller can pass it straight to os.Exit. Defined here (the lowest layer
// that produces exit codes) so both internal/cli and any future caller can
// check for it via errors.As without an import cycle.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("kubectl exited with code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }
