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

// claudeProvider shells out to Claude Code's `claude -p` non-interactive mode.
type claudeProvider struct{}

func (claudeProvider) Name() string { return "claude" }

func (claudeProvider) Available(_ context.Context) bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (claudeProvider) VersionString(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "claude", "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return "claude " + strings.TrimSpace(string(out))
}

// Run streams Claude's output to w. We pass the prompt on stdin to avoid
// argv-length pitfalls (long diagnose payloads include logs + describe
// output). We do NOT use --bare because that bypasses OAuth login: the
// typical user runs `claude` interactively to authenticate once and then
// expects subsequent invocations to inherit that session.
func (claudeProvider) Run(ctx context.Context, prompt string, w io.Writer) error {
	if w == nil {
		return errors.New("claude.Run: nil writer")
	}
	cmd := exec.CommandContext(ctx, "claude", "-p")
	cmd.Stdin = bytes.NewBufferString(prompt)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("claude: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("claude: %w", err)
	}
	return nil
}
