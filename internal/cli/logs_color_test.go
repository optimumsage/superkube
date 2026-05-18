package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/optimumsage/superkube/internal/ui"
)

// failingWriter is an io.Writer that returns the configured error on every
// call. Used to verify lineColorizer's sticky-error propagation.
type failingWriter struct{ err error }

func (f *failingWriter) Write(p []byte) (int, error) { return 0, f.err }

func TestLineColorizerFullLines(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	lc := newLineColorizer(&buf)
	in := "INFO hello\nERROR something failed\n"
	n, err := lc.Write([]byte(in))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if n != len(in) {
		t.Errorf("Write returned %d, want %d", n, len(in))
	}
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI in colorized output, got %q", out)
	}
	// stripANSI is shared via get_render_test.go (same package).
	if got := stripANSI(out); got != in {
		t.Errorf("visible content changed:\n  got:  %q\n  want: %q", got, in)
	}
}

func TestLineColorizerSplitAcrossWrites(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	lc := newLineColorizer(&buf)
	// Split a single logical line ("ERROR boom\n") across three Write calls.
	for _, chunk := range []string{"ERR", "OR bo", "om\n"} {
		if _, err := lc.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
	}
	got := stripANSI(buf.String())
	want := "ERROR boom\n"
	if got != want {
		t.Errorf("reassembled line wrong:\n  got:  %q\n  want: %q", got, want)
	}
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("expected ANSI in colorized output, got %q", buf.String())
	}
}

func TestLineColorizerFlushResidual(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	lc := newLineColorizer(&buf)
	if _, err := lc.Write([]byte("trailing without newline")); err != nil {
		t.Fatalf("write error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected nothing flushed yet, got %q", buf.String())
	}
	lc.Flush()
	if !strings.Contains(buf.String(), "trailing without newline") {
		t.Errorf("Flush() did not emit residue, got %q", buf.String())
	}
}

func TestLineColorizerPropagatesWriteError(t *testing.T) {
	// Regression: in `logs -f` mode, if the user closes the terminal, the
	// downstream write fails. lineColorizer used to swallow the error and
	// keep accepting bytes forever; now it sticks the error so subsequent
	// Writes return (0, err), letting exec.Cmd's stdout pump SIGPIPE
	// kubectl cleanly.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	sentinel := errors.New("downstream closed")
	lc := newLineColorizer(&failingWriter{err: sentinel})

	// First Write contains a newline → flush fires → sentinel returned.
	n, err := lc.Write([]byte("hello\n"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("first Write err = %v, want %v", err, sentinel)
	}
	if n != 0 {
		// We hadn't consumed anything successfully before the failed flush
		// (the buffer was empty). A short write of 0 is correct.
		t.Errorf("first Write n = %d, want 0", n)
	}

	// Subsequent Writes must short-circuit with the same sticky error.
	n2, err2 := lc.Write([]byte("more"))
	if !errors.Is(err2, sentinel) {
		t.Errorf("second Write err = %v, want %v (sticky)", err2, sentinel)
	}
	if n2 != 0 {
		t.Errorf("second Write n = %d, want 0 (sticky)", n2)
	}

	// Flush must also report the sticky error.
	if err := lc.Flush(); !errors.Is(err, sentinel) {
		t.Errorf("Flush err = %v, want %v (sticky)", err, sentinel)
	}
}

func TestLineColorizerFlushReportsError(t *testing.T) {
	// Pure-Flush failure path: nothing fails during Write (no newline =>
	// nothing written), then Flush hits the broken downstream.
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = false
	defer func() { ui.Plain = prev }()

	sentinel := errors.New("downstream closed")
	lc := newLineColorizer(&failingWriter{err: sentinel})
	if _, err := lc.Write([]byte("partial no newline")); err != nil {
		t.Fatalf("buffered Write should not have errored: %v", err)
	}
	if err := lc.Flush(); !errors.Is(err, sentinel) {
		t.Errorf("Flush err = %v, want %v", err, sentinel)
	}
}

func TestLineColorizerPassesThroughWhenNotStyled(t *testing.T) {
	restore := ui.SetStdoutTTYForTest(true)
	defer restore()
	prev := ui.Plain
	ui.Plain = true // colorizer should no-op via ColorizeLogLine
	defer func() { ui.Plain = prev }()

	var buf bytes.Buffer
	lc := newLineColorizer(&buf)
	in := "ERROR boom\nINFO ok\n"
	if _, err := lc.Write([]byte(in)); err != nil {
		t.Fatalf("write error: %v", err)
	}
	if got := buf.String(); got != in {
		t.Errorf("expected verbatim passthrough when Plain=true:\n  got:  %q\n  want: %q", got, in)
	}
}
