// Package portforward manages background `kubectl port-forward` processes
// across superkube invocations. It keeps a small JSON state file under
// $XDG_STATE_HOME/superkube so `sk pf` can list, attach, and stop forwards
// you launched from a different shell.
//
// Design note: we deliberately do NOT run a daemon. Each `sk pf start` shells
// out to `kubectl port-forward` and detaches it. The PID lives in the state
// file; liveness is probed lazily with kill(pid, 0). If the process dies
// out-of-band the entry stays until the next `sk pf list`, which prunes
// dead PIDs. A live PID is only trusted if its command line still looks like
// one of our `kubectl port-forward` children (PIDs get reused, e.g. after a
// reboot), so we never signal an unrelated process.
package portforward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/optimumsage/superkube/internal/config"
)

// Entry is one tracked port-forward.
type Entry struct {
	ID        string    `json:"id"`
	Target    string    `json:"target"` // e.g. "pod/foo" or "svc/web"
	Namespace string    `json:"namespace,omitempty"`
	Context   string    `json:"context,omitempty"`
	Ports     []string  `json:"ports"` // ["8080:80", "9090:9090"]
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	LogPath   string    `json:"log_path"`
	// ProcStart is the OS-reported start time of the spawned process
	// (`ps -o lstart=`), used to detect PID reuse. Empty in state files
	// written by older versions; identification then falls back to the
	// command line + target.
	ProcStart  string `json:"proc_start,omitempty"`
	Kubeconfig string `json:"kubeconfig,omitempty"`
}

// StateFile returns the path to the JSON manifest tracking active forwards.
func StateFile() string {
	return filepath.Join(config.StateDir(), "portforward.json")
}

// LogDir returns the directory where per-forward log files live.
func LogDir() string {
	return filepath.Join(config.StateDir(), "portforward")
}

// stateDir is the directory holding the manifest and its lock file.
func stateDir() string { return filepath.Dir(StateFile()) }

// lockPath is the flock file guarding the state read-modify-write.
func lockPath() string { return StateFile() + ".lock" }

var mu sync.Mutex

// withLock runs fn holding both the in-process mutex and an exclusive flock on
// the lock file. The mutex covers goroutines in this process; the flock covers
// other `sk` processes (two concurrent `sk pf start` would otherwise each read
// the old manifest and the later write would drop the other's entry).
func withLock(fn func() error) error {
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", lockPath(), err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// Load reads the state file, dropping any entries whose PID is no longer
// alive. The returned slice is sorted by StartedAt asc so the CLI's table
// is stable across runs.
func Load() (entries []Entry, err error) {
	err = withLock(func() error {
		entries, err = loadLocked()
		return err
	})
	return entries, err
}

// readEntries parses the state file without any liveness filtering.
func readEntries() ([]Entry, error) {
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
	return entries, nil
}

// loadLocked is the inner unlocked variant for callers that already hold the
// state lock (withLock). It prunes only entries whose process is confirmed
// gone or confirmed to be someone else's; entries we merely failed to inspect
// are kept so a transient `ps` failure can't permanently forget a live forward.
func loadLocked() ([]Entry, error) {
	entries, err := readEntries()
	if err != nil {
		return nil, err
	}
	keep := entries[:0]
	for _, e := range entries {
		if identify(e) != identStale {
			keep = append(keep, e)
		}
	}
	if len(keep) != len(entries) {
		// Pruned some — persist the cleaner version so the next Load is fast.
		_ = writeLocked(keep)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].StartedAt.Before(keep[j].StartedAt) })
	return keep, nil
}

// Add appends e to the state file. Caller must have already spawned the
// process and recorded its PID.
func Add(e Entry) error {
	return withLock(func() error {
		current, err := loadLocked()
		if err != nil {
			return err
		}
		current = append(current, e)
		return writeLocked(current)
	})
}

// Remove drops the entry with the given id (or all entries if id == "all")
// without signalling anything, and returns the entries it removed. The id is
// matched against the raw state, before stale pruning, so a dead or
// PID-reused forward can still be removed by id. Missing id is a no-op.
func Remove(id string) (removed []Entry, err error) {
	err = withLock(func() error {
		current, err := readEntries()
		if err != nil {
			return err
		}
		kept := current[:0]
		for _, e := range current {
			if id == "all" || e.ID == id {
				removed = append(removed, e)
				continue
			}
			kept = append(kept, e)
		}
		if len(removed) == 0 {
			return nil
		}
		return writeLocked(kept)
	})
	return removed, err
}

