package cli

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Review round 2: reordering regressions and remaining pflag-emulation holes.

func TestOriginalArgvForPassthroughAndAudit(t *testing.T) {
	h := newHarness(t, "")
	argv := []string{"--token", "-v8", "top", "pods"}
	if code, _ := h.run("", argv...); code != 0 || strings.TrimSpace(h.callLog()) != strings.Join(argv, " ") {
		t.Errorf("token value must not be rewritten: calls=%q", h.callLog())
	}
	m := lastAudit(t, h)
	if got := fmt.Sprint(m["argv"]); !strings.Contains(got, "-v8") || strings.Contains(got, "--v=8") {
		t.Errorf("audit argv must be the original: %v", got)
	}

	h = newHarness(t, "")
	argv = []string{"myplugin", "-v", "8", "x"}
	h.run("", argv...)
	if strings.TrimSpace(h.callLog()) != strings.Join(argv, " ") {
		t.Errorf("plugin argv changed: %q", h.callLog())
	}

	// Enhanced commands must not corrupt a value that merely looks like -v8.
	h = newHarness(t, "")
	code, _ := h.run("", "--token", "-v8", "scale", "--replicas=3", "deploy/x")
	if code != 0 || !strings.Contains(h.callLog(), "--token -v8") {
		t.Errorf("enhanced: code=%d calls=%q", code, h.callLog())
	}
}

func TestReadOnlyVerbWithGuardedWordIsNotBlocked(t *testing.T) {
	h := newHarness(t, "contexts:\n  dev:\n    forbid: [delete, apply]\n")
	if code, stderr := h.run("", "auth", "can-i", "delete", "pods"); code != 0 {
		t.Errorf("auth can-i delete pods blocked: %d %s", code, stderr)
	}
	h = newHarness(t, "contexts:\n  x:\n    forbids: [a]\n")
	if code, stderr := h.run("", "auth", "can-i", "delete", "pods"); code != 0 {
		t.Errorf("auth can-i with broken config blocked: %d %s", code, stderr)
	}
}

func TestAmbiguousLeadingFlagsRefusedWithGuidance(t *testing.T) {
	for _, argv := range [][]string{
		{"-Rf", "x.yaml", "delete", "--yes"},
		{"--unknown", "foo", "delete", "pods", "--yes"},
		{"-l", "app=web", "delete", "pods", "--yes"},
	} {
		h := newHarness(t, "")
		code, stderr := h.run("", argv...)
		if code != 1 || h.callLog() != "" || !strings.Contains(stderr, "put command flags after the verb") {
			t.Errorf("%v: code=%d calls=%q stderr=%q", argv, code, h.callLog(), stderr)
		}
	}
	// Round 3: an ambiguous leading flag plus ANY later known verb is
	// refused; an unknown verb (krew plugin) stays verbatim passthrough.
	h := newHarness(t, "")
	h.refused("--unknown", "top", "pods")
	h = newHarness(t, "")
	if code, _ := h.run("", "--unknown", "myplugin", "pods"); code != 0 || !strings.Contains(h.callLog(), "myplugin pods") {
		t.Errorf("plugin passthrough: code=%d calls=%q", code, h.callLog())
	}
}

func TestSkRootFlagsNotForwardedAndHonored(t *testing.T) {
	h := newHarness(t, "")
	code, _ := h.run("", "--ai", "claude", "--plain", "--audit", "off", "delete", "pod", "x", "--yes")
	if code != 0 || strings.TrimSpace(h.callLog()) != "delete pod x" {
		t.Errorf("sk flags leaked to kubectl: code=%d calls=%q", code, h.callLog())
	}
	h = newHarness(t, "")
	code, _ = h.run("", "delete", "pod", "x", "--plain", "--yes", "--audit=off")
	if code != 0 || strings.TrimSpace(h.callLog()) != "delete pod x" {
		t.Errorf("trailing sk flags leaked: code=%d calls=%q", code, h.callLog())
	}
}

func TestYesFalseMeansFalse(t *testing.T) {
	for _, argv := range [][]string{
		{"--yes=false", "delete", "pod", "x"},
		{"delete", "pod", "x", "--yes=false"},
		{"delete", "pod", "x", "--yes", "--yes=0"},
		{"scale", "--replicas=0", "deploy/x", "--yes=false"},
	} {
		h := newHarness(t, "")
		h.refused(argv...)
	}
}

