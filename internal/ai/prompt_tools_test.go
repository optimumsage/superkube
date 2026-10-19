package ai

import (
	"strings"
	"testing"
)

// In tool mode the prompts must not invite the model to read kubeconfig or
// Secrets; Antigravity relies on prompt constraints alone.
func TestPrompts_ToolModeForbidsSecretsAndConfigView(t *testing.T) {
	for name := range templates {
		out, err := Render(name, PromptInputs{ToolsAllowed: true, Question: "q"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out, "config view") {
			t.Errorf("%s: prompt still mentions config view", name)
		}
		if strings.Contains(out, "run read-only kubectl") && !strings.Contains(out, "Secrets") {
			t.Errorf("%s: tool-mode prompt must say Secrets/kubeconfig must not be read", name)
		}
	}
}
