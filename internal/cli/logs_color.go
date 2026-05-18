package cli

import (
	"bytes"
	"io"

	"github.com/optimumsage/superkube/internal/ui"
)

// lineColorizer is an io.Writer wrapper that buffers partial lines until a
// newline arrives, then flushes each complete line through ui.ColorizeLogLine
// to the underlying writer. Built for `sk logs` follow-mode, where kubectl
// streams bytes that can split lines across syscalls.
//
// Once the underlying writer returns an error, lineColorizer "sticks" — every
// subsequent Write returns (0, err). That matters for the `logs -f` path: if
// the user closes the terminal, the downstream write fails, and we want the
// error to propagate up through exec.Cmd's stdout pipe so kubectl gets
// SIGPIPE'd and exits cleanly instead of streaming bytes forever into a
// hung writer.
type lineColorizer struct {
	w   io.Writer
	buf []byte
	err error // sticky; once non-nil, further Writes return (0, err)
}

func newLineColorizer(w io.Writer) *lineColorizer {
	return &lineColorizer{w: w, buf: make([]byte, 0, 4096)}
}

func (l *lineColorizer) Write(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	consumed := 0
	rest := p
	for {
		idx := bytes.IndexByte(rest, '\n')
		if idx < 0 {
			// No newline left in this chunk → buffer the residue for the
			// next Write. We count it as consumed because we've taken
			// responsibility for it; the caller doesn't need to re-send.
			l.buf = append(l.buf, rest...)
			consumed += len(rest)
			break
		}
		// Complete line = (previously buffered residue) + rest[:idx]. When
		// the buffer is empty we can borrow rest's bytes directly (no copy);
		// otherwise we have to assemble in l.buf. The borrowed slice is
		// consumed immediately on the same line below, so the alias is safe.
		var line []byte
		if len(l.buf) == 0 {
			line = rest[:idx]
		} else {
			l.buf = append(l.buf, rest[:idx]...)
			line = l.buf
		}
		if _, err := io.WriteString(l.w, ui.ColorizeLogLine(string(line))+"\n"); err != nil {
			// Sticky-fail. Return a short write so io.Copy / exec.Cmd's
			// stdout pump propagates the error up the chain.
			l.err = err
			return consumed, err
		}
		consumed += idx + 1
		l.buf = l.buf[:0]
		rest = rest[idx+1:]
		if len(rest) == 0 {
			break
		}
	}
	return consumed, nil
}

// Flush emits any buffered partial line (no trailing newline appended). Call
// from defer in commands that wrap a stream — kubectl can exit without a
// trailing newline on the last line. The returned error is best-effort; most
// callers defer `func() { _ = lc.Flush() }()` because by the time Flush runs
// the command has already returned and there's nothing useful to do with a
// downstream error.
func (l *lineColorizer) Flush() error {
	if l.err != nil {
		return l.err
	}
	if len(l.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(l.w, ui.ColorizeLogLine(string(l.buf)))
	l.buf = l.buf[:0]
	if err != nil {
		l.err = err
	}
	return err
}