func TestKubectlGlobalsKeptAfterLeafCommand(t *testing.T) {
	h := newHarness(t, "")
	code, stderr := h.run("", "-n", "prod", "configmap", "edit", "x")
	if code != 0 || !strings.Contains(h.callLog(), "edit configmap") || !strings.Contains(h.callLog(), "prod") {
		t.Errorf("-n lost: code=%d calls=%q stderr=%q", code, h.callLog(), stderr)
	}
	h = newHarness(t, "")
	code, _ = h.run("", "--context", "prod", "configmap", "view", "x")
	if code != 0 || !strings.Contains(h.callLog(), "--context prod") {
		t.Errorf("--context lost: code=%d calls=%q", code, h.callLog())
	}
}

func TestShortClusters(t *testing.T) {
	h := newHarness(t, "")
	code, stderr := h.run("", "delete", "-Rf", "manifests", "--yes")
	if code != 0 || !strings.Contains(h.callLog(), "delete -Rf manifests") {
		t.Errorf("-Rf: code=%d stderr=%q calls=%q", code, stderr, h.callLog())
	}
	h = newHarness(t, "")
	h.refused("delete", "-Rf", "manifests")

	h = newHarness(t, "contexts:\n  prod:\n    forbid: [\"delete --all-namespaces\", \"delete -n kube-system\"]\n")
	h.refused("--context", "prod", "delete", "pods", "-A", "--yes")
	h = newHarness(t, "contexts:\n  prod:\n    forbid: [\"delete -n kube-system\"]\n")
	h.refused("--context", "prod", "delete", "pods", "x", "-An", "kube-system", "--yes")

	if got := positionalArgs([]string{"-An", "kube-system", "pod", "x"}); !reflect.DeepEqual(got, []string{"pod", "x"}) {
		t.Errorf("positionalArgs = %v", got)
	}
	if got := positionalArgs([]string{"--ai", "my-pod"}); !reflect.DeepEqual(got, []string{"my-pod"}) {
		t.Errorf("--ai must be boolean after the verb: %v", got)
	}
	if v, i, _ := firstVerbAt([]string{"--ai", "claude", "logs", "p"}); v != "logs" || i != 2 {
		t.Errorf("leading --ai takes a value: %q %d", v, i)
	}
}

func TestBareCascadeAndDryRunNeverConsumeNext(t *testing.T) {
	h := newHarness(t, "")
	_, stderr := h.run("", "delete", "pod", "keep-me", "--cascade", "orphan")
	if !strings.Contains(stderr, "DELETE-2") {
		t.Errorf("orphan counts as a second target: %q", stderr)
	}
	h = newHarness(t, "")
	code, _ := h.run("", "apply", "-f", "x.yaml", "--dry-run", "--yes")
	if code != 0 || !strings.Contains(h.callLog(), "apply --dry-run=client -f x.yaml") {
		t.Errorf("bare --dry-run: code=%d calls=%q", code, h.callLog())
	}
	h = newHarness(t, "")
	code, _ = h.run("", "--dry-run=none", "apply", "-f", "x.yaml", "--yes")
	if code != 0 || strings.TrimSpace(h.callLog()) != "apply -f x.yaml" {
		t.Errorf("leading --dry-run=none: code=%d calls=%q", code, h.callLog())
	}
}

func TestContextAfterUnknownFlagIsAmbiguous(t *testing.T) {
	h := newHarness(t, forbidProd)
	h.setCurrent("prod")
	h.refused("apply", "--schema-cache-dir", "--context=dev", "-f", "x.yaml", "--dry-run=none", "--yes")

	// After a KNOWN boolean the override is unambiguous: dev is allowed.
	h = newHarness(t, forbidProd)
	h.setCurrent("prod")
	if code, _ := h.run("", "delete", "pod", "x", "--force", "--context=dev", "--yes"); code != 0 {
		t.Errorf("unambiguous dev override refused: %d", code)
	}
}

func TestCommaKindsAndPrompt(t *testing.T) {
	h := newHarness(t, "")
	_, stderr := h.run("", "delete", "pod,svc", "my-app")
	if !strings.Contains(stderr, "DELETE-2") {
		t.Errorf("pod,svc my-app is two targets: %q", stderr)
	}
	h = newHarness(t, "")
	_, stderr = h.run("", "rollout", "restart")
	if strings.Contains(stderr, "  ") || !strings.Contains(stderr, "Rollout restart?") {
		t.Errorf("prompt formatting: %q", stderr)
	}
}
