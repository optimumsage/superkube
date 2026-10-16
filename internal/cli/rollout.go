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
	yes := Flags.Yes || boolFlag(args, "yes")
	kubectlArgs := stripSkFlags(args)

	// args may start with root flags (`sk --context prod rollout ...` leaves
	// `--context prod` in front), so find the subcommand among positionals.
	positionals := positionalArgs(kubectlArgs)
	if len(positionals) == 0 {
		return errors.New("rollout: subcommand required (status|history|restart|undo|pause|resume)")
	}
	sub, rest := positionals[0], positionals[1:]

	// Only status/history are read-only; unknown subcommands are treated as
	// mutating so they fail closed on a broken config.
	readOnly := sub == "status" || sub == "history"
	if readOnly {
		if rule, blocked := Policy().IsForbidden("rollout", policyArgs(args)); blocked {
			return fmt.Errorf("rollout refused by policy %q for context matching %q",
				rule, Policy().MatchedPattern)
		}
	} else if err := guardOp("rollout", args); err != nil {
		return err
	}

	if (sub == "restart" || sub == "undo" || sub == "pause" || sub == "resume") && !isDryRunArgs(kubectlArgs) {
		kind, names := splitTargets(rest)
		var targets []string
		for _, n := range names {
			if kind != "" { // `TYPE a b` -> TYPE/a TYPE/b
				n = kind + "/" + n
			}
			targets = append(targets, n)
		}
		ReshowBanner()
		if err := confirmRollout(sub, targets, hasSource(kubectlArgs), yes); err != nil {
			return err
		}
	}

	runner, err := kubectl.New()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"rollout"}, kubectlArgs...), kubectl.RunOpts{})
}

// confirmRollout gates restart/pause/resume (yes/no) and undo (typed). Every
// target kubectl will touch is counted: several targets need the phrase
// ROLLOUT-N, a manifest/selector source needs ROLLOUT.
func confirmRollout(sub string, targets []string, fromSource, yes bool) error {
	list := strings.Join(targets, ", ")
	switch {
	case sub == "undo" && fromSource:
		return guardrail.TypedPhrase("rollout undo from a manifest/selector", "ROLLOUT", yes)
	case sub == "undo" && len(targets) == 0:
		return errors.New("rollout undo: workload required (e.g. deployment/foo)")
	case sub == "undo" && len(targets) == 1:
		return guardrail.TypedName("rollout undo "+targets[0], targets[0], yes)
	case sub == "undo":
		return guardrail.TypedPhrase(
			fmt.Sprintf("rollout undo %d workloads: %s", len(targets), list),
			fmt.Sprintf("ROLLOUT-%d", len(targets)), yes)
	}
	prompt := "Rollout " + sub
	if list != "" {
		prompt += " " + list
	}
	return confirmYesNo(
		prompt+"?",
		"This changes the running workload.",
		"ROLLOUT",
		yes,
	)
}
