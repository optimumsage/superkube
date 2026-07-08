package cli

import (
	"strings"
	"testing"
)

// TestLogsAIRejectsNoContext guards the privacy-flag fix. `logs` uses
// DisableFlagParsing, so --no-context placed after the verb never binds to
// Flags.NoContext; runLogs must still detect it (via args) and refuse, rather
// than silently shipping the log tail to the AI provider.
func TestLogsAIRejectsNoContext(t *testing.T) {
	cmd := newLogsCmd()
	err := runLogs(cmd, []string{"--ai", "--no-context", "somepod"})
	if err == nil || !strings.Contains(err.Error(), "no-context") {
		t.Fatalf("expected --no-context rejection, got %v", err)
	}
}

// TestLogsAIRejectsFollow keeps the existing follow-mode guard covered.
func TestLogsAIRejectsFollow(t *testing.T) {
	cmd := newLogsCmd()
	err := runLogs(cmd, []string{"--ai", "-f", "somepod"})
	if err == nil || !strings.Contains(err.Error(), "follow") {
		t.Fatalf("expected -f rejection, got %v", err)
	}
}
