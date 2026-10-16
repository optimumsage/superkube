package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Review round 1 regressions: argv-scanning heuristics must fail closed.

func (h *harness) setCurrent(ctx string) {
	h.t.Helper()
	p := filepath.Join(h.dir, "kubeconfig")
	data, err := os.ReadFile(p)
	must(h.t, err)
	s := strings.Replace(string(data), "current-context: dev", "current-context: "+ctx, 1)
	must(h.t, os.WriteFile(p, []byte(s), 0o600))
}

func (h *harness) refused(args ...string) {
	h.t.Helper()
	code, stderr := h.run("", args...)
	if code == 0 || h.callLog() != "" {
		h.t.Errorf("%v: code=%d calls=%q stderr=%q; want refusal before kubectl", args, code, h.callLog(), stderr)
	}
}

func TestAmbiguousContextFailsClosed(t *testing.T) {
	// current-context is prod; the --context=dev token is really the VALUE of
	// --field-manager, so kubectl writes to prod.
	h := newHarness(t, forbidProd)
	h.setCurrent("prod")
	h.refused("apply", "--field-manager", "--context=dev", "-f", "x.yaml", "--dry-run=none", "--yes")

	// Two different overrides: blocked if ANY candidate forbids.
	h = newHarness(t, forbidProd)
	h.refused("--context", "dev", "delete", "pod", "x", "--yes", "--context", "prod")
	h = newHarness(t, forbidProd)
	h.refused("--context", "prod", "delete", "pod", "x", "--yes", "--context", "dev")

	// -context=prod is NOT an override in pflag, but a clean dev run must not
	// be blocked by it either.
	h = newHarness(t, forbidProd)
	if code, _ := h.run("", "delete", "pod", "x", "--yes"); code != 0 {
		t.Errorf("plain dev delete refused: %d", code)
	}
}

func TestKubeconfigCandidatesFailClosed(t *testing.T) {
	h := newHarness(t, forbidProd)
	other := filepath.Join(h.dir, "other")
	data, _ := os.ReadFile(filepath.Join(h.dir, "kubeconfig"))
	must(t, os.WriteFile(other, []byte(strings.Replace(string(data), "current-context: dev", "current-context: prod", 1)), 0o600))
	h.refused("--kubeconfig", other, "delete", "pod", "x", "--yes")
	h = newHarness(t, forbidProd)
	h.refused("delete", "pod", "x", "--yes", "--kubeconfig="+other)
}

func TestDryRunBareDoesNotSwallowNextFlag(t *testing.T) {
	h := newHarness(t, forbidProd)
	h.refused("apply", "-f", "x.yaml", "--dry-run", "--context=prod", "--dry-run=none", "--yes")

	h = newHarness(t, "")
	code, stderr := h.run("", "--dry-run", "apply", "-f", "x.yaml", "--yes")
	if code != 0 || !strings.Contains(h.callLog(), "apply --dry-run=client") {
		t.Errorf("--dry-run before verb: code=%d calls=%q stderr=%q", code, h.callLog(), stderr)
	}
}

func TestPerVerbValueFlagsBeforeVerbStillGuarded(t *testing.T) {
	h := newHarness(t, forbidProd)
	h.refused("-l", "app=web", "--context", "prod", "delete", "pods", "--yes")
	h = newHarness(t, forbidProd)
	h.refused("-f", "x.yaml", "--context", "prod", "delete", "--yes")
	h = newHarness(t, forbidProd)
	h.refused("--timeout", "30s", "--context", "prod", "drain", "node1", "--yes")

	// Still runs normally on dev.
	h = newHarness(t, forbidProd)
	if code, _ := h.run("", "-l", "app=web", "delete", "pods", "--yes"); code != 0 || !strings.Contains(h.callLog(), "delete") {
		t.Errorf("dev: code=%d calls=%q", code, h.callLog())
	}

	if v, i, _ := firstVerbAt([]string{"-o", "pod", "get", "deploy/x"}); v != "get" || i != 2 {
		t.Errorf("firstVerbAt = %q,%d", v, i)
	}
}

func TestBackstopCatchesMisroutedGuardedVerb(t *testing.T) {
	// --foo is unknown, so `bar` looks like the verb; the guarded token
	// `delete` must still be forbid-checked.
	h := newHarness(t, forbidProd)
	h.refused("--context", "prod", "--foo", "bar", "delete", "pods", "--yes")
	// Without --yes a misrouted mutating verb refuses in non-TTY.
	h = newHarness(t, "")
	h.refused("--foo", "bar", "delete", "pods")
}

