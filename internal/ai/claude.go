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
// open interactive sessions. Patterns use Claude's `Bash(prefix:*)` form.
//
// `kubectl config view` is deliberately absent (`--raw` dumps credentials), and
// so is any `sk config ...` entry: `sk config` is superkube's own config
// command (path/init), not a kubectl passthrough.
//
// SECURITY NOTE: in tool mode the model runs these commands itself and reads
// their output directly. That output never passes through ai.Redact, which only
// sees the prompt sk assembles. The allowlist is therefore paired with
// claudeDeniedTools to keep Secret contents and kubeconfig credentials out of
// reach; pattern matching is best-effort, not a security boundary.
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
}

// claudeDeniedTools are passed as --disallowedTools; deny rules take
// precedence over allow rules in Claude Code. Wildcards match anywhere in the
// command, for both the kubectl and sk spellings, so `kubectl get -n x secrets
// -o yaml`, `get secret/foo` and `get secrets,cm` are all caught.
//
// Matching is case-sensitive but kubectl lowercases resource names, so
// `get SECRETS` / `SeCrEt` work. We therefore emit a rule for every case
// variant of "ecret" (32), which covers any casing of secret/secrets/
// secrets.v1 regardless of the leading S. The scope is deliberately all
// verbs: a harmless command that merely contains "secret" (a pod named
// `secret-rotator`) is blocked too, an accepted tradeoff.
var claudeDeniedTools = buildClaudeDenied()

func buildClaudeDenied() []string {
	var out []string
	for _, bin := range []string{"kubectl", "sk"} {
		for mask := 0; mask < 1<<5; mask++ {
			v := []byte("ecret")
			for i := range v {
				if mask&(1<<i) != 0 {
					v[i] -= 'a' - 'A'
				}
			}
			out = append(out, "Bash("+bin+" *"+string(v)+"*)")
		}
		for _, verb := range []string{"get", "describe"} {
			out = append(out,
				"Bash("+bin+" "+verb+" --raw*)",
				"Bash("+bin+" "+verb+" * --raw*)",
			)
		}
		out = append(out,
			"Bash("+bin+" config view*)",
			"Bash("+bin+" * config view*)",
		)
	}
	return out
}

// claudeArgs builds the `claude` argv. Tool access is opt-in.
func claudeArgs(opts RunOpts) []string {
	args := []string{"-p"}
	if opts.AllowReadOnlyKubectl {
		args = append(args,
			"--allowedTools", strings.Join(claudeReadOnlyTools, " "),
			"--disallowedTools", strings.Join(claudeDeniedTools, " "),
		)
	}
	return args
}

// Run streams Claude's output to w. We pass the prompt on stdin to avoid
// argv-length pitfalls (long diagnose payloads include logs + describe
// output). We do NOT use --bare because that bypasses OAuth login: the
// typical user runs `claude` interactively to authenticate once and then
// expects subsequent invocations to inherit that session.
//
// When opts.AllowReadOnlyKubectl is set we pass --allowedTools with the
// claudeReadOnlyTools allowlist (and --disallowedTools with claudeDeniedTools) so Claude can execute (only) the listed
// read-only kubectl/sk verbs without an interactive permission prompt.
func (claudeProvider) Run(ctx context.Context, prompt string, w io.Writer, opts RunOpts) error {
	if w == nil {
		return errors.New("claude.Run: nil writer")
	}
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(opts)...)
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
