package guardrail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/ui"
)

// DryRunResult is what the caller needs from a dry-run preview: did the
// command produce any actual change? Printed indicates the diff has already
// been rendered to stderr.
type DryRunResult struct {
	HasChanges bool
	Printed    bool
}

// PreviewApply shells out to `kubectl diff <args>` to compute a server-side
// dry-run diff and renders it (colored) to the provided writer. kubectl's
// exit codes here are well-defined:
//
//	0  no differences
//	1  differences detected (this is normal, not an error)
//	>1 something went wrong (auth, parse, server unavailable, etc.)
//
// We translate that to a structured result so the caller can decide whether
// to skip the apply (no changes) or prompt for confirmation (changes found).
//
// kubectlArgs should be the same set of args the user passed to `apply` —
// e.g. ["-f", "deploy.yaml", "-n", "default"]. Caller is responsible for
// stripping --dry-run flags from the original args before calling.
//
// out is the writer the colored diff is rendered to (CLI passes os.Stderr;
// web handler passes a bytes.Buffer). A nil writer is treated as io.Discard
// so callers that only care about HasChanges don't have to materialize one.
func PreviewApply(ctx context.Context, runner *kubectl.Runner, kubectlArgs []string, out io.Writer) (DryRunResult, error) {
	if out == nil {
		out = io.Discard
	}
	if runner == nil {
		return DryRunResult{}, fmt.Errorf("kubectl runner is nil")
	}
	args := append([]string{"diff"}, kubectlArgs...)
	var stdout, stderr bytes.Buffer
	err := runner.Run(ctx, args, kubectl.RunOpts{
		Stdin:  os.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	})

	var ee *kubectl.ExitCodeError
	switch {
	case err == nil:
		// Exit 0: no differences.
		fmt.Fprintln(out, ui.Render(ui.Subtle, "no differences (cluster matches manifest)"))
		return DryRunResult{HasChanges: false, Printed: true}, nil
	case errors.As(err, &ee) && ee.Code == 1:
		// Exit 1: differences. Render colored diff to the caller's writer.
		if err := renderKubectlDiff(out, stdout.String()); err != nil {
			return DryRunResult{}, err
		}
		return DryRunResult{HasChanges: true, Printed: true}, nil
	default:
		// Something else went wrong. Surface stderr so the user can act.
		if stderr.Len() > 0 {
			io.Copy(out, &stderr)
		}
		return DryRunResult{}, fmt.Errorf("kubectl diff failed: %w", err)
	}
}

// renderKubectlDiff colorizes kubectl's already-unified-format diff output.
// We don't recompute the diff — kubectl already produced the canonical one
// against the live state; we just paint it.
func renderKubectlDiff(w io.Writer, raw string) error {
	if !ui.Styled() {
		_, err := fmt.Fprint(w, raw)
		return err
	}
	for _, line := range splitKeepEOL(raw) {
		switch {
		case len(line) == 0:
			continue
		case startsWith(line, "diff "):
			fmt.Fprint(w, ui.Render(ui.Title, line))
		case startsWith(line, "---") || startsWith(line, "+++"):
			fmt.Fprint(w, ui.Render(ui.Subtle, line))
		case startsWith(line, "@@"):
			fmt.Fprint(w, ui.Render(ui.Info, line))
		case line[0] == '+':
			fmt.Fprint(w, ui.Render(ui.Success, line))
		case line[0] == '-':
			fmt.Fprint(w, ui.Render(ui.Danger, line))
		default:
			fmt.Fprint(w, line)
		}
	}
	return nil
}

func splitKeepEOL(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
