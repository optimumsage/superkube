package portforward

import (
	"os/exec"
	"testing"
	"time"
)

// A forward that ignores SIGTERM must still be reported dead once Kill
// returns: SIGKILL is asynchronous, so Kill has to wait for the exit rather
// than return right after signalling.
func TestKillWaitsForSIGKILLToLand(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real process and waits out the SIGTERM grace period")
	}
	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	// Reap in the background so the killed child doesn't linger as a zombie,
	// which signal 0 would still report as alive.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	time.Sleep(100 * time.Millisecond) // let the trap install

	if err := Kill(cmd.Process.Pid); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process survived Kill")
	}
	if isAlive(cmd.Process.Pid) {
		t.Fatal("isAlive reports a SIGKILLed process as running")
	}
}
