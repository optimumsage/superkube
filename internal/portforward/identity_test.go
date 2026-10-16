package portforward

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startLookalike launches a process whose command line contains the given
// extra words, e.g. `sh -c "sleep 30" port-forward-notes.md`.
func startLookalike(t *testing.T, words ...string) *exec.Cmd {
	t.Helper()
	args := append([]string{"-c", "sleep 30; true"}, words...)
	cmd := exec.Command("sh", args...)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func alive(cmd *exec.Cmd) bool { return cmd.Process.Signal(syscall.Signal(0)) == nil }

// A reused PID whose command line merely mentions "port-forward" (an editor
// open on port-forward-notes.md) must not be treated as ours: the recorded
// target is missing from its command line.
func TestStopSkipsLookalikeProcess(t *testing.T) {
	setupTmpState(t)
	requirePS(t)
	cmd := startLookalike(t, "port-forward-notes.md")
	e := Entry{ID: "pf-look", Target: "svc/web", Ports: []string{"1:1"}, PID: cmd.Process.Pid, StartedAt: time.Now()}
	if err := Add(e); err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop("pf-look")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(stopped) != 1 || stopped[0].ID != "pf-look" {
		t.Fatalf("Stop must return the stale entry, got %v", stopped)
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(cmd) {
		t.Fatal("lookalike process was killed")
	}
}

// Start time recorded at spawn must match: same command line, different
// start time means the PID was reused.
func TestStopSkipsStartTimeMismatch(t *testing.T) {
	setupTmpState(t)
	requirePS(t)
	cmd := startLookalike(t, "port-forward", "pod/x")
	e := Entry{ID: "pf-st", Target: "pod/x", Ports: []string{"1:1"}, PID: cmd.Process.Pid,
		StartedAt: time.Now(), ProcStart: "Mon Jan  1 00:00:00 2001"}
	if err := Add(e); err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop("pf-st")
	if err != nil || len(stopped) != 1 {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(cmd) {
		t.Fatal("process with mismatching start time was killed")
	}
}

// The happy path with real ps: command line has port-forward + target, and the
// start time recorded matches, so Stop terminates it.
func TestStopKillsOwnedProcess(t *testing.T) {
	setupTmpState(t)
	requirePS(t)
	cmd := startLookalike(t, "port-forward", "pod/x")
	go func() { _ = cmd.Wait() }() // reap so liveness probe sees it die
	st, err := processStartTime(cmd.Process.Pid)
	if err != nil || st == "" {
		t.Skipf("no start time available: %v", err)
	}
	e := Entry{ID: "pf-own", Target: "pod/x", Ports: []string{"1:1"}, PID: cmd.Process.Pid, StartedAt: time.Now(), ProcStart: st}
	if err := Add(e); err != nil {
		t.Fatal(err)
	}
	if _, err := Stop("pf-own"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && isAlive(cmd.Process.Pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if isAlive(cmd.Process.Pid) {
		t.Fatal("owned process was not terminated")
	}
}

// Old state files have no ProcStart: fall back to cmdline + target.
func TestOwnsProcessLegacyEntryFallsBack(t *testing.T) {
	setupTmpState(t)
	requirePS(t)
	cmd := startLookalike(t, "port-forward", "pod/x")
	e := Entry{ID: "pf-old", Target: "pod/x", PID: cmd.Process.Pid}
	if got := identify(e); got != identOwned {
		t.Fatalf("legacy entry identity = %v, want owned", got)
	}
}

// If the process can't be inspected, the entry must neither be pruned nor
// signalled, and Stop must say so.
func TestUnknownIdentityKeptAndNotSignalled(t *testing.T) {
	setupTmpState(t)
	old := processCommand
	processCommand = func(int) (string, error) { return "", errors.New("ps: not found") }
	t.Cleanup(func() { processCommand = old })

	cmd := startLookalike(t, "port-forward", "pod/x")
	e := Entry{ID: "pf-unk", Target: "pod/x", Ports: []string{"1:1"}, PID: cmd.Process.Pid, StartedAt: time.Now()}
	if err := Add(e); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("Load = %v, %v; unknown entries must be kept", loaded, err)
	}
	_, err = Stop("pf-unk")
	if err == nil || !strings.Contains(err.Error(), "pf-unk") {
		t.Fatalf("Stop should surface an identity error, got %v", err)
	}
	if !alive(cmd) {
		t.Fatal("unverifiable process was signalled")
	}
	loaded, _ = Load()
	if len(loaded) != 1 {
		t.Fatalf("entry should remain for retry, got %v", loaded)
	}
}

var _ = os.Getpid
