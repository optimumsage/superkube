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

// argLoggingRunner installs a fake kubectl that records its full argv, one
// invocation per line, so tests can assert on the global flags we inject.
func argLoggingRunner(t *testing.T) (*kubectl.Runner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl requires a POSIX shell")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "fake-kubectl")
	body := "#!/bin/sh\necho \"$*\" >> \"$FAKE_KUBECTL_ARGV\"\necho ok\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KUBECTL_ARGV", logPath)
	return &kubectl.Runner{Path: bin}, logPath
}

// TestTUIProducersHonorContextAndKubeconfig is the regression test for TUI
// actions silently running against the kubeconfig's current-context while the
// pod list used --context.
func TestTUIProducersHonorContextAndKubeconfig(t *testing.T) {
	runner, logPath := argLoggingRunner(t)
	oldCtx, oldKC, oldNC := Flags.Context, Flags.Kubeconfig, Flags.NoContext
	defer func() { Flags.Context, Flags.Kubeconfig, Flags.NoContext = oldCtx, oldKC, oldNC }()
	Flags.Context, Flags.Kubeconfig, Flags.NoContext = "prod", "/nonexistent/kc", false

	var sink strings.Builder
	ctx := context.Background()
	producers := map[string]func() error{
		"yaml":     func() error { return makeYAMLProducer(runner, "pod")(ctx, "ns1", "p", &sink) },
		"secret":   func() error { return makeSecretYAMLProducer(runner)(ctx, "ns1", "s", &sink) },
		"describe": func() error { return makeDescribeProducer(runner)(ctx, "ns1", "p", &sink) },
		"events":   func() error { return makeEventsProducer(runner)(ctx, "ns1", "p", &sink) },
	}
	for name, fn := range producers {
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Diagnose / why gathering shells out too (describe, events, logs).
	gatherDiagnosticNS(ctx, runner, "pod/p", "pod", "p", "ns1", false)

	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 7 {
		t.Fatalf("expected >=7 kubectl calls, got %d: %v", len(lines), lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "--context prod") || !strings.Contains(l, "--kubeconfig /nonexistent/kc") {
			t.Errorf("kubectl call missing global flags: %q", l)
		}
	}
}
