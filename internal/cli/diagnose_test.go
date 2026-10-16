package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/kubectl"
)

// fakeKubectlRunner installs a fake kubectl that appends its full argv, one
// line per call, to $FAKE_KUBECTL_LOG and prints a canned line, so a test can
// count exactly which kubectl calls a code path made.
func fakeKubectlRunner(t *testing.T) (*kubectl.Runner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl requires a POSIX shell")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "fake-kubectl")
	body := `#!/bin/sh
echo "$*" >> "$FAKE_KUBECTL_LOG"
echo fake-output
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("FAKE_KUBECTL_LOG", logPath)
	return &kubectl.Runner{Path: bin}, logPath
}

func readCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// verbOf extracts the kubectl subcommand from a logged argv line, skipping the
// global flags (--kubeconfig/--context/-n/--namespace and their values) that
// kubectlGlobal prepends.
func verbOf(line string) string {
	f := strings.Fields(line)
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "--kubeconfig", "--context", "-n", "--namespace":
			i++
		default:
			return f[i]
		}
	}
	return ""
}

// TestGatherNoContextSendsNothing verifies the privacy fix: with --no-context
// set, gatherDiagnosticNS must NOT shell out to kubectl for describe/events/logs
// (previously it sent them regardless, contradicting the flag).
func TestGatherNoContextSendsNothing(t *testing.T) {
	runner, logPath := fakeKubectlRunner(t)

	defer func(v bool) { Flags.NoContext = v }(Flags.NoContext)
	Flags.NoContext = true

	inputs := gatherDiagnosticNS(context.Background(), runner, "pod/x", "pod", "x", "", true)

	if calls := readCalls(t, logPath); len(calls) != 0 {
		t.Fatalf("--no-context made kubectl calls %v, want none", calls)
	}
	if inputs.Describe != "" || inputs.Events != "" || inputs.Logs != "" {
		t.Fatalf("--no-context leaked cluster data: %+v", inputs)
	}
	if inputs.Resource != "pod/x" {
		t.Fatalf("resource reference should still be set, got %q", inputs.Resource)
	}
}

// TestGatherWithContextRunsThreeKubectlCalls confirms the normal path still
// gathers describe + events + logs (exactly three calls) for a non-pod target
// (so owner/sibling enrichment, which needs a live cluster, is not exercised).
func TestGatherWithContextRunsThreeKubectlCalls(t *testing.T) {
	runner, logPath := fakeKubectlRunner(t)

	defer func(nc bool, kc string) { Flags.NoContext = nc; Flags.Kubeconfig = kc }(Flags.NoContext, Flags.Kubeconfig)
	Flags.NoContext = false
	Flags.Kubeconfig = filepath.Join(t.TempDir(), "nonexistent-kubeconfig")

	inputs := gatherDiagnosticNS(context.Background(), runner, "deployment/web", "deployment", "web", "ns", false)

	calls := readCalls(t, logPath)
	if len(calls) != 3 {
		t.Fatalf("expected 3 kubectl calls (describe/get/logs), got %v", calls)
	}
	for i, want := range []string{"describe", "get", "logs"} {
		if got := verbOf(calls[i]); got != want {
			t.Errorf("call %d verb = %q, want %q (%q)", i, got, want, calls[i])
		}
	}
	if inputs.Describe == "" || inputs.Events == "" || inputs.Logs == "" {
		t.Fatalf("expected describe/events/logs populated, got %+v", inputs)
	}
}
