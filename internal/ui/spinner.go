package ui

import (
	"io"
	"os"
	"time"

	"github.com/briandowns/spinner"
)

// Spin starts a spinner with the given suffix message and returns a stop
// function. In non-interactive contexts the spinner is a no-op so logs and CI
// output stay clean.
//
// Typical usage:
//
//	stop := ui.Spin("diagnosing pod…")
//	defer stop()
//	// long operation
//
// Or for streaming AI output that should hide the spinner on first byte:
//
//	w, stop := ui.SpinUntilFirstByte("thinking…")
//	defer stop()
//	io.Copy(w, modelOutput)
func Spin(message string) (stop func()) {
	if !Interactive() {
		return func() {}
	}
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(os.Stderr))
	s.Suffix = " " + message
	s.Start()
	return func() { s.Stop() }
}

// SpinUntilFirstByte wraps w with a writer that stops the given spinner once
// the first byte is written. Useful for streaming model output: spinner hides
// the latency to first token, then the response prints directly.
func SpinUntilFirstByte(message string, w io.Writer) (io.Writer, func()) {
	if !Interactive() {
		return w, func() {}
	}
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(os.Stderr))
	s.Suffix = " " + message
	s.Start()
	stop := func() { s.Stop() }
	return &firstByteWriter{w: w, stop: stop}, stop
}

type firstByteWriter struct {
	w     io.Writer
	stop  func()
	dirty bool
}

func (fw *firstByteWriter) Write(p []byte) (int, error) {
	if !fw.dirty && len(p) > 0 {
		fw.stop()
		fw.dirty = true
	}
	return fw.w.Write(p)
}
