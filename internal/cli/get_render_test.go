package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/optimumsage/superkube/internal/ui"
)

// TestMain forces lipgloss into a colored profile so styled-path assertions
// produce ANSI under `go test`.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	os.Exit(m.Run())
}

func TestDetectColumns(t *testing.T) {
	header := "NAME              READY   STATUS    RESTARTS   AGE"
	spans := detectColumns(header)
	if len(spans) != 5 {
		t.Fatalf("expected 5 columns, got %d", len(spans))
	}
	want := []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE"}
	for i, n := range want {
		if spans[i].name != n {
			t.Errorf("span %d name = %q, want %q", i, spans[i].name, n)
		}
	}
	if spans[0].start != 0 {
		t.Errorf("first span should start at 0, got %d", spans[0].start)
	}
	if spans[len(spans)-1].end != -1 {
		t.Errorf("last span end should be sentinel -1, got %d", spans[len(spans)-1].end)
	}
}

func TestDetectColumnsMultiWord(t *testing.T) {
	// `kubectl get events` uses 1-space-inside, 2+-spaces-between.
	header := "LAST SEEN   TYPE      REASON              OBJECT             MESSAGE"
	spans := detectColumns(header)
	if len(spans) != 5 {
		t.Fatalf("expected 5 columns, got %d (%v)", len(spans), spans)
	}
	if spans[0].name != "LAST SEEN" {
		t.Errorf("first column should be LAST SEEN, got %q", spans[0].name)
	}
	if spans[1].name != "TYPE" {
		t.Errorf("second column should be TYPE, got %q", spans[1].name)
	}
}

func TestInferKind(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		want    string
	}{
		{"pods", []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE", "IP", "NODE"}, "pod"},
		{"deployments", []string{"NAME", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}, "deployment"},
		{"replicasets", []string{"NAME", "DESIRED", "CURRENT", "READY", "AGE"}, "replicaset"},
		{"nodes", []string{"NAME", "STATUS", "ROLES", "AGE", "VERSION"}, "node"},
		{"services", []string{"NAME", "TYPE", "CLUSTER-IP", "EXTERNAL-IP", "PORT(S)", "AGE"}, "service"},
		{"events", []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"}, "event"},
		{"ingresses", []string{"NAME", "CLASS", "HOSTS", "ADDRESS", "PORTS", "AGE"}, "ingress"},
		{"unknown", []string{"NAME", "AGE"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferKind(tc.headers); got != tc.want {
				t.Errorf("inferKind(%v) = %q, want %q", tc.headers, got, tc.want)
			}
		})
	}
}

func TestPaintRowPreservesColumnAlignment(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	header := "NAME              READY   STATUS    RESTARTS   AGE"
	row := "nginx-abc-12345   1/1     Running   0          5d"
	spans := detectColumns(header)
	kind := inferKind([]string{"NAME", "READY", "STATUS", "RESTARTS", "AGE"})
	painters := make([]cellColorizer, len(spans))
	for i, s := range spans {
		painters[i] = colorizerFor(s.name, kind)
	}

	out := paintRow(row, spans, painters)

	// Strip ANSI and compare with original — must be byte-identical, ANSI
	// escapes are zero-width and we preserve trailing padding verbatim.
	if stripped := stripANSI(out); stripped != row {
		t.Errorf("paintRow stripped output differs:\n  got:  %q\n  want: %q", stripped, row)
	}
	// Sanity: at least one ANSI escape made it in (STATUS, READY, RESTARTS
	// or AGE should have been painted).
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected at least one ANSI escape, got %q", out)
	}
}

func TestRenderGetTablePodFooter(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	// Header column widths chosen so the widest data cell fits — mirrors how
	// `kubectl get` sizes columns based on the widest row, with 2-space
	// separators between columns.
	raw := "NAME    READY   STATUS             RESTARTS     AGE   IP         NODE\n" +
		"web-1   1/1     Running            0            5d    10.0.0.1   n1\n" +
		"web-2   0/1     CrashLoopBackOff   3 (5m ago)   5d    10.0.0.2   n1\n" +
		"web-3   1/1     Running            0            5d    10.0.0.3   n2\n"
	var buf bytes.Buffer
	renderGetTable(&buf, raw)

	out := buf.String()
	if !strings.Contains(out, "3 pods") {
		t.Errorf("expected footer to mention `3 pods`, got:\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "CrashLoopBackOff") {
		t.Errorf("expected CrashLoopBackOff in footer breakdown, got:\n%s", out)
	}
	if !strings.Contains(stripANSI(out), "2 Running") {
		t.Errorf("expected `2 Running` in footer breakdown, got:\n%s", out)
	}
}

func TestRenderGetTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderGetTable(&buf, "")
	if buf.Len() != 0 {
		t.Errorf("empty input should produce no output, got %q", buf.String())
	}
}

