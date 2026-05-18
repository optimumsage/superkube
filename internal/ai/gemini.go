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

// geminiProvider shells out to the Gemini CLI's `-p` (headless) mode.
type geminiProvider struct{}

func (geminiProvider) Name() string { return "gemini" }

func (geminiProvider) Available(_ context.Context) bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

func (geminiProvider) VersionString(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "gemini", "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return "gemini " + strings.TrimSpace(string(out))
}

// Run streams Gemini's output to w. Gemini's headless mode requires a -p flag
// value but also appends stdin to it; we pass the prompt on stdin and an
// empty -p value, mirroring what we do for claude. --yolo skips per-tool
// approval prompts which would block in a non-TTY context.
//
// opts.AllowReadOnlyKubectl is honored on a best-effort basis: gemini's
// permission model is all-or-nothing in headless mode, so the prompt itself
// (templates in prompt.go) is what constrains the model to read-only verbs.
func (geminiProvider) Run(ctx context.Context, prompt string, w io.Writer, _ RunOpts) error {
	if w == nil {
		return errors.New("gemini.Run: nil writer")
	}
	cmd := exec.CommandContext(ctx, "gemini", "-p", "")
	cmd.Stdin = bytes.NewBufferString(prompt)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("gemini: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("gemini: %w", err)
	}
	return nil
}
