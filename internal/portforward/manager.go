package portforward

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// StartOpts describes a forward to launch.
type StartOpts struct {
	Target     string   // pod/foo, svc/web, deploy/api, ...
	Ports      []string // ["8080:80"]
	Namespace  string
	Context    string
	Kubeconfig string
	// KubectlPath overrides the resolved kubectl binary. Useful for tests.
	KubectlPath string
	// Address is the bind address for kubectl port-forward (--address).
	// Empty defaults to kubectl's default (127.0.0.1).
	Address string
}

// Start spawns a detached `kubectl port-forward` and records the resulting
// process in the state file. Returns the created entry on success.
//
// The child process is double-detached: setpgid + redirected stdio + Release()
// so it survives both the sk parent and the controlling terminal closing.
func Start(opts StartOpts) (Entry, error) {
	if opts.Target == "" {
		return Entry{}, errors.New("target required")
	}
	if len(opts.Ports) == 0 {
		return Entry{}, errors.New("at least one port mapping required (e.g. 8080:80)")
	}
	kubectlPath := opts.KubectlPath
	if kubectlPath == "" {
		p, err := exec.LookPath("kubectl")
		if err != nil {
			return Entry{}, fmt.Errorf("kubectl not found on PATH: %w", err)
		}
		kubectlPath = p
	}

	id := NewID()
	if err := os.MkdirAll(LogDir(), 0o700); err != nil {
		return Entry{}, err
	}
	logPath := filepath.Join(LogDir(), id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Entry{}, err
	}
	// We pass logFile to the child and close our handle after Start; the OS
	// keeps the fd alive in the child.
	defer logFile.Close()

	args := buildKubectlArgs(opts)
	cmd := exec.Command(kubectlPath, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // detach: signals to sk's pgroup don't kill the child
	}
	// Stdin closed so kubectl doesn't block waiting on TTY.
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return Entry{}, fmt.Errorf("start kubectl port-forward: %w", err)
	}
	// Release so we don't hold a zombie reference. Liveness will be polled
	// from the state file later.
	if err := cmd.Process.Release(); err != nil {
		// Non-fatal — child is already running.
		_ = err
	}

	entry := Entry{
		ID:         id,
		Target:     opts.Target,
		Ports:      opts.Ports,
		Namespace:  opts.Namespace,
		Context:    opts.Context,
		PID:        cmd.Process.Pid,
		StartedAt:  time.Now().UTC(),
		LogPath:    logPath,
		Kubeconfig: opts.Kubeconfig,
	}

	// Give kubectl a brief moment to fail fast (auth error, bad target). If
	// it dies in the first 250ms we surface the log to the caller so the
	// user gets a useful error instead of a phantom entry.
	time.Sleep(250 * time.Millisecond)
	if !isAlive(entry.PID) {
		tail, _ := os.ReadFile(logPath)
		return Entry{}, fmt.Errorf("port-forward exited immediately:\n%s", strings.TrimSpace(string(tail)))
	}

	if err := Add(entry); err != nil {
		// State file write failed — try to clean up the orphaned process
		// rather than leak it.
		_ = Kill(entry.PID)
		return Entry{}, err
	}
	return entry, nil
}

// Stop terminates the entries with the given id ("all" matches everything),
// removes them from the state file, and returns the entries it stopped.
func Stop(id string) ([]Entry, error) {
	removed, err := Remove(id)
	if err != nil {
		return removed, err
	}
	for _, e := range removed {
		_ = Kill(e.PID)
	}
	return removed, nil
}

// Kill sends SIGTERM, then SIGKILL after a brief grace window. Best-effort:
// missing PIDs and EPERM are silenced because the caller usually only cares
// that the process is gone.
func Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	if !isAlive(pid) {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return proc.Signal(syscall.SIGKILL)
}

// buildKubectlArgs assembles the argv we hand to `kubectl port-forward`. We
// add kubectl-global flags first so a user-supplied --context can't be
// silently overridden by the wrapper.
func buildKubectlArgs(opts StartOpts) []string {
	args := []string{"port-forward"}
	if opts.Kubeconfig != "" {
		args = append(args, "--kubeconfig", opts.Kubeconfig)
	}
	if opts.Context != "" {
		args = append(args, "--context", opts.Context)
	}
	if opts.Namespace != "" {
		args = append(args, "-n", opts.Namespace)
	}
	if opts.Address != "" {
		args = append(args, "--address", opts.Address)
	}
	args = append(args, opts.Target)
	args = append(args, opts.Ports...)
	return args
}
