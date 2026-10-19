package portforward

import (
	"strings"
	"testing"
	"time"
)

// stubIdentify replaces identifyProc with a function returning each outcome in
// turn (the last repeats), restoring the original on cleanup.
func stubIdentify(t *testing.T, outcomes ...identity) {
	t.Helper()
	orig := identifyProc
	n := 0
	identifyProc = func(Entry) identity {
		i := n
		if i >= len(outcomes) {
			i = len(outcomes) - 1
		}
		n++
		return outcomes[i]
	}
	t.Cleanup(func() { identifyProc = orig })
}

func addEntry(t *testing.T, id string) {
	t.Helper()
	// PID 0 is never alive/killable, so Kill is a harmless no-op.
	if err := Add(Entry{ID: id, Target: "svc/web", Ports: []string{"1:1"}, PID: 0, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func entryKept(t *testing.T, id string) bool {
	t.Helper()
	cur, err := readEntriesForTest()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range cur {
		if e.ID == id {
			return true
		}
	}
	return false
}

func readEntriesForTest() (out []Entry, err error) {
	err = withLock(func() error {
		var e error
		out, e = readEntries()
		return e
	})
	return out, err
}

// Owned at classification but unknown at the re-check: the process was never
// signalled, so the entry must stay and the error must name it.
func TestStopKeepsEntryWhenRecheckUnknown(t *testing.T) {
	setupTmpState(t)
	addEntry(t, "pf-rc")
	stubIdentify(t, identOwned, identUnknown)
	stopped, err := Stop("pf-rc")
	if err == nil || !strings.Contains(err.Error(), "pf-rc") {
		t.Fatalf("err = %v, want one naming pf-rc", err)
	}
	if len(stopped) != 0 {
		t.Fatalf("stopped = %v, want none", stopped)
	}
	if !entryKept(t, "pf-rc") {
		t.Fatal("entry was dropped though the process was not terminated")
	}
}

// Owned then stale at the re-check: the process is confirmed gone, drop it.
func TestStopDropsEntryWhenRecheckStale(t *testing.T) {
	setupTmpState(t)
	addEntry(t, "pf-rs")
	stubIdentify(t, identOwned, identStale)
	stopped, err := Stop("pf-rs")
	if err != nil || len(stopped) != 1 {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
	if entryKept(t, "pf-rs") {
		t.Fatal("entry should have been removed")
	}
}

// Owned both times: signalled (PID 0 -> no-op, treated as gone) and removed.
func TestStopDropsEntryWhenTerminated(t *testing.T) {
	setupTmpState(t)
	addEntry(t, "pf-ro")
	stubIdentify(t, identOwned, identOwned)
	stopped, err := Stop("pf-ro")
	if err != nil || len(stopped) != 1 || entryKept(t, "pf-ro") {
		t.Fatalf("stopped=%v err=%v kept=%v", stopped, err, entryKept(t, "pf-ro"))
	}
}
