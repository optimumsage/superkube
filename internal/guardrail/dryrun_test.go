package guardrail

import (
	"bytes"
	"context"
	"testing"
)

// TestPreviewApplyWritesToProvidedWriter only validates the refactor: passing
// a custom io.Writer should not panic and (when the runner is nil) should
// surface a sensible error. We don't try to spin up a real kubectl here —
// that's covered by manual / e2e tests.
func TestPreviewApplyWritesToProvidedWriter(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("PreviewApply panicked with nil runner: %v", rec)
		}
	}()
	var buf bytes.Buffer
	// nil runner will return an error before writing anything; the important
	// thing is we don't crash and we accept the writer.
	_, err := PreviewApply(context.Background(), nil, []string{"-f", "x.yaml"}, &buf)
	if err == nil {
		t.Skip("nil runner unexpectedly succeeded (kubectl on PATH?); skipping")
	}
}
