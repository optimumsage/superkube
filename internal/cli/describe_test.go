package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/ui"
)

const describePodFixture = `Name:         demo-pod
Namespace:    default
Status:       Running
IP:           10.0.0.5
Containers:
  app:
    Image:          ghcr.io/example/app:1.0.0
    State:          Running
    Ready:          True
    Restart Count:  3
Conditions:
  Type           Status
  Initialized    True
  Ready          False
  PodScheduled   True
Events:
  Type     Reason     Age    From      Message
  ----     ------     ----   ----      -------
  Normal   Scheduled  5m     default   pod assigned
  Warning  Unhealthy  2m     kubelet   liveness probe failed
`

func TestIsDescribeSectionHeader(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Containers:", true},
		{"Conditions:", true},
		{"Events:", true},
		{"  Containers:", false},   // indented = not a section header
		{"Status: Running", false}, // has a value on the same line
		{"Name: foo", false},
		{"", false},
		{"random line", false},
	}
	for _, tc := range cases {
		if got := isDescribeSectionHeader(tc.in); got != tc.want {
			t.Errorf("isDescribeSectionHeader(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRenderDescribeStylesSectionsAndEvents(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	renderDescribeOutput(&buf, describePodFixture)
	out := buf.String()

	// Strip ANSI and verify the visible text matches the input byte-for-byte
	// (modulo final newline). This is the safety guarantee — we only inject
	// zero-width escapes.
	if stripped := stripANSI(out); strings.TrimRight(stripped, "\n") != strings.TrimRight(describePodFixture, "\n") {
		t.Errorf("visible content drifted after styling:\n--- got ---\n%s\n--- want ---\n%s", stripped, describePodFixture)
	}

	// Some ANSI must be present — section headers, status values, event types
	// all expect coloring.
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escapes in styled output, got:\n%s", out)
	}
}

func TestRenderDescribeNoOpWhenNotStyled(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = true // disable styling
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	renderDescribeOutput(&buf, describePodFixture)
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("expected no ANSI when Plain=true, got:\n%s", out)
	}
}

