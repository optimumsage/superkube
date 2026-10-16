package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

func TestParseWatchArgs(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		ok           bool
		res, objName string
		ns, sel, fs  string
		all, wide    bool
	}{
		{name: "basic", args: []string{"pods", "-w"}, ok: true, res: "pods"},
		{name: "watch first", args: []string{"-w", "pods"}, ok: true, res: "pods"},
		{name: "ns flag eats value", args: []string{"-n", "kube-system", "pods", "-w"}, ok: true, res: "pods", ns: "kube-system"},
		{name: "selector", args: []string{"-w", "deploy", "--selector", "app=web"}, ok: true, res: "deploy", sel: "app=web"},
		{name: "ns equals", args: []string{"--namespace=kube-system", "svc", "-w"}, ok: true, res: "svc", ns: "kube-system"},
		{name: "name", args: []string{"pod", "foo", "-w"}, ok: true, res: "pod", objName: "foo"},
		{name: "kind/name", args: []string{"deploy/web", "-w"}, ok: true, res: "deploy", objName: "web"},
		{name: "field selector", args: []string{"pods", "-w", "--field-selector=status.phase=Running"}, ok: true, res: "pods", fs: "status.phase=Running"},
		{name: "all ns wide", args: []string{"pods", "-A", "-o", "wide", "-w"}, ok: true, res: "pods", all: true, wide: true},
		{name: "context consumed", args: []string{"--context", "prod", "pods", "-w"}, ok: true, res: "pods"},
		{name: "multiple kinds", args: []string{"pods,svc", "-w"}},
		{name: "multiple names", args: []string{"pod", "a", "b", "-w"}},
		{name: "show-labels", args: []string{"pods", "--show-labels", "-w"}},
		{name: "sort-by", args: []string{"pods", "--sort-by=.metadata.name", "-w"}},
		{name: "json output", args: []string{"pods", "-o", "json", "-w"}},
		{name: "no positional", args: []string{"-w"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, ok := parseWatchArgs(tc.args)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (opts %+v)", ok, tc.ok, o)
			}
			if !ok {
				return
			}
			if o.Resource != tc.res || o.Name != tc.objName || o.Namespace != tc.ns ||
				o.Selector != tc.sel || o.FieldSelector != tc.fs || o.AllNamespaces != tc.all || o.Wide != tc.wide {
				t.Errorf("opts = %+v", o)
			}
		})
	}
}

// Rows wider than the terminal must be truncated, never wrapped: a wrapped
// row is two physical lines but one counted line, breaking cursor-up redraws.
func TestPrintGetFrameTruncatesToWidth(t *testing.T) {
	var buf bytes.Buffer
	n := printGetFrame(&buf, kube.TableFrame{
		Headers: []string{"NAME", "STATUS"},
		Rows:    [][]string{{strings.Repeat("a", 80), "Running"}},
	}, 20)
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if w := ui.VisualLen(line); w > 20 {
			t.Errorf("line width %d > 20: %q", w, line)
		}
	}
	if got := strings.Count(buf.String(), "\n"); got != n {
		t.Errorf("emitted %d but wrote %d lines", n, got)
	}
}

func TestPrintGetFrame(t *testing.T) {
	var buf bytes.Buffer
	lines := printGetFrame(&buf, kube.TableFrame{
		Headers: []string{"NAME", "STATUS"},
		Rows: [][]string{
			{"coredns-1", "Running"},
			{"coredns-2", "Pending"},
		},
	}, 0)
	if lines != 3 {
		t.Errorf("lines = %d, want 3 (1 header + 2 rows)", lines)
	}
	out := buf.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "coredns-2") {
		t.Errorf("output missing expected content:\n%s", out)
	}
}

func TestPrintGetFrameEmpty(t *testing.T) {
	var buf bytes.Buffer
	if got := printGetFrame(&buf, kube.TableFrame{}, 0); got != 1 {
		t.Errorf("empty frame should print one informational line, got %d", got)
	}
}

func TestPrintGetFrameCompletedPodReadyNotRed(t *testing.T) {
	// Mirrors TestRenderGetTableCompletedPodReadyNotRed but for the live
	// watch path (printGetFrame). A finished job pod reports READY=0/1 with
	// STATUS=Completed; that READY cell shouldn't render in alarm-red.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	printGetFrame(&buf, kube.TableFrame{
		Headers: []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE"},
		Rows: [][]string{
			{"job-1", "0/1", "Completed", "0", "5m"},
			{"job-2", "0/1", "Pending", "0", "5m"},
		},
	}, 0)
	out := buf.String()

	dangerReady := ui.Render(ui.Danger, "0/1")
	subtleReady := ui.Render(ui.Subtle, "0/1")

	completedLine := findLineContaining(out, "Completed")
	pendingLine := findLineContaining(out, "Pending")
	if completedLine == "" || pendingLine == "" {
		t.Fatalf("missing one of the rows in output:\n%s", out)
	}
	if strings.Contains(completedLine, dangerReady) {
		t.Errorf("Completed pod READY=0/1 must NOT use Danger style; line:\n%q", completedLine)
	}
	if !strings.Contains(completedLine, subtleReady) {
		t.Errorf("Completed pod READY=0/1 should use Subtle style; got line:\n%q", completedLine)
	}
	if !strings.Contains(pendingLine, dangerReady) {
		t.Errorf("Pending pod READY=0/1 should still be Danger-styled; got line:\n%q", pendingLine)
	}
}

func TestPrintGetFrameCountsFooterInEmittedLines(t *testing.T) {
	// runGetWatch's redraw uses the emitted-line count to do an ANSI
	// cursor-up of exactly that many rows. If the footer is printed but
	// not counted, every refresh leaves the footer as orphaned text
	// scrolling above the table. Pin the contract here.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	emitted := printGetFrame(&buf, kube.TableFrame{
		Headers: []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE"},
		Rows: [][]string{
			{"web-1", "1/1", "Running", "0", "5d"},
			{"web-2", "0/1", "Pending", "0", "5d"},
		},
	}, 0)

	gotLines := strings.Count(buf.String(), "\n")
	if emitted != gotLines {
		t.Errorf("emitted=%d but wrote %d lines — cursor-up redraw would drift", emitted, gotLines)
	}
	if !strings.Contains(buf.String(), "2 pods") {
		t.Errorf("expected summary footer in watch frame, got:\n%s", buf.String())
	}
}
