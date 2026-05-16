package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/kubectl"
)

func newScaleCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "scale --replicas=N TYPE/NAME",
		Short:              "Scale a workload; confirms when scaling to 0",
		DisableFlagParsing: true,
		RunE:               runScale,
	}
}

func runScale(cmd *cobra.Command, args []string) error {
	if printHelpIfRequested(cmd, args) {
		return nil
	}
	if len(args) == 0 {
		return errors.New("scale: --replicas=N and TYPE/NAME required")
	}
	yes := Flags.Yes || hasFlag(args, "--yes") || hasFlag(args, "-y")
	kubectlArgs := stripFlag(args, "--yes")
	kubectlArgs = stripFlag(kubectlArgs, "-y")

	replicas, ok := flagValue(kubectlArgs, "--replicas")
	if !ok {
		return errors.New("scale: --replicas=N is required")
	}
	if replicas == "0" {
		target := strings.Join(positionalArgs(kubectlArgs), " ")
		if err := guardrail.YesNo(
			fmt.Sprintf("Scale %s to zero replicas?", target),
			"This will gracefully terminate all pods of the workload.",
			yes,
		); err != nil {
			if errors.Is(err, guardrail.ErrAborted) {
				return nil
			}
			return err
		}
	}

	runner, err := kubectl.Default()
	if err != nil {
		return err
	}
	return runner.Run(cmd.Context(), append([]string{"scale"}, kubectlArgs...), kubectl.RunOpts{})
}