func TestIsPlausibleLabel(t *testing.T) {
	good := []string{
		"Name", "Namespace", "Status",
		"Service Account", "Restart Count", "QoS Class", "Last Transition Time",
		"Node-Selectors", "Image ID", "Container ID",
		"kubectl.kubernetes.io/last-applied-configuration",
		"app.kubernetes.io/name",
	}
	for _, s := range good {
		if !isPlausibleLabel(s) {
			t.Errorf("isPlausibleLabel(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",
		`{"a"`,
		`"key"`,
		"[ERROR]",
		"-prefixed",
		"/leading-slash",
		"containerd",      // alone is fine — but the test is about edge inputs; this passes anyway
		" leading-space",  // starts with space
		"has\ttab\tchars", // tab disallowed
	}
	// "containerd" is actually a plausible label by my rules (alpha only), so
	// remove it from the bad list — the real test is that values like
	// `containerd://abc` (line, not label) never reach this function: the
	// label-shape check is applied to the *pre-colon* slice only.
	bad = bad[:0]
	bad = append(bad, "", `{"a"`, `"key"`, "[ERROR]", "-prefixed", "/leading-slash", " leading-space", "has\ttab\tchars")
	for _, s := range bad {
		if isPlausibleLabel(s) {
			t.Errorf("isPlausibleLabel(%q) = true, want false", s)
		}
	}
}

func TestDescribeContainerIDLineValueIsReadable(t *testing.T) {
	// Regression: the user reported that `Container ID:   containerd://...`
	// styled both the label and the value gray, making the line unreadable.
	// After the fix, the label is muted but the value keeps its default
	// terminal color (no ANSI applied to the value bytes).
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	line := "    Container ID:   containerd://8dd2e7cab60d89"
	out, _ := styleDescribeKeyValue(line)

	// The label should be wrapped in ANSI.
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected label to be styled, got %q", out)
	}
	// The value substring must NOT be wrapped in any ANSI escape.
	value := "containerd://8dd2e7cab60d89"
	vIdx := strings.Index(out, value)
	if vIdx < 0 {
		t.Fatalf("output missing the literal value %q: %q", value, out)
	}
	// Walk backwards from the value: between the last ':' of "Container ID:"
	// (followed by an ANSI reset "\x1b[0m") and the value, there must be no
	// open ANSI introducer (any "\x1b[" without a matching "\x1b[0m" between
	// it and the value).
	prefix := out[:vIdx]
	openIdx := strings.LastIndex(prefix, "\x1b[")
	if openIdx < 0 {
		t.Fatalf("expected an ANSI introducer in the label area, got %q", out)
	}
	// The most recent introducer in the prefix must be a reset (\x1b[0m),
	// meaning the value starts in default color.
	rest := prefix[openIdx:]
	if !strings.HasPrefix(rest, "\x1b[0m") {
		t.Errorf("value is inside an ANSI block (not reset before value):\n  prefix-tail: %q\n  full:        %q", rest, out)
	}
}

func TestDescribeIgnoresJSONSnippetAsLabel(t *testing.T) {
	// Annotation continuations can be JSON; the pre-colon slice ({"key") is
	// not a plausible label, so we leave the whole line alone.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	line := `                {"a": 1, "b": 2}`
	out, col := styleDescribeKeyValue(line)
	if out != line {
		t.Errorf("expected JSON-like line returned unchanged, got %q", out)
	}
	if col != -1 {
		t.Errorf("expected value column -1 for non-label line, got %d", col)
	}
}

func TestDescribeContinuationLineNotMuted(t *testing.T) {
	// Regression: lines wrapped onto multiple rows under a single field — most
	// commonly Tolerations and multi-key Annotations — start with a slug that
	// looks like a label ("node.kubernetes.io/not-ready:NoExecute"). Before
	// the fix, that slug was muted as if it were a key, producing visually
	// inconsistent output: the same shape was muted on continuation rows but
	// not muted inside the first row's value. After the fix, every line that
	// is indented at or past the first row's value column is passed through
	// unchanged.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	fixture := "" +
		"Tolerations:                 node.kubernetes.io/not-ready:NoExecute op=Exists for 300s\n" +
		"                             node.kubernetes.io/unreachable:NoExecute op=Exists for 300s\n" +
		"Annotations:    foo: bar\n" +
		"                kubectl.kubernetes.io/last-applied-configuration: yes\n" +
		"Status:         Running\n"

	var buf bytes.Buffer
	renderDescribeOutput(&buf, fixture)
	out := buf.String()

	if got := stripANSI(out); strings.TrimRight(got, "\n") != strings.TrimRight(fixture, "\n") {
		t.Fatalf("visible content drifted:\n--- got ---\n%s\n--- want ---\n%s", got, fixture)
	}

	// Each continuation line must be byte-identical to its source (no ANSI
	// injected) — locate the line in the output and assert it appears intact.
	for _, continuation := range []string{
		"                             node.kubernetes.io/unreachable:NoExecute op=Exists for 300s",
		"                kubectl.kubernetes.io/last-applied-configuration: yes",
	} {
		if !strings.Contains(out, continuation+"\n") {
			t.Errorf("continuation line should pass through untouched, but couldn't find raw %q in output:\n%s", continuation, out)
		}
	}

	// Sanity: real keys (Tolerations, Annotations, Status) ARE muted.
	for _, label := range []string{"Tolerations:", "Annotations:", "Status:"} {
		idx := strings.Index(out, label)
		if idx < 0 {
			t.Errorf("missing label %q in output", label)
			continue
		}
		if !strings.Contains(out[:idx+len(label)], "\x1b[") {
			t.Errorf("label %q was not styled", label)
		}
	}
}

func TestRenderDescribeNoisyValuesRemainReadable(t *testing.T) {
	// A real-ish kubectl describe pod chunk that hits every problematic case:
	//   - Container ID with URL-shaped value containing "://"
	//   - Image with colon-bearing image:tag
	//   - Annotation continuation with JSON content
	//   - Sub-section name "  app:"
	//   - State/Ready/Restart Count (should remain colored)
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	fixture := "" +
		"Name:           demo-pod\n" +
		"Status:         Running\n" +
		"Annotations:    kubectl.kubernetes.io/last-applied-configuration:\n" +
		"                  {\"apiVersion\":\"v1\",\"kind\":\"Pod\"}\n" +
		"Containers:\n" +
		"  app:\n" +
		"    Container ID:   containerd://8dd2e7cab60d89\n" +
		"    Image:          ghcr.io/example/app:1.0.0\n" +
		"    State:          Running\n" +
		"    Ready:          True\n" +
		"    Restart Count:  3\n"

	var buf bytes.Buffer
	renderDescribeOutput(&buf, fixture)
	out := buf.String()

	// Byte-stability: stripping ANSI must reproduce the input exactly.
	if got := stripANSI(out); strings.TrimRight(got, "\n") != strings.TrimRight(fixture, "\n") {
		t.Errorf("visible content drifted:\n--- got ---\n%s\n--- want ---\n%s", got, fixture)
	}

	// The noisy values must NOT be inside an ANSI color block. We check that
	// each value sits immediately after a reset (\x1b[0m) — i.e., outside
	// any open color span.
	for _, value := range []string{
		"containerd://8dd2e7cab60d89",
		"ghcr.io/example/app:1.0.0",
		`{"apiVersion":"v1","kind":"Pod"}`,
	} {
		idx := strings.Index(out, value)
		if idx < 0 {
			t.Errorf("output missing %q", value)
			continue
		}
		prefix := out[:idx]
		openIdx := strings.LastIndex(prefix, "\x1b[")
		if openIdx < 0 {
			continue // entire prefix has no ANSI — value is in default color
		}
		tail := prefix[openIdx:]
		if !strings.HasPrefix(tail, "\x1b[0m") {
			t.Errorf("value %q is inside an open ANSI color span — prefix-tail %q\n  full: %s",
				value, tail, out)
		}
	}

	// The status-bearing values must BE styled.
	for _, v := range []string{"Running", "True", "3"} {
		// Sloppy but sufficient: find the value preceded by ANSI introducer
		// somewhere on its line. We just assert at least one occurrence is
		// inside an ANSI block.
		idx := strings.Index(out, v)
		if idx < 0 {
			t.Errorf("output missing %q", v)
			continue
		}
		prefix := out[:idx]
		openIdx := strings.LastIndex(prefix, "\x1b[")
		if openIdx < 0 {
			t.Errorf("value %q should be styled, but no ANSI introducer precedes it", v)
			continue
		}
		tail := prefix[openIdx:]
		if strings.HasPrefix(tail, "\x1b[0m") {
			t.Errorf("value %q should be styled, but is preceded by a reset (%q)", v, tail)
		}
	}
}

func TestDescribeSubSectionStyled(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	// Indented "container-name:" with no value on the same line should be
	// recognized as a sub-section anchor.
	if !isDescribeSubSection("  app:") {
		t.Errorf("`  app:` should be a sub-section")
	}
	if !isDescribeSubSection("    Limits:") {
		t.Errorf("`    Limits:` should be a sub-section")
	}
	if isDescribeSubSection("Containers:") {
		t.Errorf("top-level lines should not match sub-section (they hit the section path instead)")
	}
	if isDescribeSubSection("  State: Running") {
		t.Errorf("indented Key: Value should not match sub-section")
	}
	if isDescribeSubSection(`  {"a": 1}`) {
		t.Errorf("JSON-like content should not match sub-section")
	}
}
