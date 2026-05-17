// Package portforward manages background `kubectl port-forward` processes
// across superkube invocations. It keeps a small JSON state file under
// $XDG_STATE_HOME/superkube so `sk pf` can list, attach, and stop forwards
// you launched from a different shell.
//
// Design note: we deliberately do NOT run a daemon. Each `sk pf start` shells
// out to `kubectl port-forward` and detaches it. The PID lives in the state
// file; liveness is probed lazily with kill(pid, 0). If the process dies
// out-of-band the entry stays until the next `sk pf list`, which prunes
// dead PIDs.
package portforward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/optimumsage/superkube/internal/config"
)

// Entry is one tracked port-forward.
type Entry struct {
	ID         string    `json:"id"`
	Target     string    `json:"target"` // e.g. "pod/foo" or "svc/web"
	Namespace  string    `json:"namespace,omitempty"`
	Context    string    `json:"context,omitempty"`
	Ports      []string  `json:"ports"` // ["8080:80", "9090:9090"]
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	LogPath    string    `json:"log_path"`
	Kubeconfig string    `json:"kubeconfig,omitempty"`
}

// StateFile returns the path to the JSON manifest tracking active forwards.
func StateFile() string {
	return filepath.Join(config.StateDir(), "portforward.json")
}

// LogDir returns the directory where per-forward log files live.
func LogDir() string {
	return filepath.Join(config.StateDir(), "portforward")
}

var mu sync.Mutex

// Load reads the state file, dropping any entries whose PID is no longer
// alive. The returned slice is sorted by StartedAt asc so the CLI's table
// is stable across runs.
func Load() ([]Entry, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked()
}

// loadLocked is the inner unlocked variant for callers that already hold mu.
func loadLocked() ([]Entry, error) {
	data, err := os.ReadFile(StateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	if len(data) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parse state file: %w", err)
		}
	}
	alive := entries[:0]
	for _, e := range entries {
		if isAlive(e.PID) {
			alive = append(alive, e)
		}
	}
	if len(alive) != len(entries) {
		// Pruned some — persist the cleaner version so the next Load is fast.
		_ = writeLocked(alive)
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i].StartedAt.Before(alive[j].StartedAt) })
	return alive, nil
}

// Add appends e to the state file. Caller must have already spawned the
// process and recorded its PID.
func Add(e Entry) error {
	mu.Lock()
	defer mu.Unlock()
	current, err := loadLocked()
	if err != nil {
		return err
	}
	current = append(current, e)
	return writeLocked(current)
}

// Remove drops the entry with the given id (or all entries if id == "all").
// Returns the entries it removed so the caller can act on them (send SIGTERM,
// delete log files, etc.). Missing id is not an error — Remove("nope") is a
// no-op.
func Remove(id string) ([]Entry, error) {
	mu.Lock()
	defer mu.Unlock()
	current, err := loadLocked()
	if err != nil {
		return nil, err
	}
	var removed []Entry
	kept := current[:0]
	for _, e := range current {
		if id == "all" || e.ID == id {
			removed = append(removed, e)
			continue
		}
		kept = append(kept, e)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := writeLocked(kept); err != nil {
		return removed, err
	}
	return removed, nil
}

// FindByID returns the matching entry from a freshly loaded state, or
// (Entry{}, false) when no entry has that id.
func FindByID(id string) (Entry, bool, error) {
	entries, err := Load()
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// writeLocked persists entries to the state file. Caller must hold mu.
func writeLocked(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(StateFile()), 0o700); err != nil {
		return err
	}
	if entries == nil {
		entries = []Entry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := StateFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StateFile())
}

// isAlive checks whether pid refers to a live process this user can signal.
// `kill -0` is the standard Unix idiom and avoids actually delivering a
// signal — perfect for liveness probes.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// ESRCH means "no such process"; EPERM means it exists but we can't
	// signal it (probably ran in another user). Treat the latter as alive
	// because the daemon-ish process tree might be intentionally restricted.
	return errors.Is(err, syscall.EPERM)
}

// NewID returns a short id derived from PID + start time. Collision-free at
// human time scales without pulling in a UUID library.
func NewID() string {
	return fmt.Sprintf("pf-%x", time.Now().UnixNano()&0xFFFFFF)
}
