package ai

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRenderToolsFraming(t *testing.T) {
	const readOnlyMarker = "read-only kubectl"

	for _, name := range []string{"explain", "diagnose", "why"} {
		t.Run(name+"/tools-on", func(t *testing.T) {
			out, err := Render(name, PromptInputs{Resource: "pod/x", Question: "q", ToolsAllowed: true})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(out, readOnlyMarker) {
				t.Fatalf("ToolsAllowed=true should mention %q in %s template; got:\n%s", readOnlyMarker, name, out)
			}
		})
		t.Run(name+"/tools-off", func(t *testing.T) {
			out, err := Render(name, PromptInputs{Resource: "pod/x", Question: "q", ToolsAllowed: false})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if name != "explain" && strings.Contains(out, readOnlyMarker) {
				// explain always references read-only tools in one branch or the
				// other; diagnose/why should only mention them when opted in.
				t.Fatalf("ToolsAllowed=false leaked tool framing into %s template:\n%s", name, out)
			}
		})
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	if _, err := Render("does-not-exist", PromptInputs{}); err == nil {
		t.Fatal("expected error for unknown template")
	}
}

// TestFakeProviderRoundTrip exercises the shared test double so its behavior
// (recording prompt/opts, canned output, error injection) is itself covered.
func TestFakeProviderRoundTrip(t *testing.T) {
	fp := &fakeProvider{
		name:      "fake",
		available: true,
		version:   "fake 1.0",
		response:  "hello",
	}
	if fp.Name() != "fake" || !fp.Available(context.Background()) || fp.VersionString(context.Background()) != "fake 1.0" {
		t.Fatal("fakeProvider metadata mismatch")
	}
	var out bytes.Buffer
	if err := fp.Run(context.Background(), "the prompt", &out, RunOpts{AllowReadOnlyKubectl: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "hello" || fp.lastPrompt != "the prompt" || !fp.lastOpts.AllowReadOnlyKubectl {
		t.Fatalf("fakeProvider did not record inputs: %+v out=%q", fp, out.String())
	}

	fp.runErr = errors.New("boom")
	if err := fp.Run(context.Background(), "x", &out, RunOpts{}); err == nil {
		t.Fatal("expected injected error")
	}
}
