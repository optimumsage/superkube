package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
)

func TestGetResourceArg(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"basic", []string{"pods", "-w"}, "pods"},
		{"watch first", []string{"-w", "pods"}, "pods"},
		{"namespace flag eats value", []string{"-n", "kube-system", "pods", "-w"}, "pods"},
		{"namespace before resource", []string{"-n", "kube-system", "deploy", "-w", "--selector", "app=web"}, "deploy"},
		{"long ns equals form", []string{"--namespace=kube-system", "svc", "-w"}, "svc"},
		{"no positional", []string{"-w"}, ""},
		{"selector eats value", []string{"-l", "app=web", "pods"}, "pods"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getResourceArg(tc.args); got != tc.want {
				t.Errorf("getResourceArg(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
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
	})
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
	if got := printGetFrame(&buf, kube.TableFrame{}); got != 1 {
		t.Errorf("empty frame should print one informational line, got %d", got)
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
	})

	gotLines := strings.Count(buf.String(), "\n")
	if emitted != gotLines {
		t.Errorf("emitted=%d but wrote %d lines — cursor-up redraw would drift", emitted, gotLines)
	}
	if !strings.Contains(buf.String(), "2 pods") {
		t.Errorf("expected summary footer in watch frame, got:\n%s", buf.String())
	}
}
