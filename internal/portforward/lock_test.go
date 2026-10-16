package portforward

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// fakeForwardCommand makes every PID look like one of our kubectl
// port-forward processes, so tests that use os.Getpid() as a "live forward"
// keep working with the identity check.
func fakeForwardCommand(t *testing.T) {
	t.Helper()
	old := processCommand
	processCommand = func(int) (string, error) {
		return "kubectl port-forward pod/foo svc/web pod/x pod/y svc/x 8080:80", nil
	}
	t.Cleanup(func() { processCommand = old })
}

// TestAddBlocksOnCrossProcessLock proves the state read-modify-write is
// guarded by an flock, not just the in-process mutex: a lock held on a
// separate file descriptor (as another `sk pf start` process would) stalls
// Add until released.
func TestAddBlocksOnCrossProcessLock(t *testing.T) {
	setupTmpState(t)
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- Add(Entry{ID: "pf-lock", Target: "pod/x", Ports: []string{"1:1"}, PID: os.Getpid(), StartedAt: time.Now()})
	}()
	select {
	case <-done:
		t.Fatal("Add completed while another process held the state lock")
	case <-time.After(150 * time.Millisecond):
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Add did not proceed after lock release")
	}
}

// TestStopDoesNotKillReusedPID: a state entry whose PID now belongs to an
// unrelated process (e.g. after a reboot) must be dropped, not signalled.
func TestStopDoesNotKillReusedPID(t *testing.T) {
	setupTmpState(t) // real processCommand: sleep is not a port-forward
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	if err := Add(Entry{ID: "pf-stale", Target: "pod/x", Ports: []string{"1:1"}, PID: cmd.Process.Pid, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop("pf-stale")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(stopped) != 1 || stopped[0].ID != "pf-stale" {
		t.Fatalf("Stop must return the stale entry, got %v", stopped)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process was killed: %v", err)
	}
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("stale entry not dropped: %v", entries)
	}
}
