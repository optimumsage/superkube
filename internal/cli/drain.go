package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newDrainCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "drain NODE",
		Short:              "Drain a node after typed-name confirmation",
		DisableFlagParsing: true,
		RunE:               runDrain,
	}
}

func runDrain(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("drain: NODE name required")
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if err := guardOp("drain", args); err != nil {
		return err
	}

	positionals := positionalArgs(kubectlArgs)
	if len(positionals) == 0 {
		return errors.New("drain: NODE name required")
	}
	node := positionals[0]
	ReshowBanner()
	if err := guardrail.TypedName(
		fmt.Sprintf("drain node %s (this will evict its pods)", node),
		node,
		yes,
	); err != nil {
		return err
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"drain"}, kubectlArgs...), kubectl.RunOpts{})
}

func newCordonCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "cordon NODE",
		Short:              "Mark a node unschedulable after confirmation",
		DisableFlagParsing: true,
		RunE:               runCordon,
	}
}

func runCordon(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("cordon: NODE name required")
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if err := guardOp("cordon", args); err != nil {
		return err
	}

	positionals := positionalArgs(kubectlArgs)
	if len(positionals) == 0 {
		return errors.New("cordon: NODE name required")
	}
	node := positionals[0]
	ReshowBanner()
	if err := confirmYesNo(
		fmt.Sprintf("Cordon node %s? (new pods will not be scheduled here)", node),
		"Use `sk uncordon` to reverse. Existing pods are unaffected.",
		"CORDON",
		yes,
	); err != nil {
		return err
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"cordon"}, kubectlArgs...), kubectl.RunOpts{})
}
