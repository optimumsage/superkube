package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Round 3: the invariant "a flag before the verb is never silently dropped",
// mirroring the black-box acceptance script case by case.

const e2eCfg = `
contexts:
  "prod*":
    banner: PROD
    forbid: ["delete", "apply", "patch", "drain", "scale --replicas=0"]
`

func TestAcceptanceBlocked(t *testing.T) {
	cases := [][]string{
		{"--context", "prod", "delete", "pod", "x", "--yes"},
		{"--as", "admin", "--context", "prod", "delete", "pod", "x", "--yes"},
		{"--context", "prod", "patch", "deploy", "x", "-p", "{}"},
		{"--context", "prod", "apply", "--field-manager", "--context=dev", "-f", "m.yaml", "--dry-run=none", "--yes"},
		{"apply", "-f", "m.yaml", "--dry-run", "--context=prod", "--dry-run=none", "--yes"},
		{"-l", "app=web", "--context", "prod", "delete", "pods", "--yes"},
		{"--context", "prod", "delete", "pods", "-l", "--context=dev", "--yes"},
		{"-context=dev", "--context", "prod", "delete", "pod", "x", "--yes"},
		{"--context", "prod", "scale", "deploy/x", "--replicas", "0", "--yes"},
		{"--context", "prod", "scale", "deploy/x", "--replicas=0", "--yes"},
		{"-Rf", "m.yaml", "delete"},
		{"--context", "prod", "delete", "pod", "x", "-n", "default", "--yes"},
		{"--context", "prod", "drain", "node1", "--yes"},
		{"--context", "dev", "--yes=false", "delete", "pod", "x"},
		{"--context", "dev", "delete", "pod", "x"},
		{"--context", "prod", "-n", "kube-system", "delete", "pod", "x", "--yes"},
		{"--help", "delete", "pod", "x", "--yes"},
		{"-h", "delete", "pod", "x", "--yes"},
		{"--version", "delete", "pod", "x", "--yes"},
		{"--context", "dev", "delete", "pod", "x", "--audit", "--yes"},
		{"--context", "dev", "delete", "pod", "x", "--ai-timeout", "--yes"},
		{"--context", "dev", "delete", "pod", "x", "-y=false"},
		{"--context", "dev", "delete", "pod", "x", "-R=false", "-y=0"},
		{"--context", "prod", "-v", "+0", "delete", "pod", "x", "--yes"},
		{"--context", "prod", "-v", "-1", "delete", "pod", "x", "--yes"},
		{"--ai", "bogus", "delete", "pod", "x", "--yes"},
		{"--field-validation", "x", "--context", "prod", "delete", "pod", "y", "--yes"},
	}
	for _, argv := range cases {
		h := newHarness(t, e2eCfg)
		h.refused(argv...)
	}
}

func TestAcceptanceLegit(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"--context", "dev", "delete", "pod", "x", "--verbose", "--yes"}, "delete"},
		{[]string{"--context", "dev", "get", "pods", "--plain"}, ""}, // get uses kubectl.Default; covered below
		{[]string{"--context", "dev", "top", "pods", "-o", "wide"}, "top pods -o wide"},
		{[]string{"--request-timeout", "5s", "--context", "dev", "top", "pods"}, "--request-timeout"},
		{[]string{"--context", "dev", "krewplugin", "--foo", "bar"}, "krewplugin --foo bar"},
		{[]string{"--context", "prod", "auth", "can-i", "delete", "pods"}, "auth can-i delete pods"},
		{[]string{"--context", "dev", "--yes", "apply", "-f", "-"}, "diff"},
		{[]string{"--context", "dev", "--yes", "apply", "-f", "m.yaml", "--dry-run=server"}, "--dry-run=server"},
		{[]string{"--context", "dev", "delete", "-Rf", "dir", "--yes"}, "delete"},
		{[]string{"--context", "prod", "scale", "deploy/x", "--replicas=3", "--yes"}, "scale"},
		{[]string{"-n", "web", "--context", "dev", "configmap", "edit", "x"}, "edit configmap"},
	}
	for _, tc := range cases {
		if tc.want == "" {
			continue
		}
		h := newHarness(t, e2eCfg)
		if code, stderr := h.run("manifest\n", tc.argv...); code != 0 || !strings.Contains(h.callLog(), tc.want) {
			t.Errorf("%v: code=%d calls=%q stderr=%q", tc.argv, code, h.callLog(), stderr)
		}
	}
}

