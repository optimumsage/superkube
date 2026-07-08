package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kube"
)

func newAICmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ai",
		Short: "AI-assisted commands (explain, etc.)",
	}
	c.AddCommand(newAIExplainCmd())
	return c
}

func newAIExplainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain QUESTION",
		Short: "Ask the local AI provider a Kubernetes question",
		Long: `Sends QUESTION to the local AI provider (claude preferred, antigravity
fallback) and streams the response. The current kubectl context and namespace
are attached as light context unless --no-context is set.

Pass --tools to let the provider run read-only kubectl/sk commands to answer
(claude enforces the read-only allowlist; antigravity is best-effort via the
prompt). --ai-timeout overrides how long to wait for a response.

This command never sends data to a remote service of our own — the provider
binary runs on your machine under your account.`,
		Args: cobra.MinimumNArgs(1),
		RunE: runAIExplain,
	}
}

func runAIExplain(cmd *cobra.Command, args []string) error {
	question := strings.Join(args, " ")
	if strings.TrimSpace(question) == "" {
		return errors.New("ai explain: question is required")
	}

	provider, err := ai.Detect(Flags.AIProvider)
	if err != nil {
		return err
	}
	recordAIProvider(provider.Name())

	inputs := ai.PromptInputs{Question: question, ToolsAllowed: Flags.Tools}
	if !Flags.NoContext {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
		inputs.Context, inputs.Namespace, _ = loader.CurrentContextAndNamespace()
	}

	prompt, err := ai.Render("explain", inputs)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), resolveAITimeout(Flags.Tools))
	defer cancel()

	return streamAIResponse(ctx, provider, prompt, ai.RunOpts{AllowReadOnlyKubectl: Flags.Tools})
}
