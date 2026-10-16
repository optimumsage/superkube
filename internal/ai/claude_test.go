package ai

import (
	"strings"
	"testing"
)

func TestClaudeArgs_NoToolsByDefault(t *testing.T) {
	args := claudeArgs(RunOpts{})
	if len(args) != 1 || args[0] != "-p" {
		t.Fatalf("args = %v, want just -p", args)
	}
}

func TestClaudeArgs_ToolModeDeniesSecretsAndConfig(t *testing.T) {
	args := claudeArgs(RunOpts{AllowReadOnlyKubectl: true})
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--allowedTools") || !strings.Contains(joined, "--disallowedTools") {
		t.Fatalf("expected both allow and deny flags: %v", args)
	}
	for _, tool := range claudeReadOnlyTools {
		if strings.Contains(tool, "config view") {
			t.Errorf("allowlist must not include %q (--raw dumps credentials)", tool)
		}
		if strings.Contains(tool, "sk config") {
			t.Errorf("allowlist entry %q targets superkube's own config command", tool)
		}
	}
	denied := strings.Join(claudeDeniedTools, " ")
	for _, want := range []string{
		"Bash(kubectl get *secret*)",
		"Bash(kubectl describe *secret*)",
		"Bash(kubectl config view*)",
		"Bash(sk get *secret*)",
		"Bash(sk describe *secret*)",
	} {
		if !strings.Contains(denied, want) {
			t.Errorf("deny list missing %q", want)
		}
	}
}