// TestBlackBoxGetPaths builds the real binary: `sk get` uses the process-wide
// kubectl singleton, so it can't be driven in-process against a fake.
func TestBlackBoxGetPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	h := newHarness(t, "")
	bin := filepath.Join(h.dir, "sk")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/superkube").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(args ...string) string {
		_ = os.Remove(filepath.Join(h.dir, "calls.log"))
		cmd := exec.Command(bin, args...)
		cmd.Env = os.Environ()
		_ = cmd.Run()
		return h.callLog()
	}
	if got := run("--context", "dev", "get", "pods", "--plain"); !strings.Contains(got, "get pods") || strings.Contains(got, "--plain") {
		t.Errorf("get pods --plain: %q", got)
	}
	if got := run("-v", "8", "get", "pods"); !strings.Contains(got, "--v=8") {
		t.Errorf("-v 8 get pods: %q", got)
	}
	if got := run("--plain", "--audit", "off", "--context", "dev", "get", "pods"); strings.Contains(got, "--plain") || strings.Contains(got, "--audit") {
		t.Errorf("leading sk flags leaked: %q", got)
	}
	if got := run("--context", "dev", "get", "pods", "-o", "wide"); !strings.Contains(got, "pods -o wide") || !strings.Contains(got, "--context dev") {
		t.Errorf("get -o wide: %q", got)
	}
}

func TestLeadingDryRunIsForwardedAndNeedsNoConfirmation(t *testing.T) {
	for _, argv := range [][]string{
		{"--dry-run=client", "delete", "pod", "x"},
		{"--dry-run=server", "scale", "--replicas=0", "deploy/x"},
		{"--dry-run=client", "rollout", "restart", "deploy/x"},
		{"--dry-run=client", "drain", "n1"},
		{"--dry-run=client", "cordon", "n1"},
	} {
		h := newHarness(t, "")
		code, stderr := h.run("", argv...)
		if code != 0 || !strings.Contains(h.callLog(), "--dry-run=") {
			t.Errorf("%v: code=%d calls=%q stderr=%q", argv, code, h.callLog(), stderr)
		}
	}
	// Passthrough keeps it too, and policy still beats a dry-run.
	h := newHarness(t, e2eCfg)
	h.refused("--context", "prod", "--dry-run=client", "delete", "pod", "x")
}

func TestLeadingHelpNeverRuns(t *testing.T) {
	h := newHarness(t, "")
	if code, stderr := h.run("", "--help", "delete", "pod", "x", "--yes"); code == 0 ||
		h.callLog() != "" || !strings.Contains(stderr, "after the verb") {
		t.Errorf("code=%d calls=%q stderr=%q", code, h.callLog(), stderr)
	}
	// Trailing help shows help without deleting.
	h = newHarness(t, "")
	h.run("", "delete", "pod", "x", "--yes", "--help")
	if h.callLog() != "" {
		t.Errorf("trailing --help ran kubectl: %q", h.callLog())
	}
}

func TestValueFlagsValidatedBeforeAnythingRuns(t *testing.T) {
	h := newHarness(t, "")
	code, stderr := h.run("", "delete", "pod", "x", "--audit", "--yes")
	if code != 1 || h.callLog() != "" || !strings.Contains(stderr, "--audit") {
		t.Errorf("code=%d calls=%q stderr=%q", code, h.callLog(), stderr)
	}
	h = newHarness(t, "")
	h.refused("--audit=maybe", "delete", "pod", "x", "--yes")
}

func TestStripSkFlagsClusters(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"-yRf", "dir", "pod"}, []string{"-Rf", "dir", "pod"}},
		{[]string{"-y"}, []string{}},
		{[]string{"-v", "pods"}, []string{"pods"}},
		{[]string{"--verbose", "--plain", "x"}, []string{"x"}},
		{[]string{"-v", "8", "x"}, []string{"-v", "8", "x"}},
		{[]string{"-Rf", "dir"}, []string{"-Rf", "dir"}},
		{[]string{"--audit", "off", "--ai-timeout=5s", "a"}, []string{"a"}},
		{[]string{"-y=false", "a"}, []string{"a"}},
	}
	for _, tc := range cases {
		if got := stripSkFlags(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("stripSkFlags(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBoolFlagUsesOneWalk(t *testing.T) {
	if boolFlag([]string{"--audit", "--yes"}, "yes") {
		t.Error("--yes consumed as --audit's value must not count as --yes")
	}
	if boolFlag([]string{"-y=false"}, "yes") || !boolFlag([]string{"-yRf", "x"}, "yes") {
		t.Error("cluster / =false handling wrong")
	}
}

func TestSubcommandChainSkipsFlagsAndPolicySeesThem(t *testing.T) {
	h := newHarness(t, "contexts:\n  dev:\n    forbid: [\"edit -n kube-system\"]\n")
	h.refused("configmap", "-n", "kube-system", "edit", "x")
	h = newHarness(t, "contexts:\n  dev:\n    forbid: [\"edit -n kube-system\"]\n")
	if code, _ := h.run("", "configmap", "-n", "web", "edit", "x"); code != 0 {
		t.Errorf("other namespace should be allowed: %d", code)
	}
}

func TestNormalizeVerbosityRespectsLeadZone(t *testing.T) {
	// `--ai -v8` after the verb: --ai is boolean, so -v8 is a real flag.
	got := normalizeVerbosity([]string{"logs", "--ai", "-v8"}, 0, true)
	if !reflect.DeepEqual(got, []string{"logs", "--ai", "--v=8"}) {
		t.Errorf("got %v", got)
	}
}