// Stop terminates the entries with the given id ("all" matches everything).
// The id is matched first, then each entry's process identity is checked:
// owned processes are signalled and removed; confirmed-stale entries (process
// gone, or PID reused by something else) are removed without signalling;
// entries whose identity could not be verified are left in the state file,
// not signalled, and reported in the returned error. The returned slice holds
// the entries that were removed.
func Stop(id string) (stopped []Entry, err error) {
	var unknown []string
	var toKill []Entry
	// Classify and persist under the lock, but signal after releasing it:
	// Kill can wait up to 2s per PID and must not stall other sk processes.
	err = withLock(func() error {
		current, err := readEntries()
		if err != nil {
			return err
		}
		kept := current[:0]
		changed := false
		for _, e := range current {
			if id != "all" && e.ID != id {
				kept = append(kept, e)
				continue
			}
			switch identify(e) {
			case identOwned:
				toKill = append(toKill, e)
				stopped = append(stopped, e)
				changed = true
			case identStale:
				stopped = append(stopped, e)
				changed = true
			default:
				unknown = append(unknown, fmt.Sprintf("%s (pid %d)", e.ID, e.PID))
				kept = append(kept, e)
			}
		}
		if !changed {
			return nil
		}
		return writeLocked(kept)
	})
	for _, e := range toKill {
		// Re-verify right before signalling; the PID could have been reused
		// while we were unlocked.
		if identify(e) == identOwned {
			_ = Kill(e.PID)
		}
	}
	if err == nil && len(unknown) > 0 {
		err = fmt.Errorf("could not verify process identity for %s; not signalled and left in the state file (is `ps` available?)", strings.Join(unknown, ", "))
	}
	return stopped, err
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

// writeLocked persists entries to the state file. Caller must hold the state lock.
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

// errProcessGone means ps ran fine and reported no such process.
var errProcessGone = errors.New("process not found")

func psField(pid int, field string) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", field+"=").Output()
	if err != nil {
		// A failing ps proves nothing by itself (permissions, sandboxing,
		// missing binary). Only call the process gone when it is
		// independently confirmed absent; otherwise the caller gets a plain
		// error and treats the identity as unknown.
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(strings.TrimSpace(string(out))) == 0 && !isAlive(pid) {
			return "", errProcessGone
		}
		return "", err
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		if isAlive(pid) {
			return "", errors.New("ps returned no output for a live process")
		}
		return "", errProcessGone
	}
	return v, nil
}

// processCommand returns the command line of pid. Variables so tests can
// substitute them. `ps -o command=` / `lstart=` exist on macOS and Linux.
var processCommand = func(pid int) (string, error) { return psField(pid, "command") }

// processStartTime returns the OS-reported start time of pid (`ps -o lstart=`),
// which together with the PID identifies a process across PID reuse.
var processStartTime = func(pid int) (string, error) { return psField(pid, "lstart") }

type identity int

const (
	identUnknown identity = iota // could not be inspected: keep, don't signal
	identOwned                   // our kubectl port-forward
	identStale                   // gone, or PID reused by something else
)

// identify decides whether e.PID is still the `kubectl port-forward` we
// spawned. Owned requires the command line to contain both "port-forward" and
// the recorded target, and (when recorded) the start time to match. PIDs get
// reused, so a bare cmdline substring check would let `vim port-forward.md`
// or another user's forward be killed. Inspection failures are Unknown, never
// Stale: dropping a live forward is bad, signalling a stranger is worse.
func identify(e Entry) identity {
	if !isAlive(e.PID) {
		return identStale
	}
	cmd, err := processCommand(e.PID)
	if err != nil {
		if errors.Is(err, errProcessGone) {
			return identStale
		}
		return identUnknown
	}
	if !strings.Contains(cmd, "port-forward") || (e.Target != "" && !strings.Contains(cmd, e.Target)) {
		return identStale
	}
	if e.ProcStart != "" {
		st, err := processStartTime(e.PID)
		if err != nil {
			if errors.Is(err, errProcessGone) {
				return identStale
			}
			return identUnknown
		}
		if st != e.ProcStart {
			return identStale
		}
	}
	return identOwned
}
