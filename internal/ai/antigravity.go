package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// antigravityProvider shells out to the Antigravity CLI's `agy -p` (print) mode.
// It replaced the old Gemini provider when the Gemini CLI was discontinued.
type antigravityProvider struct{}

func (antigravityProvider) Name() string { return "antigravity" }

func (antigravityProvider) Available(_ context.Context) bool {
	_, err := exec.LookPath("agy")
	return err == nil
}

func (antigravityProvider) VersionString(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "agy", "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return "antigravity " + strings.TrimSpace(string(out))
}

// Run streams Antigravity's output to w. Unlike the old Gemini provider (which
// needed an empty -p value plus the prompt on stdin), `agy -p "<prompt>"` takes
// the prompt directly as the flag value in its non-interactive print mode, so
// we pass it as an argument. Diagnose payloads (describe + 200 log lines) stay
// well under ARG_MAX (1 MB), so argv length is not a concern; if that ever
// changes, the fallback is `agy -p ""` with the prompt on stdin.
//
// opts.AllowReadOnlyKubectl is honored on a best-effort, prompt-only basis:
// agy has no per-command tool allowlist (unlike claude's --allowedTools), and
// we deliberately do NOT pass --dangerously-skip-permissions because that would
// auto-approve state-mutating tools too, violating superkube's guardrail model.
// The prompt templates (prompt.go) are what constrain the model to read-only
// verbs for this provider.
func (antigravityProvider) Run(ctx context.Context, prompt string, w io.Writer, _ RunOpts) error {
	if w == nil {
		return errors.New("antigravity.Run: nil writer")
	}
	cmd := exec.CommandContext(ctx, "agy", "-p", prompt)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("antigravity: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("antigravity: %w", err)
	}
	return nil
}
