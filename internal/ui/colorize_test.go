package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain forces lipgloss into a colored profile so the per-test SetStdoutTTYForTest
// flips actually result in ANSI output. Without this, lipgloss auto-detects no
// terminal in `go test` and falls back to the Ascii profile (no escape codes).
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	os.Exit(m.Run())
}

// hasANSI reports whether s contains an ANSI CSI escape. Sufficient for
// "did the colorizer paint anything?" assertions without depending on the
// specific palette index lipgloss picks.
func hasANSI(s string) bool { return strings.Contains(s, "\x1b[") }

func TestColorizersNoOpWhenPlain(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = true
	defer func() { Plain = prevPlain }()

	for name, got := range map[string]string{
		"status":   ColorizeStatus("Running"),
		"ready":    ColorizeReady("3/3"),
		"restarts": ColorizeRestarts("7"),
		"node":     ColorizeNodeStatus("NotReady"),
		"event":    ColorizeEventType("Warning"),
		"svc":      ColorizeServiceType("LoadBalancer"),
		"age":      ColorizeAge("5d"),
		"log":      ColorizeLogLine("ERROR something failed"),
	} {
		if hasANSI(got) {
			t.Errorf("%s: expected no ANSI when Plain=true, got %q", name, got)
		}
	}
}

func TestColorizersNoOpWhenNotTTY(t *testing.T) {
	restore := SetStdoutTTYForTest(false)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	if got := ColorizeStatus("CrashLoopBackOff"); hasANSI(got) {
		t.Errorf("expected no ANSI when stdout is not a TTY, got %q", got)
	}
	if got := ColorizeLogLine("ERROR"); hasANSI(got) {
		t.Errorf("expected no ANSI when stdout is not a TTY, got %q", got)
	}
}

func TestColorizeStatus(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	cases := []struct {
		in     string
		styled bool
	}{
		{"Running", true},
		{"Pending", true},
		{"Succeeded", true},
		{"Terminating", true},
		{"CrashLoopBackOff", true},
		{"ImagePullBackOff", true},
		{"OOMKilled", true},
		{"Error", true},
		{"Evicted", true},
		{"Init:0/1", true},
		{"", false},
		{"SomethingUnknown", false},
	}
	for _, tc := range cases {
		got := ColorizeStatus(tc.in)
		if tc.styled && !hasANSI(got) {
			t.Errorf("ColorizeStatus(%q): expected ANSI, got %q", tc.in, got)
		}
		if !tc.styled && hasANSI(got) {
			t.Errorf("ColorizeStatus(%q): expected no ANSI, got %q", tc.in, got)
		}
		if !strings.Contains(got, tc.in) {
			t.Errorf("ColorizeStatus(%q): output %q must contain original text", tc.in, got)
		}
	}
}

func TestColorizeReady(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	cases := []struct {
		in     string
		styled bool
	}{
		{"3/3", true},
		{"2/3", true},
		{"0/3", true},
		{"True", true},
		{"False", true},
		{"Unknown", true},
		{"", false},
		{"not-a-ratio", false},
	}
	for _, tc := range cases {
		got := ColorizeReady(tc.in)
		if tc.styled && !hasANSI(got) {
			t.Errorf("ColorizeReady(%q): expected ANSI, got %q", tc.in, got)
		}
		if !tc.styled && hasANSI(got) {
			t.Errorf("ColorizeReady(%q): expected no ANSI, got %q", tc.in, got)
		}
	}
}

func TestColorizeRestarts(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	if got := ColorizeRestarts("0"); !hasANSI(got) {
		t.Errorf("0 should still be styled (subtle), got %q", got)
	}
	if got := ColorizeRestarts("3"); !hasANSI(got) {
		t.Errorf("low count should be warning, got %q", got)
	}
	if got := ColorizeRestarts("99"); !hasANSI(got) {
		t.Errorf("high count should be danger, got %q", got)
	}
	if got := ColorizeRestarts("7 (5m ago)"); !hasANSI(got) {
		t.Errorf("`N (X ago)` form should still parse, got %q", got)
	}
	if got := ColorizeRestarts("nonsense"); hasANSI(got) {
		t.Errorf("non-numeric should be returned unchanged, got %q", got)
	}
}

func TestColorizeLogLine(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	cases := []struct {
		name   string
		in     string
		styled bool
	}{
		{"panic", "panic: runtime error", true},
		{"fatal", "fatal: cannot start", true},
		{"bracket error", "[ERROR] something went wrong", true},
		{"bare WARN", "2025-01-01T12:00:00Z WARN something", true},
		{"INFO with ts", "2025-01-01T12:00:00Z INFO hello", true},
		{"DEBUG", "DEBUG: started", true},
		{"http 500", `GET /foo 500 12.3ms`, true},
		{"http 200", `POST /bar 200 4ms`, true},
		{"stack frame", "\tat com.example.Foo.bar(Foo.java:42)", true},
		{"plain", "just a normal line", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ColorizeLogLine(tc.in)
			if tc.styled && !hasANSI(got) {
				t.Errorf("expected ANSI for %q, got %q", tc.in, got)
			}
			if !tc.styled && hasANSI(got) {
				t.Errorf("expected no ANSI for %q, got %q", tc.in, got)
			}
		})
	}
}
