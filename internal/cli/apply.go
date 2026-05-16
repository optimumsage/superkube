package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "apply [-f FILENAME]...",
		Short:              "Apply manifests after previewing the server-side diff",
		Long:               applyLongHelp,
		DisableFlagParsing: true,
		RunE:               runApply,
	}
}

func runApply(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}

	// Honor --dry-run=none to skip the preview entirely.
	if Flags.DryRun != "none" {
		preview, err := guardrail.PreviewApply(cmd.Context(), runner, kubectlArgs)
		if err != nil {
			return err
		}
		if !preview.HasChanges {
			return nil
		}
		if err := confirmApply(cmd.Context(), yes); err != nil {
			if errors.Is(err, guardrail.ErrAborted) {
				return nil
			}
			return err
		}
	}

	return runner.Run(cmd.Context(), append([]string{"apply"}, kubectlArgs...), kubectl.RunOpts{})
}

func confirmApply(_ context.Context, yes bool) error {
	return guardrail.YesNo(
		"Apply the changes shown above?",
		"You'll see the result from kubectl after confirmation.",
		yes,
	)
}

const applyLongHelp = `Apply manifests with a server-side dry-run preview.

Workflow:
  1. Run kubectl diff to compute the change set vs. live state.
  2. Render the colored diff to your terminal.
  3. Ask for confirmation (Yes / Cancel) — skipped with --yes.
  4. Run the real apply on confirmation.

Notes:
  * If the manifest matches the cluster, no apply happens at all.
  * Pass --dry-run=none to skip the preview and behave like plain kubectl apply.
  * Flags after the verb are passed to kubectl verbatim (-f, -k, --prune, etc).

Example:
  sk apply -f deployment.yaml
  sk apply -k overlays/staging -n web
  sk --yes apply -f -                 # apply stdin without confirming`
