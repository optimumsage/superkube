package portforward

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requirePS(t *testing.T) {
	t.Helper()
	if _, err := processCommand(os.Getpid()); err != nil {
		t.Skipf("ps cannot inspect processes here: %v", err)
	}
}

// Start must record the real PID and start time (Process.Release() sets
// Process.Pid to -1, so they have to be captured first).
func TestStartRecordsPIDAndProcStart(t *testing.T) {
	setupTmpState(t)
	requirePS(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := Start(StartOpts{Target: "pod/x", Ports: []string{"1:1"}, KubectlPath: fake})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(e.PID, syscall.SIGKILL) })
	if e.PID <= 0 {
		t.Fatalf("PID = %d", e.PID)
	}
	if e.ProcStart == "" {
		t.Fatal("ProcStart not recorded")
	}
}

// A ps that fails (non-zero exit, stderr only) for a live process must yield
// unknown, never stale: the entry may not be pruned or orphaned.
func TestIdentifyUnknownWhenPSFailsForLiveProcess(t *testing.T) {
	setupTmpState(t)
	cmd := exec.Command("sh", "-c", "sleep 30; true", "port-forward", "pod/x")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho 'ps: permission denied' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	e := Entry{ID: "pf-ps", Target: "pod/x", PID: cmd.Process.Pid}
	if got := identify(e); got != identUnknown {
		t.Fatalf("identify = %v, want unknown", got)
	}
}

// Stop with one owned and one unknown entry must return both the stopped one
// and an error.
func TestStopReturnsStoppedAlongsideUnknownError(t *testing.T) {
	setupTmpState(t)
	a := exec.Command("sh", "-c", "sleep 30; true", "port-forward", "pod/a")
	b := exec.Command("sh", "-c", "sleep 30; true", "port-forward", "pod/b")
	for _, c := range []*exec.Cmd{a, b} {
		if err := c.Start(); err != nil {
			t.Skip(err)
		}
		c := c
		t.Cleanup(func() { _ = c.Process.Kill() })
		go func() { _ = c.Wait() }()
	}
	old := processCommand
	t.Cleanup(func() { processCommand = old })
	processCommand = func(pid int) (string, error) {
		if pid == b.Process.Pid {
			return "", os.ErrPermission
		}
		return old(pid)
	}
	requirePS(t)
	for id, c := range map[string]*exec.Cmd{"pf-a": a, "pf-b": b} {
		tg := "pod/a"
		if id == "pf-b" {
			tg = "pod/b"
		}
		if err := Add(Entry{ID: id, Target: tg, Ports: []string{"1:1"}, PID: c.Process.Pid, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	stopped, err := Stop("all")
	if err == nil || !strings.Contains(err.Error(), "pf-b") {
		t.Fatalf("want identity error for pf-b, got %v", err)
	}
	if len(stopped) != 1 || stopped[0].ID != "pf-a" {
		t.Fatalf("stopped = %v", stopped)
	}
	if isAlive(b.Process.Pid) == false {
		t.Fatal("unknown process was signalled")
	}
}