func TestRenderGetTableUnknownKindNoFooter(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	raw := "NAME    AGE\nfoo     5d\nbar     1h\n"
	var buf bytes.Buffer
	renderGetTable(&buf, raw)
	out := buf.String()
	if strings.Contains(out, " · ") {
		t.Errorf("unknown kind should not emit a summary footer, got:\n%s", out)
	}
}

func TestLooksLikeGetHeader(t *testing.T) {
	good := []string{
		"NAME              READY   STATUS",
		"NAME    TYPE   CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE",
		"NAMESPACE   NAME",
	}
	for _, s := range good {
		if !looksLikeGetHeader(s) {
			t.Errorf("looksLikeGetHeader(%q) = false, want true", s)
		}
	}
	bad := []string{
		"",
		"  indented",
		"web-1   1/1   Running   0   5d", // lowercase data row
		"pod/web-1   1/1   Running",      // contains '/'
		"Containers:",                    // describe section, not get
		"error: the server could not find anything",
	}
	for _, s := range bad {
		if looksLikeGetHeader(s) {
			t.Errorf("looksLikeGetHeader(%q) = true, want false", s)
		}
	}
}

func TestRenderGetTableMultiKind(t *testing.T) {
	// `kubectl get pods,svc` emits two sub-tables separated by a blank line.
	// Regression: the original implementation only detected the first
	// header and tried to slice the services rows by pod column offsets,
	// producing garbage. After the fix, both headers are colored as bands,
	// each sub-table's rows are painted with their own spans, and each gets
	// its own summary footer.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	raw := "" +
		"NAME    READY   STATUS    RESTARTS   AGE\n" +
		"web-1   1/1     Running   0          5d\n" +
		"web-2   0/1     Pending   0          5d\n" +
		"\n" +
		"NAME      TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE\n" +
		"web       ClusterIP   10.0.0.1     <none>        80/TCP    5d\n"

	var buf bytes.Buffer
	renderGetTable(&buf, raw)
	out := buf.String()
	stripped := stripANSI(out)

	// Every input line must still appear in the stripped output. The styled
	// summary footer (`2 pods · 1 Running · 1 Pending`) is an intentional
	// addition between the two sub-tables, so we don't byte-equal the whole
	// stream.
	for _, want := range []string{
		"NAME    READY   STATUS    RESTARTS   AGE",
		"web-1   1/1     Running   0          5d",
		"web-2   0/1     Pending   0          5d",
		"NAME      TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE",
		"web       ClusterIP   10.0.0.1     <none>        80/TCP    5d",
	} {
		if !strings.Contains(stripped, want) {
			t.Errorf("output missing line %q:\n%s", want, stripped)
		}
	}

	// Both headers must be styled as bands. Proxy: each header line is
	// preceded by an ANSI introducer.
	for _, header := range []string{"NAME    READY", "NAME      TYPE"} {
		idx := strings.Index(out, header)
		if idx < 0 {
			t.Fatalf("output missing header %q", header)
		}
		if !strings.Contains(out[:idx], "\x1b[") {
			t.Errorf("header %q not styled — no ANSI before it", header)
		}
	}

	// Pods footer present, and it appears BEFORE the services header (so
	// the summary attaches to the right sub-table).
	footerIdx := strings.Index(stripped, "2 pods")
	if footerIdx < 0 {
		t.Errorf("expected `2 pods` summary footer, got:\n%s", stripped)
	}
	svcHeaderIdx := strings.Index(stripped, "NAME      TYPE")
	if footerIdx >= 0 && svcHeaderIdx >= 0 && footerIdx >= svcHeaderIdx {
		t.Errorf("pods footer must come before services header:\n%s", stripped)
	}
}

// stripANSI removes any ANSI CSI escape sequence (the form ESC[...m used by
// lipgloss). Sufficient for byte-equality checks in these tests.
func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
