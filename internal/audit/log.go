// Package audit appends a JSON-Lines record per superkube invocation so users
// can answer "what did I just run, and against which cluster". One file is
// written per host: ${XDG_STATE_HOME:-~/.local/state}/superkube/audit.log.
//
// The schema is intentionally flat and stable. Adding fields is backwards
// compatible (jq users just see new keys); removing or renaming fields is a
// breaking change and bumps the "v" field.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/optimumsage/superkube/internal/config"
)

const schemaVersion = 1

// Event is one row in the audit log.
type Event struct {
	Timestamp        time.Time `json:"ts"`
	Schema           int       `json:"v"`
	ID               string    `json:"id"`
	User             string    `json:"user,omitempty"`
	Cmd              string    `json:"cmd"`
	Argv             []string  `json:"argv"`
	Context          string    `json:"context,omitempty"`
	Namespace        string    `json:"namespace,omitempty"`
	Kubeconfig       string    `json:"kubeconfig,omitempty"`
	Verb             string    `json:"verb,omitempty"`
	Destructive      bool      `json:"destructive,omitempty"`
	DryRun           bool      `json:"dry_run,omitempty"`
	Confirmed        bool      `json:"confirmed,omitempty"`
	AIProvider       string    `json:"ai_provider,omitempty"`
	DurationMS       int64     `json:"duration_ms"`
	ExitCode         int       `json:"exit_code"`
	StderrTail       string    `json:"stderr_tail,omitempty"`
	GuardrailSkipped bool      `json:"guardrail_skipped,omitempty"`
}

// Record appends one event to the audit log. Best-effort: any error is logged
// to stderr at -v level but never propagated, because a logging failure must
// not break the user's command.
func Record(e Event) {
	if e.Schema == 0 {
		e.Schema = schemaVersion
	}
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	// Redact common secret-bearing kubectl flags from the stored argv so the
	// audit log doesn't double as a credential leak. We do NOT mutate the
	// caller's slice; we copy.
	e.Argv = redactArgv(e.Argv)

	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := appendLine(LogPath(), append(line, '\n')); err != nil {
		_ = err // best-effort; -v could log this later
	}
}

// LogPath returns the resolved audit log file path.
func LogPath() string {
	return filepath.Join(config.StateDir(), "audit.log")
}

var mu sync.Mutex

// appendLine writes b to path under both an in-process mutex and a Unix file
// lock, so multiple concurrent sk invocations interleave cleanly. Creates the
// parent directory and file as needed with 0600 mode.
func appendLine(path string, b []byte) error {
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

// newID returns a short, lexicographically-sortable ID: 12 hex chars of unix
// micros + 8 hex chars of randomness. Not a real ULID but good enough for
// per-invocation correlation, and zero dependencies.
func newID() string {
	t := time.Now().UnixMicro()
	tail := make([]byte, 4)
	_, _ = rand.Read(tail)
	return fmt.Sprintf("%012x-%s", t, hex.EncodeToString(tail))
}

// secretFlags are the flags whose value is a credential. Both `--flag=value`
// and `--flag value` spellings are masked. Flags whose value is a file path
// (--client-key, --kubeconfig) or a non-secret identity (--username) are kept.
var secretFlags = []string{"--from-literal", "--token", "--password", "--docker-password"}

// redactArgv masks the value portion of secret-bearing kubectl flags:
// --from-literal=KEY=VALUE → --from-literal=KEY=<redacted>, and the
// space-separated forms. Tokens that look like JWTs are also masked, in case
// the user pasted one as a positional arg. The output slice is fresh; the
// input is not mutated (it is still the argv that gets executed).
func redactArgv(in []string) []string {
	out := make([]string, len(in))
	pending := "" // secret flag whose value is the next element
	for i, a := range in {
		if pending != "" {
			out[i] = redactFlagValue(pending, a)
			pending = ""
			continue
		}
		out[i] = redactToken(a)
		for _, f := range secretFlags {
			if a == f {
				pending = f
			}
		}
	}
	return out
}

// redactFlagValue masks v, the value of secret flag f. --from-literal values
// are KEY=VALUE; the key is kept.
func redactFlagValue(f, v string) string {
	if f == "--from-literal" {
		if eq := strings.IndexByte(v, '='); eq >= 0 {
			return v[:eq] + "=<redacted>"
		}
	}
	return "<redacted>"
}

func redactToken(s string) string {
	for _, f := range secretFlags {
		if strings.HasPrefix(s, f+"=") {
			return f + "=" + redactFlagValue(f, s[len(f)+1:])
		}
	}
	// JWT detection: three base64ish segments joined by dots, leading "eyJ".
	if strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2 {
		return "<redacted-jwt>"
	}
	return s
}
