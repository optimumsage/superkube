package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newRolloutCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "rollout SUBCOMMAND",
		Short:              "Manage rollouts; `undo` requires a typed confirmation, restart/pause/resume a yes/no",
		DisableFlagParsing: true,
		RunE:               runRollout,
	}
}

func runRollout(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	// args may start with root flags (`sk --context prod rollout ...` leaves
	// `--context prod` in front), so find the subcommand among positionals.
	positionals := positionalArgs(kubectlArgs)
	if len(positionals) == 0 {
		return errors.New("rollout: subcommand required (status|history|restart|undo|pause|resume)")
	}
	sub, rest := positionals[0], positionals[1:]
	mutating := sub == "restart" || sub == "undo" || sub == "pause" || sub == "resume"

	if mutating {
		if err := guardOp("rollout", args); err != nil {
			return err
		}
	} else if rule, blocked := Policy().IsForbidden("rollout", args); blocked {
		return fmt.Errorf("rollout refused by policy %q for context matching %q",
			rule, Policy().MatchedPattern)
	}

	if mutating {
		target := rolloutTarget(rest)
		ReshowBanner()
		if sub == "undo" {
			if target == "" {
				return errors.New("rollout undo: workload required (e.g. deployment/foo)")
			}
			if err := guardrail.TypedName(
				fmt.Sprintf("rollout undo %s", target),
				target,
				yes,
			); err != nil {
				return err
			}
		} else {
			if err := confirmYesNo(
				strings.TrimSpace(fmt.Sprintf("Rollout %s %s?", sub, target)),
				"This changes the running workload.",
				"ROLLOUT",
				yes,
			); err != nil {
				return err
			}
		}
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"rollout"}, kubectlArgs...), kubectl.RunOpts{})
}

// rolloutTarget names the workload from the positionals after the rollout
// subcommand: `deploy/x` stays as is; `deployment x` becomes `deployment/x`.
func rolloutTarget(rest []string) string {
	switch {
	case len(rest) == 0:
		return ""
	case len(rest) >= 2 && !strings.Contains(rest[0], "/"):
		return rest[0] + "/" + rest[1]
	default:
		return rest[0]
	}
}
