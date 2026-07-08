package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// styledMarkdown feeds input through a MarkdownWriter with styling forced on and
// returns the emitted output.
func styledMarkdown(t *testing.T, input string) string {
	t.Helper()
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	if _, err := mw.Write([]byte(input)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := mw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return buf.String()
}

func TestMarkdownStylesAndStripsMarkers(t *testing.T) {
	out := styledMarkdown(t, "# Title\nsome **bold** and `code` here\n")
	if !hasANSI(out) {
		t.Fatal("expected ANSI styling on a TTY")
	}
	plain := stripANSI(out)
	for _, marker := range []string{"# Title", "**bold**", "`code`"} {
		if strings.Contains(plain, marker) {
			t.Fatalf("marker %q should be stripped; got %q", marker, plain)
		}
	}
	for _, kept := range []string{"Title", "bold", "code"} {
		if !strings.Contains(plain, kept) {
			t.Fatalf("content %q should survive; got %q", kept, plain)
		}
	}
}

func TestMarkdownBulletMarker(t *testing.T) {
	out := stripANSI(styledMarkdown(t, "- first\n- second\n"))
	if !strings.Contains(out, "• first") || !strings.Contains(out, "• second") {
		t.Fatalf("dash bullets should become •; got %q", out)
	}
}

func TestMarkdownCodeFenceNotInlineMangled(t *testing.T) {
	// Inside a fence, *stars* and `ticks` must survive verbatim (dimmed, not
	// treated as emphasis).
	out := stripANSI(styledMarkdown(t, "```\nx := *ptr // not *italic*\n```\n"))
	if !strings.Contains(out, "*ptr") || !strings.Contains(out, "*italic*") {
		t.Fatalf("fenced code should be preserved verbatim; got %q", out)
	}
}

func TestMarkdownPlainPassthrough(t *testing.T) {
	restore := SetStdoutTTYForTest(false) // non-TTY → passthrough
	defer restore()
	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	const raw = "# Title\n**bold** stays literal\n"
	if _, err := mw.Write([]byte(raw)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = mw.Flush()
	if buf.String() != raw {
		t.Fatalf("non-TTY should pass through unchanged; got %q", buf.String())
	}
}

func TestMarkdownReassemblesChunkedLines(t *testing.T) {
	restore := SetStdoutTTYForTest(true)
	defer restore()
	prevPlain := Plain
	Plain = false
	defer func() { Plain = prevPlain }()

	var buf bytes.Buffer
	mw := NewMarkdownWriter(&buf)
	// A line split across writes (as model tokens arrive) must render once whole.
	mw.Write([]byte("**bo"))
	mw.Write([]byte("ld**\n"))
	out := stripANSI(buf.String())
	if strings.Contains(out, "*") || !strings.Contains(out, "bold") {
		t.Fatalf("split emphasis should reassemble and strip markers; got %q", out)
	}
}

func TestMarkdownFlushTrailingLineNoNewline(t *testing.T) {
	out := styledMarkdown(t, "final line without newline")
	if strings.HasSuffix(out, "\n") {
		t.Fatalf("Flush must not append a trailing newline; got %q", out)
	}
	if !strings.Contains(stripANSI(out), "final line without newline") {
		t.Fatalf("trailing partial line lost; got %q", out)
	}
}
