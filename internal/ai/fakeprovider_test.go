package ai

import (
	"context"
	"io"
)

// fakeProvider is a test double implementing Provider. It records the prompt
// and RunOpts it was handed and writes a canned response, so tests can exercise
// the Provider contract without spawning a real CLI. The Provider interface has
// always been trivially fakeable; this is the shared seam for it.
type fakeProvider struct {
	name       string
	available  bool
	version    string
	response   string
	lastPrompt string
	lastOpts   RunOpts
	runErr     error
}

func (f *fakeProvider) Name() string                         { return f.name }
func (f *fakeProvider) Available(context.Context) bool       { return f.available }
func (f *fakeProvider) VersionString(context.Context) string { return f.version }

func (f *fakeProvider) Run(_ context.Context, prompt string, out io.Writer, opts RunOpts) error {
	f.lastPrompt = prompt
	f.lastOpts = opts
	if f.runErr != nil {
		return f.runErr
	}
	_, err := io.WriteString(out, f.response)
	return err
}
