// Package helm shells out to the user's helm binary. It mirrors the layout of
// internal/kubectl so the web layer can locate, run, and propagate exit codes
// from helm exactly the same way it does for kubectl. helm is optional: when
// the binary is not on PATH the caller receives ErrNotInstalled and renders a
// "helm is not installed" UX instead of failing.
package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrNotInstalled is returned by New / Default when the helm binary cannot be
// located. Callers should treat this as a soft failure and surface a helpful
// message — not crash the process.
var ErrNotInstalled = errors.New("helm not installed (set $HELM_BIN to override PATH lookup)")

// Runner is the configured helm shell-out client. Construct one via Default()
// and reuse.
type Runner struct {
	// Path is the resolved path to the helm binary.
	Path string
}

// RunOpts configures a single helm invocation. Zero values inherit from the
// parent process.
type RunOpts struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	ExtraEnv []string
}

var (
	defaultOnce sync.Once
	defaultR    *Runner
	defaultErr  error
)

// Default returns the process-wide Runner singleton, locating helm on first
// call. Safe for concurrent use. Returns ErrNotInstalled when helm is missing.
func Default() (*Runner, error) {
	defaultOnce.Do(func() {
		defaultR, defaultErr = New()
	})
	return defaultR, defaultErr
}

// New locates helm. $HELM_BIN overrides PATH lookup. Returns ErrNotInstalled
// (wrapped with the underlying cause) when nothing is found.
func New() (*Runner, error) {
	if explicit := os.Getenv("HELM_BIN"); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return nil, fmt.Errorf("%w: $HELM_BIN points to %q (%v)", ErrNotInstalled, explicit, err)
		}
		return &Runner{Path: explicit}, nil
	}
	p, err := exec.LookPath("helm")
	if err != nil {
		return nil, ErrNotInstalled
	}
	return &Runner{Path: p}, nil
}

// Run invokes helm with the given args, streaming stdio. Returns ExitCodeError
// on non-zero exit so the caller can propagate the code.
func (r *Runner) Run(ctx context.Context, args []string, opts RunOpts) error {
	if r == nil {
		return ErrNotInstalled
	}
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
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &ExitCodeError{Code: ee.ExitCode()}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &ExitCodeError{Code: 130, Err: err}
		}
		return err
	}
	return nil
}

// Capture runs helm and returns combined stdout+stderr plus an exit code.
// Convenience for handlers that need the output for JSON responses.
func (r *Runner) Capture(ctx context.Context, args []string) (string, int, error) {
	if r == nil {
		return "", 0, ErrNotInstalled
	}
	var buf bytes.Buffer
	err := r.Run(ctx, args, RunOpts{Stdout: &buf, Stderr: &buf})
	if err != nil {
		var ee *ExitCodeError
		if errors.As(err, &ee) {
			return buf.String(), ee.Code, nil
		}
		return buf.String(), -1, err
	}
	return buf.String(), 0, nil
}

var (
	versionOnce sync.Once
	versionVal  string
	versionErr  error
)

// Version returns the short helm version string ("v3.14.1"). Cached for the
// process lifetime. Returns ErrNotInstalled when helm is missing.
func (r *Runner) Version(ctx context.Context) (string, error) {
	if r == nil {
		return "", ErrNotInstalled
	}
	versionOnce.Do(func() {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, r.Path, "version", "--short")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			versionErr = fmt.Errorf("probe helm version: %w", err)
			return
		}
		// `helm version --short` prints e.g. "v3.14.1+gabcdef" — trim the build
		// suffix and any whitespace.
		v := strings.TrimSpace(out.String())
		if i := strings.IndexByte(v, '+'); i > 0 {
			v = v[:i]
		}
		versionVal = v
	})
	return versionVal, versionErr
}

// ExitCodeError carries a helm exit code through Go's error path.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("helm exited with code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }
