package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/optimumsage/superkube/internal/guardrail"
)

// These tests drive Execute in-process against a fake kubectl ($KUBECTL) so
// the full routing -> policy -> confirm -> exec path is covered. Every
// scenario below corresponds to a verified guardrail bypass or misbehavior.

const fakeKubectl = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/calls.log"
case "$*" in
  *"-f -"*|*"--filename=-"*) cat > "$FAKE_DIR/$1.stdin" ;;
esac
if [ "$1" = "diff" ] && [ -n "$FAKE_DIFF" ]; then
  echo "+changed"
  exit 1
fi
exit 0
`

const fakeKubeconfig = `apiVersion: v1
kind: Config
current-context: dev
contexts:
- name: dev
  context: {cluster: c, user: u}
- name: prod
  context: {cluster: c, user: u}
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: u
  user: {}
`

type harness struct {
	t   *testing.T
	dir string
}

func newHarness(t *testing.T, cfgYAML string) *harness {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "kubectl")
	must(t, os.WriteFile(script, []byte(fakeKubectl), 0o755))
	kc := filepath.Join(dir, "kubeconfig")
	must(t, os.WriteFile(kc, []byte(fakeKubeconfig), 0o600))
	t.Setenv("KUBECTL", script)
	t.Setenv("KUBECONFIG", kc)
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("FAKE_DIFF", "")
	t.Setenv("SUPERKUBE_AI", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	if cfgYAML != "" {
		p := filepath.Join(dir, "cfg", "superkube", "config.yaml")
		must(t, os.MkdirAll(filepath.Dir(p), 0o700))
		must(t, os.WriteFile(p, []byte(cfgYAML), 0o600))
	}
	return &harness{t: t, dir: dir}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// run executes sk in-process with stdin content and returns exit code and stderr.
func (h *harness) run(stdin string, args ...string) (int, string) {
	h.t.Helper()
	inR, inW, _ := os.Pipe()
	_, _ = inW.WriteString(stdin)
	_ = inW.Close()
	errR, errW, _ := os.Pipe()
	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = inR, errW
	done := make(chan string)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := errR.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	code := Execute(args)
	_ = errW.Close()
	os.Stdin, os.Stderr = oldIn, oldErr
	return code, <-done
}

func (h *harness) callLog() string {
	data, _ := os.ReadFile(filepath.Join(h.dir, "calls.log"))
	return string(data)
}

func (h *harness) file(name string) string {
	data, _ := os.ReadFile(filepath.Join(h.dir, name))
	return string(data)
}

const forbidProd = `
contexts:
  "prod*":
    forbid: [delete, apply, scale, rollout, cordon, drain, patch]
`

func TestForbidUsesContextFlag(t *testing.T) {
	cases := [][]string{
		{"--context", "prod", "delete", "pod", "x", "--yes"},
		{"--context=prod", "delete", "pod", "x", "--yes"},
		{"delete", "pod", "x", "--yes", "--context", "prod"},
		{"--as", "admin", "--context", "prod", "delete", "pod", "x", "--yes"},
		{"--context", "prod", "apply", "-f", "x.yaml", "--yes"},
		{"--context", "prod", "scale", "--replicas=0", "deploy/x", "--yes"},
		{"--context", "prod", "rollout", "restart", "deploy/x", "--yes"},
		{"--context", "prod", "cordon", "n1", "--yes"},
		{"--context", "prod", "drain", "n1", "--yes"},
	}
	for _, args := range cases {
		h := newHarness(t, forbidProd)
		code, stderr := h.run("", args...)
		if code == 0 || h.callLog() != "" {
			t.Errorf("%v: code=%d calls=%q; want refusal", args, code, h.callLog())
		}
		if !strings.Contains(stderr, "policy") {
			t.Errorf("%v: stderr %q does not mention policy", args, stderr)
		}
	}
}

func TestForbidNotAppliedToOtherContext(t *testing.T) {
	h := newHarness(t, forbidProd)
	code, stderr := h.run("", "delete", "pod", "x", "--yes")
	if code != 0 {
		t.Fatalf("dev delete refused: %d %s", code, stderr)
	}
	if !strings.Contains(h.callLog(), "delete") {
		t.Errorf("kubectl not invoked: %q", h.callLog())
	}
}

func TestForbidAppliesToPassthroughVerbs(t *testing.T) {
	h := newHarness(t, forbidProd)
	code, _ := h.run("", "--context", "prod", "patch", "deploy", "x", "-p", "{}")
	if code == 0 || h.callLog() != "" {
		t.Errorf("patch on prod: code=%d calls=%q", code, h.callLog())
	}
	code, _ = h.run("", "--context", "dev", "patch", "deploy", "x", "-p", "{}")
	if code != 0 || !strings.Contains(h.callLog(), "patch") {
		t.Errorf("patch on dev: code=%d calls=%q", code, h.callLog())
	}
}

func TestBrokenConfigFailsClosed(t *testing.T) {
	h := newHarness(t, "contexts:\n  \"prod*\":\n    forbids: [delete]\n")

	code, stderr := h.run("", "delete", "pod", "x", "--yes")
	if code == 0 || h.callLog() != "" {
		t.Errorf("delete with broken config: code=%d calls=%q", code, h.callLog())
	}
	if !strings.Contains(stderr, "config") || !strings.Contains(stderr, "invalid") {
		t.Errorf("stderr should explain invalid config: %q", stderr)
	}

	code, _ = h.run("", "patch", "deploy", "x", "-p", "{}")
	if code == 0 || h.callLog() != "" {
		t.Errorf("passthrough patch with broken config: code=%d calls=%q", code, h.callLog())
	}

	// Read-only commands keep working (with a warning).
	code, stderr = h.run("", "top", "pods")
	if code != 0 || !strings.Contains(h.callLog(), "top pods") {
		t.Errorf("get with broken config: code=%d calls=%q", code, h.callLog())
	}
	if !strings.Contains(stderr, "superkube: config ") {
		t.Errorf("expected one-line warning, got %q", stderr)
	}
}

func TestApplyStdinReachesBothDiffAndApply(t *testing.T) {
	h := newHarness(t, "")
	t.Setenv("FAKE_DIFF", "1")
	manifest := "kind: ConfigMap\n"
	code, stderr := h.run(manifest, "apply", "-f", "-", "--yes")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if got := h.file("diff.stdin"); got != manifest {
		t.Errorf("diff stdin = %q", got)
	}
	if got := h.file("apply.stdin"); got != manifest {
		t.Errorf("apply stdin = %q (stdin was consumed by the preview)", got)
	}
}

func TestApplyDryRunModes(t *testing.T) {
	t.Run("none skips preview and strips flag", func(t *testing.T) {
		h := newHarness(t, "")
		t.Setenv("FAKE_DIFF", "1")
		code, _ := h.run("", "apply", "-f", "x.yaml", "--dry-run=none", "--yes")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if got := strings.TrimSpace(h.callLog()); got != "apply -f x.yaml" {
			t.Errorf("calls = %q", got)
		}
	})
	t.Run("none still confirms", func(t *testing.T) {
		h := newHarness(t, "")
		code, _ := h.run("", "apply", "-f", "x.yaml", "--dry-run=none")
		if code == 0 || h.callLog() != "" {
			t.Errorf("non-TTY without --yes must refuse: code=%d calls=%q", code, h.callLog())
		}
	})
	t.Run("server forwards to apply without diff or confirm", func(t *testing.T) {
		h := newHarness(t, "")
		code, _ := h.run("", "apply", "-f", "x.yaml", "--dry-run=server")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if got := strings.TrimSpace(h.callLog()); got != "apply --dry-run=server -f x.yaml" {
			t.Errorf("calls = %q", got)
		}
	})
	t.Run("auto never forwards --dry-run to diff", func(t *testing.T) {
		h := newHarness(t, "")
		t.Setenv("FAKE_DIFF", "1")
		code, _ := h.run("", "apply", "-f", "x.yaml", "--dry-run=auto", "--yes")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		if got := h.callLog(); strings.Contains(got, "dry-run") || !strings.Contains(got, "diff -f x.yaml") {
			t.Errorf("calls = %q", got)
		}
	})
}

func TestDeclineExitsAborted(t *testing.T) {
	orig := promptYesNo
	promptYesNo = func(string, string, bool) error { return guardrail.ErrAborted }
	t.Cleanup(func() { promptYesNo = orig })

	cases := [][]string{
		{"apply", "-f", "x.yaml"},
		{"scale", "--replicas=0", "deploy/x"},
		{"cordon", "n1"},
		{"rollout", "restart", "deploy/x"},
	}
	for _, args := range cases {
		h := newHarness(t, "")
		t.Setenv("FAKE_DIFF", "1")
		code, _ := h.run("", args...)
		if code != 130 {
			t.Errorf("%v: code=%d, want 130", args, code)
		}
		if strings.Contains(h.callLog(), args[0]+" ") && !strings.HasPrefix(h.callLog(), "diff") {
			t.Errorf("%v: mutating call happened: %q", args, h.callLog())
		}
	}
}

func TestRolloutConfirmation(t *testing.T) {
	h := newHarness(t, "")
	code, _ := h.run("", "rollout", "restart", "deploy/x")
	if code == 0 || h.callLog() != "" {
		t.Errorf("restart without --yes must refuse non-interactively: code=%d calls=%q", code, h.callLog())
	}
	code, _ = h.run("", "rollout", "restart", "deploy/x", "--yes")
	if code != 0 || !strings.Contains(h.callLog(), "rollout restart") {
		t.Errorf("restart --yes: code=%d calls=%q", code, h.callLog())
	}
	// Read-only subcommands need no confirmation.
	code, _ = h.run("", "rollout", "status", "deploy/x")
	if code != 0 {
		t.Errorf("status: code=%d", code)
	}
}

func TestRolloutUndoTypesWorkloadNotNamespace(t *testing.T) {
	h := newHarness(t, "")
	_, stderr := h.run("", "rollout", "undo", "-n", "prod", "deploy/x")
	if !strings.Contains(stderr, "deploy/x") || strings.Contains(stderr, `"prod"`) {
		t.Errorf("confirmation should target deploy/x: %q", stderr)
	}
}

func TestDeleteArgParsing(t *testing.T) {
	h := newHarness(t, "")
	_, stderr := h.run("", "--context", "prod", "delete", "pod", "x")
	if strings.Contains(stderr, "DELETE-2") || !strings.Contains(stderr, `"x"`) {
		t.Errorf("--context value counted as a name: %q", stderr)
	}

	code, stderr := h.run("", "delete", "-f", "x.yaml", "--yes")
	if code != 0 || !strings.Contains(h.callLog(), "delete -f x.yaml") {
		t.Errorf("delete -f: code=%d stderr=%s calls=%q", code, stderr, h.callLog())
	}
	h2 := newHarness(t, "")
	code, _ = h2.run("", "delete", "-f", "x.yaml")
	if code == 0 || h2.callLog() != "" {
		t.Errorf("delete -f without --yes must refuse: code=%d calls=%q", code, h2.callLog())
	}
}

func TestRequireTypedConfirmUpgradesYesNo(t *testing.T) {
	h := newHarness(t, "guardrails:\n  require_typed_confirm: true\n")
	code, stderr := h.run("", "scale", "--replicas=0", "deploy/x")
	if code == 0 || !strings.Contains(stderr, "typed phrase") {
		t.Errorf("want typed-phrase refusal, code=%d stderr=%q", code, stderr)
	}
}

func lastAudit(t *testing.T, h *harness) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, "state", "superkube", "audit.log"))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("bad audit line %q: %v", lines[len(lines)-1], err)
	}
	return m
}

func TestAuditRecordsRealContextAndVerb(t *testing.T) {
	h := newHarness(t, "")
	h.run("", "--token", "abc", "--context", "prod", "-n", "web", "top", "pods")
	m := lastAudit(t, h)
	if m == nil {
		t.Fatal("no audit row")
	}
	if m["verb"] != "top" || m["context"] != "prod" || m["namespace"] != "web" {
		t.Errorf("audit row = %v", m)
	}

	h.run("", "delete", "pod", "x", "--yes", "--context", "prod", "-n", "web")
	m = lastAudit(t, h)
	if m["verb"] != "delete" || m["context"] != "prod" || m["namespace"] != "web" {
		t.Errorf("guarded audit row = %v", m)
	}
}

func TestAuditEnabledConfig(t *testing.T) {
	h := newHarness(t, "audit:\n  enabled: false\n")
	h.run("", "top", "pods")
	if lastAudit(t, h) != nil {
		t.Error("audit.enabled=false should disable audit")
	}
	h.run("", "--audit", "on", "top", "pods")
	if lastAudit(t, h) == nil {
		t.Error("--audit on should override audit.enabled=false")
	}
}

func TestConfigAIDefaults(t *testing.T) {
	newHarness(t, "ai:\n  provider: claude\n  timeout: 2m\n")
	Execute([]string{"config", "path"})
	if Flags.AIProvider != "claude" || Flags.AITimeout != 2*time.Minute {
		t.Errorf("config defaults not applied: %q %v", Flags.AIProvider, Flags.AITimeout)
	}

	t.Setenv("SUPERKUBE_AI", "agy")
	Execute([]string{"config", "path"})
	if Flags.AIProvider != "" {
		t.Errorf("SUPERKUBE_AI must beat config, got flag %q", Flags.AIProvider)
	}

	Execute([]string{"--ai", "agy", "--ai-timeout", "5s", "config", "path"})
	if Flags.AIProvider != "agy" || Flags.AITimeout != 5*time.Second {
		t.Errorf("explicit flags must beat config: %q %v", Flags.AIProvider, Flags.AITimeout)
	}
}
