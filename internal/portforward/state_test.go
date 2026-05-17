package portforward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTmpState(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
}

func TestAddLoadRemove(t *testing.T) {
	setupTmpState(t)

	e1 := Entry{
		ID:        "pf-aaa",
		Target:    "pod/foo",
		Ports:     []string{"8080:80"},
		PID:       os.Getpid(), // alive
		StartedAt: time.Now().Add(-1 * time.Minute),
		LogPath:   "/tmp/pf-aaa.log",
	}
	e2 := Entry{
		ID:        "pf-bbb",
		Target:    "svc/web",
		Ports:     []string{"9090:9090"},
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		LogPath:   "/tmp/pf-bbb.log",
	}
	if err := Add(e1); err != nil {
		t.Fatalf("Add e1: %v", err)
	}
	if err := Add(e2); err != nil {
		t.Fatalf("Add e2: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(loaded))
	}
	// Sort is by StartedAt asc: e1 should come first.
	if loaded[0].ID != "pf-aaa" {
		t.Errorf("sort order wrong: got %s first", loaded[0].ID)
	}

	removed, err := Remove("pf-aaa")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removed) != 1 || removed[0].ID != "pf-aaa" {
		t.Errorf("Remove returned %v", removed)
	}
	loaded, _ = Load()
	if len(loaded) != 1 || loaded[0].ID != "pf-bbb" {
		t.Errorf("after remove, expected only pf-bbb, got %v", loaded)
	}

	// Removing missing id should be a clean no-op.
	if removed, err := Remove("nope"); err != nil || len(removed) != 0 {
		t.Errorf("Remove(\"nope\") = %v, %v", removed, err)
	}
}

func TestRemoveAll(t *testing.T) {
	setupTmpState(t)
	for _, id := range []string{"pf-1", "pf-2", "pf-3"} {
		_ = Add(Entry{ID: id, PID: os.Getpid(), StartedAt: time.Now(), Target: "x", Ports: []string{"80:80"}})
	}
	removed, err := Remove("all")
	if err != nil {
		t.Fatalf("Remove(all): %v", err)
	}
	if len(removed) != 3 {
		t.Errorf("expected 3 removed, got %d", len(removed))
	}
	loaded, _ := Load()
	if len(loaded) != 0 {
		t.Errorf("expected empty state, got %d", len(loaded))
	}
}

func TestPrunesDeadPIDs(t *testing.T) {
	setupTmpState(t)
	// Write a state file by hand with a guaranteed-dead PID. Linux/macOS both
	// reserve PID 1 (init); a very high PID is far less likely to be live.
	dead := Entry{
		ID:        "pf-dead",
		Target:    "pod/x",
		Ports:     []string{"1:1"},
		PID:       99999999,
		StartedAt: time.Now(),
	}
	alive := Entry{
		ID:        "pf-alive",
		Target:    "pod/y",
		Ports:     []string{"2:2"},
		PID:       os.Getpid(),
		StartedAt: time.Now(),
	}
	data, _ := json.Marshal([]Entry{dead, alive})
	_ = os.MkdirAll(filepath.Dir(StateFile()), 0o700)
	if err := os.WriteFile(StateFile(), data, 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].ID != "pf-alive" {
		t.Errorf("expected dead PID pruned, got %v", got)
	}
}

func TestFindByID(t *testing.T) {
	setupTmpState(t)
	e := Entry{ID: "pf-find", PID: os.Getpid(), StartedAt: time.Now(), Target: "svc/x", Ports: []string{"1:1"}}
	_ = Add(e)
	got, ok, err := FindByID("pf-find")
	if err != nil || !ok {
		t.Fatalf("FindByID: ok=%v err=%v", ok, err)
	}
	if got.Target != "svc/x" {
		t.Errorf("wrong entry: %v", got)
	}
	_, ok, _ = FindByID("missing")
	if ok {
		t.Errorf("expected miss")
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if seen[id] {
			t.Errorf("collision at iteration %d: %s", i, id)
		}
		seen[id] = true
		// Tiny sleep so the time-based suffix advances; on very fast machines
		// 100 sequential calls within the same nanosecond could collide and
		// the test isn't about that.
		time.Sleep(50 * time.Microsecond)
	}
}
