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

// claudeReadOnlyTools enumerates the Bash patterns we allow Claude to invoke
// when the caller passes RunOpts.AllowReadOnlyKubectl. The list is
// intentionally conservative: only verbs that cannot mutate cluster state or
// open interactive sessions. Patterns are space-separated and exact; Claude's
// `Bash(prefix:*)` form matches any argv whose first token starts with prefix.
var claudeReadOnlyTools = []string{
	"Bash(kubectl get:*)",
	"Bash(kubectl describe:*)",
	"Bash(kubectl logs:*)",
	"Bash(kubectl events:*)",
	"Bash(kubectl top:*)",
	"Bash(kubectl explain:*)",
	"Bash(kubectl api-resources:*)",
	"Bash(kubectl api-versions:*)",
	"Bash(kubectl version:*)",
	"Bash(kubectl cluster-info:*)",
	"Bash(kubectl auth can-i:*)",
	"Bash(kubectl config get-contexts:*)",
	"Bash(kubectl config current-context:*)",
	"Bash(kubectl config view:*)",
	"Bash(sk get:*)",
	"Bash(sk describe:*)",
	"Bash(sk logs:*)",
	"Bash(sk events:*)",
	"Bash(sk top:*)",
	"Bash(sk explain:*)",
	"Bash(sk api-resources:*)",
	"Bash(sk api-versions:*)",
	"Bash(sk version:*)",
	"Bash(sk cluster-info:*)",
	"Bash(sk auth can-i:*)",
	"Bash(sk config get-contexts:*)",
	"Bash(sk config current-context:*)",
	"Bash(sk config view:*)",
}

// Run streams Claude's output to w. We pass the prompt on stdin to avoid
// argv-length pitfalls (long diagnose payloads include logs + describe
// output). We do NOT use --bare because that bypasses OAuth login: the
// typical user runs `claude` interactively to authenticate once and then
// expects subsequent invocations to inherit that session.
//
// When opts.AllowReadOnlyKubectl is set we pass --allowedTools with the
// claudeReadOnlyTools allowlist so Claude can execute (only) the listed
// read-only kubectl/sk verbs without an interactive permission prompt.
func (claudeProvider) Run(ctx context.Context, prompt string, w io.Writer, opts RunOpts) error {
	if w == nil {
		return errors.New("claude.Run: nil writer")
	}
	args := []string{"-p"}
	if opts.AllowReadOnlyKubectl {
		args = append(args, "--allowedTools", strings.Join(claudeReadOnlyTools, " "))
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
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
