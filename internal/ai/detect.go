package ai

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoProvider is returned when no AI binary is available and the user has
// not pinned one explicitly.
var ErrNoProvider = errors.New("no AI provider found on PATH (install `claude` or `gemini`, or pass --ai)")

// Detect returns the Provider the user should use. Precedence:
//
//  1. explicit (--ai flag passed by the caller — e.g. "claude", "gemini")
//  2. $SUPERKUBE_AI env var
//  3. config file (wired in later; for now this falls through)
//  4. PATH lookup: claude, then gemini
//
// If the explicit choice can't be resolved (binary missing), the caller gets
// an error rather than silent fallback — silent fallback would surprise users
// who think their --ai pin is being honored.
func Detect(explicit string) (Provider, error) {
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv("SUPERKUBE_AI"))
	}

	switch strings.ToLower(explicit) {
	case "claude":
		if _, err := exec.LookPath("claude"); err != nil {
			return nil, errors.New("claude not found on PATH (you pinned --ai=claude)")
		}
		return &claudeProvider{}, nil
	case "gemini":
		if _, err := exec.LookPath("gemini"); err != nil {
			return nil, errors.New("gemini not found on PATH (you pinned --ai=gemini)")
		}
		return &geminiProvider{}, nil
	case "":
		// Auto-detect.
	default:
		return nil, errors.New("unknown AI provider: " + explicit + " (supported: claude, gemini)")
	}

	if _, err := exec.LookPath("claude"); err == nil {
		return &claudeProvider{}, nil
	}
	if _, err := exec.LookPath("gemini"); err == nil {
		return &geminiProvider{}, nil
	}
	return nil, ErrNoProvider
}

// DetectName returns the name of the provider that would be chosen, without
// actually constructing it. Used by `sk version`.
func DetectName(explicit string) string {
	p, err := Detect(explicit)
	if err != nil {
		return "(none)"
	}
	_ = context.TODO
	return p.Name()
}
