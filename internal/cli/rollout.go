package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newRolloutCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "rollout SUBCOMMAND",
		Short:              "Manage rollouts; `undo` requires a typed confirmation",
		DisableFlagParsing: true,
		RunE:               runRollout,
	}
}

func runRollout(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("rollout: subcommand required (status|history|restart|undo|pause|resume)")
	}
	sub := args[0]
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	if sub == "undo" {
		positionals := positionalArgs(kubectlArgs[1:]) // skip "undo"
		if len(positionals) == 0 {
			return errors.New("rollout undo: workload required (e.g. deployment/foo)")
		}
		if err := guardrail.TypedName(
			fmt.Sprintf("rollout undo %s", positionals[0]),
			positionals[0],
			yes,
		); err != nil {
			return err
		}
	}

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"rollout"}, kubectlArgs...), kubectl.RunOpts{})
}