func TestKubectlVerbosityFlag(t *testing.T) {
	for _, argv := range [][]string{
		{"-v", "8", "top", "pods"},
		{"-v8", "top", "pods"},
		{"--v", "8", "top", "pods"},
		{"--v=8", "top", "pods"},
	} {
		h := newHarness(t, "")
		if code, stderr := h.run("", argv...); code != 0 || !strings.Contains(h.callLog(), "--v=8 top pods") {
			t.Errorf("%v: code=%d calls=%q stderr=%q", argv, code, h.callLog(), stderr)
		}
	}
	h := newHarness(t, "")
	code, _ := h.run("", "-v", "8", "scale", "--replicas=3", "deploy/x")
	if code != 0 || !strings.Contains(h.callLog(), "--v=8") || !strings.Contains(h.callLog(), "scale") {
		t.Errorf("enhanced: code=%d calls=%q", code, h.callLog())
	}
	if code, _ := h.run("", "-v", "8", "config", "path"); code != 0 {
		t.Errorf("cobra-parsed command with -v 8: code=%d", code)
	}
}

func TestBrokenConfigReadOnlyBySubcommand(t *testing.T) {
	broken := "contexts:\n  x:\n    forbids: [a]\n"
	h := newHarness(t, broken)
	h.refused("auth", "reconcile", "-f", "rbac.yaml")
	h = newHarness(t, broken)
	h.refused("rollout", "frobnicate", "deploy/x")
	h = newHarness(t, broken)
	if code, _ := h.run("", "auth", "can-i", "get", "pods"); code != 0 {
		t.Errorf("auth can-i should work: %d", code)
	}
	if code, _ := h.run("", "rollout", "status", "deploy/x"); code != 0 {
		t.Errorf("rollout status should work: %d", code)
	}
}

func TestRolloutUndoMultipleTargetsAndSources(t *testing.T) {
	h := newHarness(t, "")
	_, stderr := h.run("", "rollout", "undo", "deploy/a", "deploy/b")
	if !strings.Contains(stderr, "ROLLOUT-2") || h.callLog() != "" {
		t.Errorf("want ROLLOUT-2 phrase, got %q calls=%q", stderr, h.callLog())
	}
	h = newHarness(t, "")
	h.refused("rollout", "undo", "-f", "m.yaml")
	h = newHarness(t, "")
	if code, _ := h.run("", "rollout", "undo", "-f", "m.yaml", "--yes"); code != 0 || !strings.Contains(h.callLog(), "rollout undo -f m.yaml") {
		t.Errorf("undo -f --yes: code=%d calls=%q", code, h.callLog())
	}
	h = newHarness(t, "")
	_, stderr = h.run("", "rollout", "undo", "deployment", "a", "b")
	if !strings.Contains(stderr, "ROLLOUT-2") {
		t.Errorf("TYPE a b counts two targets: %q", stderr)
	}
}

func TestDeleteAttachedShorthandsAndCommaTargets(t *testing.T) {
	for _, a := range [][]string{{"-fx.yaml"}, {"-f-"}, {"-lapp=x"}, {"-kdir"}} {
		h := newHarness(t, "")
		h.refused(append([]string{"delete"}, a...)...)
		h = newHarness(t, "")
		args := append(append([]string{"delete"}, a...), "--yes")
		if code, stderr := h.run("", args...); code != 0 || !strings.Contains(h.callLog(), "delete") {
			t.Errorf("%v: code=%d stderr=%q calls=%q", args, code, stderr, h.callLog())
		}
	}
	h := newHarness(t, "")
	_, stderr := h.run("", "delete", "pod/a,pod/b")
	if !strings.Contains(stderr, "DELETE-2") {
		t.Errorf("comma targets must count 2: %q", stderr)
	}
	_, stderr = h.run("", "delete", "pod", "a,b")
	if !strings.Contains(stderr, "DELETE-2") {
		t.Errorf("comma names must count 2: %q", stderr)
	}
}

func TestExplicitAITimeoutBeatsConfigForDisabledParsingCommands(t *testing.T) {
	newHarness(t, "ai:\n  timeout: 2m\n")
	// `get` has DisableFlagParsing: cobra never sees --ai-timeout.
	Execute([]string{"get", "pods", "--ai-timeout", "5s"})
	if Flags.AITimeout != 5*time.Second {
		t.Errorf("explicit --ai-timeout lost to config: %v", Flags.AITimeout)
	}
	Execute([]string{"get", "pods"})
	if Flags.AITimeout != 2*time.Minute {
		t.Errorf("config default not applied: %v", Flags.AITimeout)
	}
}

func TestPassthroughBanner(t *testing.T) {
	newHarness(t, "contexts:\n  prod:\n    banner: PRODBANNER\n")
	h := &harness{t: t}
	Flags.Context = "prod"
	loadPolicy()
	var got string
	got = captureStderr(t, func() { showPassthroughBanner(true) })
	if !strings.Contains(got, "PRODBANNER") {
		t.Errorf("banner missing: %q", got)
	}
	if got = captureStderr(t, func() { showPassthroughBanner(false) }); got != "" {
		t.Errorf("banner must not pollute non-TTY stderr: %q", got)
	}
	_ = h
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	f()
	_ = w.Close()
	os.Stderr = old
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	return string(buf[:n])
}
