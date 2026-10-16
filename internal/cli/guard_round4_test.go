package cli

import (
	"strings"
	"testing"
)

func TestMovedFlagsKeepOrderAndLastYesWins(t *testing.T) {
	h := newHarness(t, "")
	h.refused("--context", "dev", "delete", "--yes", "pod", "x", "--yes=false")

	h = newHarness(t, e2eCfg)
	code, _ := h.run("", "--context", "prod", "delete", "pod", "x", "--context", "dev", "--yes")
	if code == 0 && strings.Contains(h.callLog(), "--context prod") {
		t.Errorf("prod must be policy-checked: %q", h.callLog())
	}
	if code == 0 {
		t.Errorf("prod candidate should be forbidden")
	}
	// Order preserved with a permissive policy: kubectl last-wins -> dev.
	h = newHarness(t, "")
	code, stderr := h.run("", "--context", "prod", "delete", "pod", "x", "--context", "dev", "--yes")
	log := h.callLog()
	i, j := strings.Index(log, "--context prod"), strings.Index(log, "--context dev")
	if code != 0 || i < 0 || j < 0 || i > j {
		t.Errorf("code=%d calls=%q stderr=%q", code, log, stderr)
	}
}

func TestPassthroughForwardsOriginalArgvUntouched(t *testing.T) {
	h := newHarness(t, "")
	code, stderr := h.run("", "myplugin", "--audit=/tmp/x.log", "--yes", "--ai", "foo")
	want := "myplugin --audit=/tmp/x.log --yes --ai foo"
	if code != 0 || !strings.Contains(h.callLog(), want) {
		t.Errorf("code=%d calls=%q stderr=%q", code, h.callLog(), stderr)
	}
}
