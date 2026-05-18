package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/ui"
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
		Long: `Sends QUESTION to the local AI provider (claude preferred, gemini fallback)
and streams the response. The current kubectl context and namespace are
attached as light context unless --no-context is set.

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

	inputs := ai.PromptInputs{Question: question}
	if !Flags.NoContext {
		loader := kube.Loader{KubeconfigPath: Flags.Kubeconfig, Context: Flags.Context}
		inputs.Context, _ = loader.CurrentContext()
		inputs.Namespace, _ = loader.CurrentNamespace()
	}

	prompt, err := ai.Render("explain", inputs)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
	defer cancel()

	w, stopSpinner := ui.SpinUntilFirstByte("asking "+provider.Name()+"…", os.Stdout)
	defer stopSpinner()
	if err := provider.Run(ctx, prompt, w, ai.RunOpts{}); err != nil {
		return fmt.Errorf("%s: %w", provider.Name(), err)
	}
	fmt.Fprintln(os.Stdout)
	return nil
}
